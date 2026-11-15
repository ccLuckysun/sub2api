package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OpsRequestMeta holds request/sample metadata. Raw values never become metric
// labels: Model and Endpoint are classified and account names use the snapshot.
// AttachMeta replaces this value, so callers should supply the complete metadata.
type OpsRequestMeta struct {
	RequestID                                string
	UserID, APIKeyID, AccountID, GroupID     int64
	Platform, Model, Endpoint, EndpointClass string
}

// FinalizeInput is the response status and the marker supplied by Recovery.
type FinalizeInput struct {
	Status int
	Panic  bool
}

type attemptContextKey struct{}

type recorderContextKey struct{}
type recorderDependenciesKey struct{}
type recorderDependencies struct {
	accounts *AccountLabels
	coverage *OpsCoverage
	sink     OpsSampleSink
}

// WithOpsRecorderDependencies supplies the immutable startup account snapshot
// and shared minute accumulator without global state. It does not create a
// Recorder. A missing snapshot maps every account to unknown.
func WithOpsRecorderDependencies(ctx context.Context, accounts *AccountLabels, coverage *OpsCoverage, sinks ...OpsSampleSink) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	var sink OpsSampleSink
	if len(sinks) > 0 {
		sink = sinks[0]
	}
	return context.WithValue(ctx, recorderDependenciesKey{}, recorderDependencies{accounts, coverage, sink})
}

// BeginGatewayRequest creates an enabled recorder; nil metrics means disabled.
// The returned context must be propagated to the request/application layer.
func BeginGatewayRequest(ctx context.Context, path string, m *OpsMetrics) (context.Context, *OpsRecorder) {
	return beginGatewayRequestAt(ctx, path, m, time.Now)
}

func beginGatewayRequestAt(ctx context.Context, path string, m *OpsMetrics, now func() time.Time) (context.Context, *OpsRecorder) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return ctx, nil
	}
	if now == nil {
		now = time.Now
	}
	deps, _ := ctx.Value(recorderDependenciesKey{}).(recorderDependencies)
	entry := now()
	r := &OpsRecorder{sampleSink: deps.sink, internalRequestID: fmt.Sprintf("internal-%d-%d", entry.UnixNano(), opsRequestSequence.Add(1)), metrics: m, now: now, entry: entry, path: path, accounts: deps.accounts, coverage: deps.coverage, bytes: make(map[string]byteObservation)}
	r.coverageToken = r.coverage.Entry()
	return context.WithValue(ctx, recorderContextKey{}, r), r
}

// RecorderFromContext returns nil for disabled or unrelated contexts.
func RecorderFromContext(ctx context.Context) *OpsRecorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(recorderContextKey{}).(*OpsRecorder)
	return r
}

// WithAttempt carries one application-level attempt through request contexts.
// A nil attempt explicitly masks an inherited handle; nil ctx uses Background.
func WithAttempt(ctx context.Context, a *OpsAttempt) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, attemptContextKey{}, a)
}

// AttemptFromContext returns nil for disabled, unrelated, or nil contexts.
func AttemptFromContext(ctx context.Context) *OpsAttempt {
	if ctx == nil {
		return nil
	}
	attempt, _ := ctx.Value(attemptContextKey{}).(*OpsAttempt)
	return attempt
}

var opsRequestSequence atomic.Uint64

var opsSampleNode = func() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}()

type byteObservation struct {
	n      int64
	status string
}

// OpsRecorder serializes request and attempt events with a single mutex. It
// never reads bodies, starts goroutines, waits for usage, or recovers panics.
// Finalize is a cutoff: later events (including async MarkFinish) are ignored.
type OpsRecorder struct {
	mu                           sync.Mutex
	metrics                      *OpsMetrics
	sampleSink                   OpsSampleSink
	internalRequestID            string
	now                          func() time.Time
	entry                        time.Time
	path                         string
	meta                         OpsRequestMeta
	accounts                     *AccountLabels
	coverage                     *OpsCoverage
	coverageToken                *CoverageToken
	finalized                    bool
	bodyMarked                   bool
	bodyAt                       time.Time
	bodyStatus                   string
	declared                     int64
	firstSend                    time.Time
	wait                         time.Duration
	finishAt                     time.Time
	failureAt                    time.Time
	failureResult, failureReason string
	abortStage                   string
	abortAt                      time.Time
	aborts                       map[string]bool
	bytes                        map[string]byteObservation
	attempts                     []*OpsAttempt
}

