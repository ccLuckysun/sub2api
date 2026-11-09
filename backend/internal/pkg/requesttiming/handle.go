// Package requesttiming collects request-level timing facts without retaining
// Gin or request-body references.
//
// A Handle is created by the HTTP logger middleware for gateway requests. It is
// safe for concurrent use: the HTTP handler records the decoded body size and
// marks whether the request is eligible for timing enrichment, the logger
// finalizes the total span, and the asynchronous usage writer registers the
// committed usage row after it knows the real (id, created_at).
package requesttiming

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// maxGatewayLatencyMs is the largest value representable by the
// usage_logs.gateway_latency_ms integer column. Spans beyond this are kept NULL
// instead of being truncated into a misleading value.
const maxGatewayLatencyMs = int64(1<<31 - 1)

var latencyOverflowCount atomic.Uint64
var createdCount atomic.Uint64
var finalizedCount atomic.Uint64
var eligibleCount atomic.Uint64
var registeredCount atomic.Uint64

// ProcessCounters exposes scalar lifecycle coverage without retaining per-ID
// labels or request objects. These are current-node counts, not fleet totals.
func ProcessCounters() map[string]uint64 {
	return map[string]uint64{"created": createdCount.Load(), "finalized": finalizedCount.Load(), "eligible": eligibleCount.Load(), "registered": registeredCount.Load()}
}

// LatencyOverflowCount reports how many finalized spans exceeded the integer
// column range and were therefore kept NULL.
func LatencyOverflowCount() uint64 {
	return latencyOverflowCount.Load()
}

// RowRef identifies a committed usage_logs row. created_at participates in the
// predicate because usage_logs may be partitioned by time in the future.
type RowRef struct {
	ID        int64
	CreatedAt time.Time
	APIKeyID  int64
	RequestID string
}

// InsertReceipt is returned by the usage repository after an insert attempt.
// Row is only meaningful when Inserted is true; duplicate/conflict and
// best-effort-recent hits must never reinterpret an existing row as this
// request's row.
type InsertReceipt struct {
	Inserted bool
	Row      RowRef
}

// TimingPatch asks the enrichment worker to write gateway_latency_ms for one
// committed row and mark the corresponding minute bucket dirty in the same
// transaction.
type TimingPatch struct {
	Row              RowRef
	GatewayLatencyMs int
	MinuteStart      time.Time
	Reason           string
}

// Sink accepts timing patches without blocking the request path.
type Sink interface {
	// Enqueue returns false when the patch was dropped (queue full/stopped).
	Enqueue(TimingPatch) bool
}

// Handle is the lightweight per-request state machine. It intentionally holds
// no Gin context, upstream result, or request body.
type Handle struct {
	start time.Time
	sink  Sink

	mu              sync.Mutex
	bodyBytes       *int64
	bodySeen        bool
	finalized       bool
	end             time.Time
	latencyMs       int
	latencyOverflow bool
	overflowCounted bool
	eligible        bool
	invalidated     bool
	row             *RowRef
	enqueued        bool
}

// New creates a timing handle. start should be captured at the beginning of
// request middleware; sink may be nil when enrichment is disabled.
func New(start time.Time, sink Sink) *Handle {
	createdCount.Add(1)
	return &Handle{start: start, sink: sink}
}

// RecordBodyBytes records the canonical decoded body size at the first
// successful body-read point. Later reads are ignored so a middleware pre-read
// plus a handler re-read cannot double count. A zero-length body is recorded
// as 0, distinct from "not observed" (nil).
func (h *Handle) RecordBodyBytes(n int64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bodySeen {
		return
	}
	h.bodySeen = true
	h.bodyBytes = &n
}

// MarkEligible marks this request as a normal, successful HTTP request that is
// not WS/live/cyber and did not end in a partial/stream error. Only eligible
// requests get gateway_latency_ms enriched. The call is idempotent.
func (h *Handle) MarkEligible() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.eligible {
		eligibleCount.Add(1)
	}
	h.eligible = true
	patch, ok := h.takePatchLocked()
	h.mu.Unlock()
	if ok {
		h.sink.Enqueue(patch)
	}
}

