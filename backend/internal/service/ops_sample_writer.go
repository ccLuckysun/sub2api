package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/redis/go-redis/v9"
)

const (
	OpsRealtimeRequestsRedisKey = "sub2api:ops:realtime_requests"

	OpsSampleQueueSize       = 8192
	OpsSampleBatchSize       = 200
	OpsSampleFlushInterval   = 500 * time.Millisecond
	DefaultOpsSampleMaxItems = 2000
	DefaultOpsSampleTTLHours = 24

	opsSampleRedisTimeout      = 3 * time.Second
	opsSampleStopTimeout       = 10 * time.Second
	opsRealtimeSlowThresholdMs = 3000
	opsRealtimeDefaultLimit    = 100
	opsRealtimeMaxLimit        = 500
)

// RealtimeRequestsFilter defines query parameters for the realtime requests ring buffer.
type RealtimeRequestsFilter struct {
	Result string // "all" (default), "success", "failure"
	Model  string // exact match if non-empty and != "__all__"
	Search string // case-insensitive substring across metadata fields
	Limit  int    // default 100, clamped to 1..500
}

// RealtimeRequestsSummary summarizes filtered items from the Redis ring buffer.
type RealtimeRequestsSummary struct {
	Total       int     `json:"total"`
	Success     int     `json:"success"`
	Failure     int     `json:"failure"`
	SuccessRate float64 `json:"success_rate"`
	AvgTotalMs  int64   `json:"avg_total_ms"`
	SlowCount   int     `json:"slow_count"`
}

// RealtimeRequestsResponse is the API response contract for GET /api/v1/admin/ops/realtime-requests.
type RealtimeRequestsResponse struct {
	Enabled       bool                    `json:"enabled"`
	BufferSize    int                     `json:"buffer_size"`
	TTLHours      int                     `json:"ttl_hours"`
	TotalInBuffer int                     `json:"total_in_buffer"`
	Summary       RealtimeRequestsSummary `json:"summary"`
	Items         []monitoring.OpsSample  `json:"items"`
	Models        []string                `json:"models"`
	Timestamp     time.Time               `json:"timestamp"`
}

// RealtimeRequestsReader reads realtime request samples from the ring buffer.
type RealtimeRequestsReader interface {
	ListRealtimeRequests(ctx context.Context, filter RealtimeRequestsFilter) (*RealtimeRequestsResponse, error)
}

type opsSampleRingStore interface {
	PushBatch(ctx context.Context, payloads []any, maxItems int, ttl time.Duration) error
	List(ctx context.Context, maxItems int) ([]string, error)
}

type redisOpsSampleRingStore struct {
	rdb *redis.Client
}

func (s *redisOpsSampleRingStore) PushBatch(ctx context.Context, payloads []any, maxItems int, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return errors.New("redis client unavailable")
	}
	if len(payloads) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	pipe.LPush(ctx, OpsRealtimeRequestsRedisKey, payloads...)
	pipe.LTrim(ctx, OpsRealtimeRequestsRedisKey, 0, int64(maxItems-1))
	pipe.Expire(ctx, OpsRealtimeRequestsRedisKey, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisOpsSampleRingStore) List(ctx context.Context, maxItems int) ([]string, error) {
	if s == nil || s.rdb == nil {
		return nil, errors.New("redis client unavailable")
	}
	return s.rdb.LRange(ctx, OpsRealtimeRequestsRedisKey, 0, int64(maxItems-1)).Result()
}

// OpsSampleWriter asynchronously buffers monitoring.OpsSample snapshots into a
// bounded Redis list ring buffer and serves filtered realtime request queries.
// Submit never waits for queue capacity or Redis I/O. Start/Stop are terminal,
// idempotent lifecycle operations.
type OpsSampleWriter struct {
	store                      opsSampleRingStore
	maxItems                   int
	ttlHours                   int
	ttl                        time.Duration
	logger                     *slog.Logger
	queue                      chan monitoring.OpsSample
	events                     chan opsSampleDropEvent
	mu                         sync.Mutex
	started, stopped           bool
	stop, done                 chan struct{}
	drainCtx                   context.Context
	cancelDrain                context.CancelFunc
	accepted, dropped, written atomic.Uint64
}

type opsSampleDropEvent struct {
	reason string
	rows   int
	err    error
}