func (r *OpsRecorder) AttachMeta(m OpsRequestMeta) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized {
		r.meta = m
	}
}

func (r *OpsRecorder) MarkBodyRead(n, declared int64, status string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || r.bodyMarked {
		slog.Debug("ops: duplicate/late MarkBodyRead ignored")
		return
	}
	if n < 0 {
		n = 0
	}
	r.bodyMarked = true
	r.bodyAt = r.now()
	r.bodyStatus = byteStatus(status)
	r.declared = declared
	r.addBytes("in_wire", n, status)
	if r.metrics.RequestBody != nil {
		r.metrics.RequestBody.WithLabelValues(EndpointClass(r.endpoint())).Observe(float64(n))
	}
}

func (r *OpsRecorder) endpoint() string {
	if r.meta.Endpoint != "" {
		return r.meta.Endpoint
	}
	return r.path
}

func (r *OpsRecorder) AddWait(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || !r.bodyMarked || r.bodyStatus != "complete" || !r.firstSend.IsZero() || d <= 0 {
		return
	}
	// Bound each addition by elapsed pre-send time, avoiding duration overflow.
	available := r.now().Sub(r.bodyAt) - r.wait
	if d > available {
		slog.Warn("ops: wait exceeds pre-send interval; clamping")
		d = available
	}
	if d > 0 {
		r.wait += d
	}
}

// StartAttempt is for application-level attempts, not physical HTTP retries.
func (r *OpsRecorder) StartAttempt(account string) *OpsAttempt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return nil
	}
	a := &OpsAttempt{rec: r, account: r.accounts.Label(account)}
	r.attempts = append(r.attempts, a)
	return a
}

func byteStatus(status string) string {
	switch status {
	case "complete", "partial":
		return status
	default:
		return "unknown"
	}
}

func (r *OpsRecorder) AddBytes(direction string, n int64, status string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized {
		r.addBytes(direction, n, status)
	}
}

// MarkBytesComplete upgrades a direction's observation state, without adding
// bytes or changing the existing AddBytes accumulation/partial semantics.
// It can establish a known-empty observation and is ignored after Finalize.
func (r *OpsRecorder) MarkBytesComplete(direction string) {
	if r == nil {
		return
	}
	switch direction {
	case "in_wire", "req_decoded", "req_upstream", "resp_upstream", "resp_downstream":
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return
	}
	if r.bytes == nil {
		r.bytes = make(map[string]byteObservation)
	}
	observation := r.bytes[direction]
	observation.status = "complete"
	r.bytes[direction] = observation
}

func (r *OpsRecorder) addBytes(direction string, n int64, status string) {
	switch direction {
	case "in_wire", "req_decoded", "req_upstream", "resp_upstream", "resp_downstream":
	default:
		return
	}
	if n < 0 {
		return
	}
	status = byteStatus(status)
	prev, seen := r.bytes[direction]
	// Incomplete observations cannot become complete through later chunks.
	if seen && prev.status != "complete" {
		status = prev.status
	}
	r.bytes[direction] = byteObservation{prev.n + n, status}
	if r.metrics.TransferBytes != nil {
		r.metrics.TransferBytes.WithLabelValues(direction).Add(float64(n))
	}
}

// MarkClientAbort accepts an already established client cancellation only.
// Each valid stage is counted once; EOF/timeouts must not call this method.
func (r *OpsRecorder) MarkClientAbort(stage string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || (stage != "body_read" && stage != "response") {
		return
	}
	if r.aborts == nil {
		r.aborts = make(map[string]bool)
	}
	if r.aborts[stage] {
		return
	}
	r.aborts[stage] = true
	if r.abortStage == "" || stage == "body_read" {
		r.abortStage = stage
		r.abortAt = r.now()
	}
	if r.metrics.ClientAbort != nil {
		r.metrics.ClientAbort.WithLabelValues(stage).Inc()
	}
}