// Finalize fixes the server-side total span once. It is normally called from a
// deferred function in the HTTP logger so panics and early aborts still
// finalize. end must be the same clock reading used for access-log latency.
func (h *Handle) Finalize(end time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.finalized {
		h.mu.Unlock()
		return
	}
	h.finalized = true
	finalizedCount.Add(1)
	h.end = end
	latency := end.Sub(h.start)
	if latency < 0 {
		latency = 0
	}
	ms := latency.Milliseconds()
	if ms > maxGatewayLatencyMs {
		// Preserve NULL rather than truncating a span larger than the integer
		// column can represent.
		h.latencyOverflow = true
		if !h.overflowCounted {
			h.overflowCounted = true
			latencyOverflowCount.Add(1)
		}
	} else {
		h.latencyMs = int(ms)
	}
	patch, ok := h.takePatchLocked()
	h.mu.Unlock()
	if ok {
		h.sink.Enqueue(patch)
	}
}

// Invalidate prevents a request that already marked itself eligible from
// enqueuing a timing patch, for example when the handler panics after
// MarkEligible. It is idempotent.
func (h *Handle) Invalidate() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.invalidated = true
	h.mu.Unlock()
}

// RegisterRow is called exactly when a usage row was inserted by this request
// and the insert is committed (autocommit completed or outer transaction
// committed). Duplicate/conflict/recent-hit/reverted results must not call it.
func (h *Handle) RegisterRow(receipt InsertReceipt) {
	if h == nil || !receipt.Inserted || receipt.Row.ID <= 0 || receipt.Row.CreatedAt.IsZero() {
		return
	}
	h.mu.Lock()
	if h.row != nil {
		h.mu.Unlock()
		return
	}
	row := receipt.Row
	h.row = &row
	registeredCount.Add(1)
	patch, ok := h.takePatchLocked()
	h.mu.Unlock()
	if ok {
		h.sink.Enqueue(patch)
	}
}

// takePatchLocked returns the immutable patch to enqueue at most once, only
// when the request is eligible, the logger has finalized a representable total
// span, and the billing row is known. The caller must invoke Sink.Enqueue after
// releasing h.mu so a sink implementation can never re-enter the handle.
func (h *Handle) takePatchLocked() (TimingPatch, bool) {
	if h.enqueued || h.invalidated || h.latencyOverflow || !h.eligible || !h.finalized || h.row == nil || h.sink == nil {
		return TimingPatch{}, false
	}
	h.enqueued = true
	row := *h.row
	return TimingPatch{
		Row:              row,
		GatewayLatencyMs: h.latencyMs,
		MinuteStart:      row.CreatedAt.UTC().Truncate(time.Minute),
		Reason:           ReasonLateRegister,
	}, true
}

// BodyBytes returns a copy of the observed decoded body size, or nil when the
// body was not observed.
func (h *Handle) BodyBytes() *int64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bodyBytes == nil {
		return nil
	}
	v := *h.bodyBytes
	return &v
}

// GatewayLatencyMs returns the finalized total span when it is already known.
// The asynchronous enricher is still responsible for the normal late path.
func (h *Handle) GatewayLatencyMs() *int {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.finalized || h.latencyOverflow {
		return nil
	}
	v := h.latencyMs
	return &v
}

// LatencyOverflow reports whether the finalized span exceeded the integer
// column range and was kept NULL.
func (h *Handle) LatencyOverflow() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latencyOverflow
}

// Finalized reports whether Finalize has fixed the total span.
func (h *Handle) Finalized() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finalized
}

// End returns the finalized clock reading, or the zero time.
func (h *Handle) End() time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.end
}

const (
	// ReasonLateRegister is used when the row insert commits after the logger
	// finalized (the normal asynchronous billing path).
	ReasonLateRegister = "late_register"
)

type contextKey struct{}

// WithContext attaches a timing handle to a request context. It is used inside
// the HTTP request only; the usage worker must receive the same handle through
// an explicit input field, not by copying arbitrary context keys.
func WithContext(ctx context.Context, h *Handle) context.Context {
	if ctx == nil || h == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, h)
}

// FromContext returns the timing handle attached by the logger middleware.
func FromContext(ctx context.Context) *Handle {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(contextKey{}).(*Handle)
	return h
}