type OpsSampleWriterStats struct{ Accepted, Dropped, Written uint64 }

func NewOpsSampleWriter(rdb *redis.Client, maxItems, ttlHours int) *OpsSampleWriter {
	var store opsSampleRingStore
	if rdb != nil {
		store = &redisOpsSampleRingStore{rdb: rdb}
	}
	return newOpsSampleWriterWithStore(store, maxItems, ttlHours)
}

func newOpsSampleWriterWithStore(store opsSampleRingStore, maxItems, ttlHours int) *OpsSampleWriter {
	if maxItems <= 0 {
		maxItems = DefaultOpsSampleMaxItems
	}
	if ttlHours <= 0 {
		ttlHours = DefaultOpsSampleTTLHours
	}
	return &OpsSampleWriter{
		store:    store,
		maxItems: maxItems,
		ttlHours: ttlHours,
		ttl:      time.Duration(ttlHours) * time.Hour,
		logger:   slog.Default(),
		queue:    make(chan monitoring.OpsSample, OpsSampleQueueSize),
		events:   make(chan opsSampleDropEvent, 64),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (w *OpsSampleWriter) Stats() OpsSampleWriterStats {
	if w == nil {
		return OpsSampleWriterStats{}
	}
	return OpsSampleWriterStats{w.accepted.Load(), w.dropped.Load(), w.written.Load()}
}

func (w *OpsSampleWriter) drop(reason string, n int, err error) {
	w.dropped.Add(uint64(n))
	// Logging is asynchronous too: a slow log sink must not delay Submit.
	select {
	case w.events <- opsSampleDropEvent{reason, n, err}:
	default:
	}
}

func (w *OpsSampleWriter) Submit(sample monitoring.OpsSample) bool {
	if w == nil {
		return false
	}
	if strings.TrimSpace(sample.RequestID) == "" || sample.ObservedAt.IsZero() {
		w.drop("invalid_sample", 1, nil)
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started || w.stopped {
		w.drop("writer_not_running", 1, nil)
		return false
	}
	select {
	case w.queue <- sample:
		w.accepted.Add(1)
		return true
	default:
		w.drop("queue_full", 1, nil)
		return false
	}
}

func (w *OpsSampleWriter) Start() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.stopped {
		return
	}
	w.started = true
	w.drainCtx, w.cancelDrain = context.WithCancel(context.Background())
	go w.run()
}

func (w *OpsSampleWriter) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		if w.started {
			close(w.stop)
		} else {
			close(w.done)
		}
	}
	started := w.started
	w.mu.Unlock()
	if !started {
		return
	}
	timer := time.NewTimer(opsSampleStopTimeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return
	case <-timer.C:
		w.cancelDrain()
		<-w.done
	}
}

func (w *OpsSampleWriter) logDrop(event opsSampleDropEvent) {
	w.logger.Warn("ops sample discarded", "reason", event.reason, "rows", event.rows, "dropped_total", w.dropped.Load(), "error", event.err)
}