// ClientAborted reports recorded client cancellation without changing state.
// A nil recorder (disabled instrumentation) has no observed cancellation.
func (r *OpsRecorder) ClientAborted() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abortStage != ""
}

func (r *OpsRecorder) MarkSwitch() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized && r.metrics.AccountSwitches != nil {
		r.metrics.AccountSwitches.Inc()
	}
}

// MarkFinish marks usage submission completion, including a dropped submission.
// The caller must signal submission before Finalize; Finalize never waits on it.
func (r *OpsRecorder) MarkFinish() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized && r.finishAt.IsZero() {
		r.finishAt = r.now()
	}
}

// MarkFailure supplies a request-path failure with no attempt (e.g. synchronous
// billing DB failure). Async usage DB failures must never call this method.
func (r *OpsRecorder) MarkFailure(result, reason string, ev FailureEvidence) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return
	}
	r.failureResult, r.failureReason = opsResultReason(result, reason, ev)
	r.failureAt = r.now()
}

func opsResultReason(result, reason string, ev FailureEvidence) (string, string) {
	result = NormalizeResult(result)
	classified := ClassifyFailure(result, ev)
	if classified != "unknown" {
		return result, classified
	}
	switch reason {
	case "upstream_rate_limit", "upstream_auth", "upstream_model_or_request", "upstream_server_error", "upstream_connect_timeout", "upstream_read_timeout", "proxy_unreachable", "client_abort_during_body_read", "client_abort_during_response", "gateway_panic", "gateway_db", "gateway_ratelimit":
		return result, reason
	default:
		return result, "unknown"
	}
}

// OpsAttempt is a logical attempt. All its fields use rec.mu; no lock ordering
// or callback into the Recorder is required during finalization.
type OpsAttempt struct {
	rec            *OpsRecorder
	account        string
	sentAt         time.Time
	lastSentAt     time.Time
	generation     uint64
	candidate      *OpsResponse
	locked         *OpsResponse
	responseLocked bool
	done           bool
	doneAt         time.Time
	result, reason string
}

// OpsResponse identifies one physical send. Retain the handle returned by Sent
// for response callbacks that may arrive after another send has started.
// Fields are private and protected by the owning Recorder mutex.
type OpsResponse struct {
	attempt                    *OpsAttempt
	generation                 uint64
	status                     int
	headersAt, firstAt, bodyAt time.Time
	bodyErr                    error
}

func (a *OpsAttempt) update(fn func(*OpsRecorder)) {
	if a == nil || a.rec == nil {
		return
	}
	r := a.rec
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized && !a.done {
		fn(r)
	}
}

// Sent returns the identity for this physical send, preserving the attempt's
// first send time. A subsequent send never replaces an already locked response.
func (a *OpsAttempt) Sent() *OpsResponse {
	var response *OpsResponse
	a.update(func(r *OpsRecorder) {
		now := r.now()
		a.lastSentAt = now
		if a.sentAt.IsZero() {
			a.sentAt = now
		}
		if r.firstSend.IsZero() {
			r.firstSend = now
		}
		a.generation++
		response = &OpsResponse{attempt: a, generation: a.generation}
		a.candidate = response
	})
	return response
}

// updateResponse resolves the current-response convenience entry under the
// same lock as identity validation. Explicit handles remain valid after Sent.
func (a *OpsAttempt) updateResponse(response *OpsResponse, current bool, fn func(*OpsRecorder, *OpsResponse)) {
	a.update(func(r *OpsRecorder) {
		if current {
			response = a.candidate
		}
		if response == nil || response.attempt != a || (a.responseLocked && response != a.locked) {
			return
		}
		fn(r, response)
	})
}

// NoteHeaders targets the current physical response. Async callbacks should
// use the For variant with the handle returned by their own Sent call.
func (a *OpsAttempt) NoteHeaders(status int) { a.noteHeaders(nil, true, status) }
func (a *OpsAttempt) NoteHeadersFor(response *OpsResponse, status int) {
	a.noteHeaders(response, false, status)
}
func (a *OpsAttempt) noteHeaders(response *OpsResponse, current bool, status int) {
	a.updateResponse(response, current, func(r *OpsRecorder, response *OpsResponse) {
		if response.headersAt.IsZero() {
			response.headersAt = r.now()
			response.status = status
		}
	})
}

func (a *OpsAttempt) NoteFirstByte()                         { a.noteFirstByte(nil, true) }
func (a *OpsAttempt) NoteFirstByteFor(response *OpsResponse) { a.noteFirstByte(response, false) }
func (a *OpsAttempt) noteFirstByte(response *OpsResponse, current bool) {
	a.updateResponse(response, current, func(r *OpsRecorder, response *OpsResponse) {
		if !response.headersAt.IsZero() && response.firstAt.IsZero() {
			response.firstAt = r.now()
		}
	})
}

// UseResponse freezes the selected physical identity, not its future events.
func (a *OpsAttempt) UseResponse()                         { a.useResponse(nil, true) }
func (a *OpsAttempt) UseResponseFor(response *OpsResponse) { a.useResponse(response, false) }
func (a *OpsAttempt) useResponse(response *OpsResponse, current bool) {
	a.updateResponse(response, current, func(r *OpsRecorder, response *OpsResponse) {
		if !a.responseLocked && !response.headersAt.IsZero() {
			a.locked = response
			a.responseLocked = true
		}
	})
}

func (a *OpsAttempt) BodyDone(err error)                           { a.bodyDone(nil, true, err) }
func (a *OpsAttempt) BodyDoneFor(response *OpsResponse, err error) { a.bodyDone(response, false, err) }
func (a *OpsAttempt) bodyDone(response *OpsResponse, current bool, err error) {
	a.updateResponse(response, current, func(r *OpsRecorder, response *OpsResponse) {
		if response.bodyAt.IsZero() {
			response.bodyAt = r.now()
			response.bodyErr = err
		}
	})
}

func (a *OpsAttempt) Done(result, reason string, ev FailureEvidence) {
	if a == nil || a.rec == nil {
		return
	}
	r := a.rec
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || a.done {
		slog.Debug("ops: duplicate/late attempt Done ignored")
		return
	}
	a.settle(result, reason, ev, r.now(), true)
}

func (a *OpsAttempt) settle(result, reason string, ev FailureEvidence, at time.Time, observeTTFT bool) {
	a.done = true
	a.doneAt = at
	a.result, a.reason = opsResultReason(result, reason, ev)
	m := a.rec.metrics
	if m.UpstreamRequests != nil {
		m.UpstreamRequests.WithLabelValues(a.account, a.result, a.reason).Inc()
	}
	if m.ModelRequests != nil {
		m.ModelRequests.WithLabelValues(ModelClass(a.rec.meta.Model), a.result).Inc()
	}
	if observeTTFT && a.responseLocked && !a.locked.firstAt.IsZero() && !a.sentAt.IsZero() && m.UpstreamTTFT != nil {
		m.UpstreamTTFT.WithLabelValues(a.account).Observe(nonnegative(a.locked.firstAt.Sub(a.sentAt)).Seconds())
	}
}

func (a *OpsAttempt) response() OpsResponse {
	if a.responseLocked {
		return *a.locked
	}
	if a.candidate != nil {
		return *a.candidate
	}
	return OpsResponse{}
}

func nonnegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
func boundedTime(t, lo, hi time.Time) time.Time {
	if t.IsZero() || t.Before(lo) {
		return lo
	}
	if t.After(hi) {
		return hi
	}
	return t
}