func (w *OpsSampleWriter) run() {
	defer close(w.done)
	defer func() {
		for {
			select {
			case event := <-w.events:
				w.logDrop(event)
			default:
				return
			}
		}
	}()
	defer w.cancelDrain()
	ticker := time.NewTicker(OpsSampleFlushInterval)
	defer ticker.Stop()
	batch := make([]monitoring.OpsSample, 0, OpsSampleBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if w.store == nil {
			w.drop("redis_unavailable", len(batch), errors.New("redis client unavailable"))
			batch = batch[:0]
			return
		}
		payloads := make([]any, 0, len(batch))
		for _, item := range batch {
			raw, err := json.Marshal(item)
			if err != nil {
				w.drop("marshal_error", 1, err)
				continue
			}
			payloads = append(payloads, string(raw))
		}
		if len(payloads) == 0 {
			batch = batch[:0]
			return
		}
		ctx, cancel := context.WithTimeout(w.drainCtx, opsSampleRedisTimeout)
		err := w.store.PushBatch(ctx, payloads, w.maxItems, w.ttl)
		cancel()
		if err != nil {
			w.drop("redis_error", len(payloads), err)
		} else {
			w.written.Add(uint64(len(payloads)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case event := <-w.events:
			w.logDrop(event)
		case sample := <-w.queue:
			batch = append(batch, sample)
			if len(batch) == OpsSampleBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.stop:
			for {
				if w.drainCtx.Err() != nil {
					// Submit is closed by the state lock, so the remainder is finite.
					n := len(batch)
					for {
						select {
						case <-w.queue:
							n++
						default:
							if n > 0 {
								w.drop("shutdown_timeout", n, w.drainCtx.Err())
							}
							return
						}
					}
				}
				select {
				case sample := <-w.queue:
					batch = append(batch, sample)
					if len(batch) == OpsSampleBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// ListRealtimeRequests reads the Redis ring buffer in a single LRANGE round-trip
// and applies filtering, summary aggregation, and slicing in memory.
func (w *OpsSampleWriter) ListRealtimeRequests(ctx context.Context, filter RealtimeRequestsFilter) (*RealtimeRequestsResponse, error) {
	if w == nil {
		return &RealtimeRequestsResponse{
			Enabled:   false,
			Items:     []monitoring.OpsSample{},
			Models:    []string{},
			Timestamp: time.Now().UTC(),
		}, nil
	}
	if w.store == nil {
		return nil, errors.New("redis client unavailable")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = opsRealtimeDefaultLimit
	} else if limit > opsRealtimeMaxLimit {
		limit = opsRealtimeMaxLimit
	}

	rawItems, err := w.store.List(ctx, w.maxItems)
	if err != nil {
		return nil, err
	}

	parsed := make([]monitoring.OpsSample, 0, len(rawItems))
	modelSet := make(map[string]struct{})
	for _, raw := range rawItems {
		var item monitoring.OpsSample
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}
		parsed = append(parsed, item)
		if item.Model != nil {
			if m := strings.TrimSpace(*item.Model); m != "" {
				modelSet[m] = struct{}{}
			}
		}
	}

	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	sort.Strings(models)

	resultFilter := strings.ToLower(strings.TrimSpace(filter.Result))
	modelFilter := strings.TrimSpace(filter.Model)
	searchFilter := strings.ToLower(strings.TrimSpace(filter.Search))

	filtered := make([]monitoring.OpsSample, 0, len(parsed))
	var summary RealtimeRequestsSummary
	var totalMsSum int64
	var totalMsCount int64

	for _, item := range parsed {
		isSuccess := opsSampleIsSuccess(item)
		switch resultFilter {
		case "success":
			if !isSuccess {
				continue
			}
		case "failure":
			if isSuccess {
				continue
			}
		}

		if modelFilter != "" && modelFilter != "__all__" {
			if item.Model == nil || *item.Model != modelFilter {
				continue
			}
		}

		if searchFilter != "" && !opsSampleMatchesSearch(item, searchFilter) {
			continue
		}

		filtered = append(filtered, item)
		summary.Total++
		if isSuccess {
			summary.Success++
		} else {
			summary.Failure++
		}
		if item.TotalMs != nil {
			totalMsSum += int64(*item.TotalMs)
			totalMsCount++
			if *item.TotalMs >= opsRealtimeSlowThresholdMs {
				summary.SlowCount++
			}
		}
	}

	if summary.Total > 0 {
		summary.SuccessRate = math.Round(float64(summary.Success)*1000/float64(summary.Total)) / 10
	}
	if totalMsCount > 0 {
		summary.AvgTotalMs = totalMsSum / totalMsCount
	}

	items := filtered
	if len(items) > limit {
		items = items[:limit]
	}

	return &RealtimeRequestsResponse{
		Enabled:       true,
		BufferSize:    w.maxItems,
		TTLHours:      w.ttlHours,
		TotalInBuffer: len(parsed),
		Summary:       summary,
		Items:         items,
		Models:        models,
		Timestamp:     time.Now().UTC(),
	}, nil
}

func opsSampleIsSuccess(item monitoring.OpsSample) bool {
	return item.Result != nil && *item.Result == "success"
}

func opsSampleMatchesSearch(item monitoring.OpsSample, needleLower string) bool {
	if strings.Contains(strings.ToLower(item.RequestID), needleLower) {
		return true
	}
	for _, field := range []*string{
		item.ClientRequestID,
		item.UserEmail,
		item.APIKeyName,
		item.AccountName,
		item.GroupName,
		item.Model,
		item.Endpoint,
		item.FailureReason,
	} {
		if field != nil && strings.Contains(strings.ToLower(*field), needleLower) {
			return true
		}
	}
	return false
}