// Finalize settles all unfinished attempts, then emits exactly one request
// result/outcome and six exclusive phase samples. It never recovers a panic.
func (r *OpsRecorder) Finalize(in FinalizeInput) {
	if r == nil {
		return
	}
	var sample *OpsSample
	r.mu.Lock()
	defer func() {
		r.mu.Unlock()
		if sample != nil {
			submitOpsSample(r.sampleSink, *sample)
		}
	}()
	if r.finalized {
		slog.Debug("ops: duplicate Finalize ignored")
		return
	}
	r.finalized = true
	now := r.now()
	for _, a := range r.attempts {
		if !a.done {
			a.settle("upstream_error", "unknown", FailureEvidence{}, now, false)
		}
	}
	var last *OpsAttempt
	if len(r.attempts) > 0 {
		last = r.attempts[len(r.attempts)-1]
	}
	phases, ttft, hasTTFT := r.phases(now, last)
	m := r.metrics
	if m.RequestPhase != nil {
		for phase, d := range phases {
			m.RequestPhase.WithLabelValues(phase).Observe(d.Seconds())
		}
		if hasTTFT {
			m.RequestPhase.WithLabelValues("headers_end").Observe(ttft.Seconds())
		}
	}
	result, reason := r.requestResult(in, last)
	if m.GatewayRequests != nil {
		m.GatewayRequests.WithLabelValues(result, reason).Inc()
	}
	outcome := "all_failed"
	if result == "success" {
		if len(r.attempts) <= 1 {
			outcome = "first_success"
		} else {
			outcome = "retried_success"
		}
	}
	if m.Attempts != nil {
		m.Attempts.WithLabelValues(outcome).Inc()
	}
	if r.coverage != nil {
		complete := true
		for _, dir := range []string{"in_wire", "req_upstream", "resp_upstream", "resp_downstream"} {
			if r.bytes[dir].status != "complete" {
				complete = false
			}
		}
		r.coverage.Complete(r.coverageToken, CoverageSample{Request: m.RequestPhase != nil, Account: last != nil && last.account != "unknown", Bytes: complete, User: r.meta.UserID != 0 && r.meta.APIKeyID != 0})
	}
	if r.sampleSink != nil {
		value := r.makeSample(in, result, reason, phases, ttft, hasTTFT)
		sample = &value
	}
}

func (r *OpsRecorder) requestResult(in FinalizeInput, last *OpsAttempt) (string, string) {
	if in.Panic {
		return "gateway_error", "gateway_panic"
	}
	result, reason := "", ""
	choose := func(candidateResult, candidateReason string) {
		if candidateResult != "" && (result == "" || failurePriority(candidateReason) < failurePriority(reason)) {
			result, reason = candidateResult, candidateReason
		}
	}
	if last != nil && last.result != "success" {
		choose(last.result, last.reason)
	}
	choose(r.failureResult, r.failureReason)
	if r.abortStage != "" {
		choose("client_abort", ClassifyFailure("client_abort", FailureEvidence{Stage: r.abortStage, ClientCanceled: true}))
	}
	if result != "" {
		return result, reason
	}
	if in.Status >= 400 {
		if in.Status == 429 {
			return "rejected", "gateway_ratelimit"
		}
		if in.Status < 500 {
			return "rejected", "unknown"
		}
		return "gateway_error", "unknown"
	}
	if r.bodyMarked && r.bodyStatus == "partial" {
		return "gateway_error", "unknown"
	}
	if last != nil {
		return last.result, last.reason
	}
	if in.Status >= 200 && in.Status < 400 {
		return "success", ""
	}
	return "gateway_error", "unknown"
}

func failurePriority(reason string) int {
	for i, value := range []string{"upstream_rate_limit", "upstream_auth", "upstream_model_or_request", "upstream_server_error", "upstream_connect_timeout", "upstream_read_timeout", "proxy_unreachable", "client_abort_during_body_read", "client_abort_during_response", "gateway_panic", "gateway_db", "gateway_ratelimit", "unknown"} {
		if value == reason {
			return i
		}
	}
	return 13
}

func (r *OpsRecorder) phases(now time.Time, last *OpsAttempt) (map[string]time.Duration, time.Duration, bool) {
	p := map[string]time.Duration{"body_read": 0, "wait": 0, "gateway": 0, "upstream": 0, "response": 0, "finish": 0}
	terminal := now
	if last != nil {
		terminal = last.doneAt
		if t := last.response().bodyAt; !t.IsZero() {
			terminal = t
		}
	}
	if !r.failureAt.IsZero() {
		terminal = r.failureAt
	}
	if !r.abortAt.IsZero() {
		terminal = r.abortAt
	}
	terminal = boundedTime(terminal, r.entry, now)
	if r.firstSend.IsZero() && (r.bodyStatus == "partial" || r.abortStage == "body_read") {
		terminal = boundedTime(r.bodyAt, r.entry, now)
		if r.abortAt.After(terminal) {
			terminal = boundedTime(r.abortAt, r.entry, now)
		}
		p["body_read"] = terminal.Sub(r.entry)
		return p, 0, false
	}
	if !r.bodyMarked && r.firstSend.IsZero() {
		// Rejected without reading: no fabricated body/wait/finish observations.
		p["gateway"] = terminal.Sub(r.entry)
		return p, 0, false
	}
	body := boundedTime(r.bodyAt, r.entry, terminal)
	send := terminal
	if !r.firstSend.IsZero() {
		send = boundedTime(r.firstSend, body, terminal)
	}
	p["body_read"] = body.Sub(r.entry)
	p["wait"] = r.wait
	if p["wait"] > send.Sub(body) {
		slog.Warn("ops: wait exceeds final pre-send interval; clamping")
		p["wait"] = send.Sub(body)
	}
	p["gateway"] = send.Sub(body) - p["wait"]
	headers := terminal
	hasTTFT := false
	ttft := time.Duration(0)
	if last != nil && !r.firstSend.IsZero() {
		resp := last.response()
		if !resp.headersAt.IsZero() {
			headers = boundedTime(resp.headersAt, send, terminal)
		}
		if last.responseLocked && !last.locked.firstAt.IsZero() && !last.sentAt.IsZero() {
			hasTTFT = true
			ttft = nonnegative(last.locked.firstAt.Sub(last.sentAt))
		}
	}
	p["upstream"] = headers.Sub(send)
	p["response"] = terminal.Sub(headers)
	finish := terminal
	if !r.finishAt.IsZero() {
		finish = boundedTime(r.finishAt, terminal, now)
	}
	p["finish"] = finish.Sub(terminal)
	return p, ttft, hasTTFT
}

// RequestID is stable for the recorder lifetime when metadata has no ID.
func (r *OpsRecorder) RequestID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sampleRequestID()
}
func (r *OpsRecorder) sampleRequestID() string {
	if strings.TrimSpace(r.meta.RequestID) != "" {
		return r.meta.RequestID
	}
	return r.internalRequestID
}
func samplePtr[T any](value T) *T { return &value }
func optionalSampleID(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}
func optionalSampleText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func sampleMilliseconds(duration time.Duration) *int {
	value := nonnegative(duration).Milliseconds()
	// SQL phase columns are INT. Saturate observations beyond ~24.8 days rather
	// than overflowing into negative values or dropping the whole async batch.
	if value > 1<<31-1 {
		value = 1<<31 - 1
	}
	return samplePtr(int(value))
}
func (r *OpsRecorder) makeSample(in FinalizeInput, result, reason string, phases map[string]time.Duration, ttft time.Duration, hasTTFT bool) OpsSample {
	endpoint := r.endpoint()
	class := r.meta.EndpointClass
	if class == "" {
		class = EndpointClass(endpoint)
	}
	failed := 0
	for _, attempt := range r.attempts {
		if attempt.result != "success" {
			failed++
		}
	}
	total := time.Duration(0)
	for _, duration := range phases {
		total += duration
	}
	sample := OpsSample{
		ObservedAt: r.entry, RequestID: r.sampleRequestID(),
		UserID: optionalSampleID(r.meta.UserID), APIKeyID: optionalSampleID(r.meta.APIKeyID), AccountID: optionalSampleID(r.meta.AccountID), GroupID: optionalSampleID(r.meta.GroupID),
		Platform: optionalSampleText(r.meta.Platform), Model: optionalSampleText(r.meta.Model), Endpoint: samplePtr(endpoint), EndpointClass: samplePtr(class),
		Result: samplePtr(result), HTTPStatus: samplePtr(in.Status), FailureReason: samplePtr(reason), AttemptCount: samplePtr(len(r.attempts)), FailedAttempts: samplePtr(failed), Node: samplePtr(opsSampleNode),
		BodyReadMs: sampleMilliseconds(phases["body_read"]), GatewayMs: sampleMilliseconds(phases["gateway"]), WaitMs: sampleMilliseconds(phases["wait"]), UpstreamMs: sampleMilliseconds(phases["upstream"]), ResponseMs: sampleMilliseconds(phases["response"]), FinishMs: sampleMilliseconds(phases["finish"]), TotalMs: sampleMilliseconds(total),
	}
	if hasTTFT {
		sample.HeadersEndMs = sampleMilliseconds(ttft)
	}
	if r.bodyMarked && r.declared >= 0 {
		sample.BytesInDeclared = samplePtr(r.declared)
	}
	bytes := func(direction, source string) (*int64, *string, *string) {
		observation, seen := r.bytes[direction]
		if !seen {
			return nil, samplePtr("unknown"), samplePtr("unknown")
		}
		return samplePtr(observation.n), samplePtr(byteStatus(observation.status)), samplePtr(source)
	}
	sample.BytesInWire, sample.BytesInWireStatus, sample.BytesInWireSource = bytes("in_wire", "body_read")
	sample.BytesUpReqTotal, sample.BytesUpReqTotalStatus, sample.BytesUpReqTotalSource = bytes("req_upstream", "request_body_read")
	sample.BytesRespUpstream, sample.BytesRespUpstreamStatus, sample.BytesRespUpstreamSource = bytes("resp_upstream", "response_body_read")
	sample.BytesRespDownstream, sample.BytesRespDownstreamStatus, sample.BytesRespDownstreamSource = bytes("resp_downstream", "writer_size")
	if decoded, seen := r.bytes["req_decoded"]; seen {
		sample.BytesInDecoded = samplePtr(decoded.n)
	}
	return sample
}

// Only the sink call is isolated; business panics remain Recovery's concern.
// Queue rejection is owned/logged by the writer and cannot affect metrics.
func submitOpsSample(sink OpsSampleSink, sample OpsSample) {
	if sink == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("ops sample sink panic", "panic", recovered)
		}
	}()
	sink.Submit(sample)
}

// UsageLogFields freezes optional usage observations at the caller's instant.
// It must run on the request goroutine before usage submission. The returned
// scalars are independent of Recorder and safe for an asynchronous worker.
// Latency is a pre-submit lower bound: gateway + wait + elapsed since observed
// t_done. It intentionally excludes the future Submit/fallback duration, without
// changing MarkFinish, phase metrics or the final request sample.
func (r *OpsRecorder) UsageLogFields() (bodyBytes, gatewayLatencyMs *int64) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if body, seen := r.bytes["in_wire"]; seen {
		bodyBytes = samplePtr(body.n)
	}
	if len(r.attempts) == 0 || r.firstSend.IsZero() {
		return bodyBytes, nil
	}
	last := r.attempts[len(r.attempts)-1]
	terminal := last.doneAt
	if bodyAt := last.response().bodyAt; !bodyAt.IsZero() {
		terminal = bodyAt
	}
	if !r.failureAt.IsZero() {
		terminal = r.failureAt
	}
	if !r.abortAt.IsZero() {
		terminal = r.abortAt
	}
	if terminal.IsZero() {
		return bodyBytes, nil
	} // Never invent a response terminal.
	now := r.now()
	terminal = boundedTime(terminal, r.entry, now)
	phases, _, _ := r.phases(now, last)
	latency := nonnegative(phases["gateway"] + phases["wait"] + nonnegative(now.Sub(terminal))).Milliseconds()
	return bodyBytes, &latency
}
