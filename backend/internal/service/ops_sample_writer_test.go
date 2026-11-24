package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sampleRingStoreStub struct {
	mu      sync.Mutex
	sizes   []int
	items   []string
	calls   chan int
	block   <-chan struct{}
	pushErr error
	listErr error
}

func (s *sampleRingStoreStub) PushBatch(ctx context.Context, payloads []any, maxItems int, ttl time.Duration) error {
	s.mu.Lock()
	s.sizes = append(s.sizes, len(payloads))
	s.mu.Unlock()
	if s.calls != nil {
		select {
		case s.calls <- len(payloads):
		default:
		}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.pushErr != nil {
		return s.pushErr
	}
	s.mu.Lock()
	for _, p := range payloads {
		s.items = append([]string{fmt.Sprint(p)}, s.items...)
	}
	if len(s.items) > maxItems {
		s.items = s.items[:maxItems]
	}
	s.mu.Unlock()
	return nil
}

func (s *sampleRingStoreStub) List(ctx context.Context, maxItems int) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.items)
	if n > maxItems {
		n = maxItems
	}
	out := make([]string, n)
	copy(out, s.items[:n])
	return out, nil
}

func validSample(id int) monitoring.OpsSample {
	return monitoring.OpsSample{ObservedAt: time.Now().UTC(), RequestID: fmt.Sprintf("sample-%d", id)}
}

func testSampleWriterWithStub(store opsSampleRingStore) (*OpsSampleWriter, *bytes.Buffer) {
	w := newOpsSampleWriterWithStore(store, DefaultOpsSampleMaxItems, DefaultOpsSampleTTLHours)
	var logs bytes.Buffer
	w.logger = slog.New(slog.NewTextHandler(&logs, nil))
	return w, &logs
}

func TestOpsSampleWriterBatchAndTimer(t *testing.T) {
	store := &sampleRingStoreStub{calls: make(chan int, 8)}
	w, _ := testSampleWriterWithStub(store)
	w.Start()
	w.Start()
	defer w.Stop()

	start := time.Now()
	for i := 0; i < OpsSampleBatchSize; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	select {
	case size := <-store.calls:
		require.Equal(t, OpsSampleBatchSize, size)
		require.Less(t, time.Since(start), OpsSampleFlushInterval)
	case <-time.After(2 * time.Second):
		t.Fatal("size flush missing")
	}

	require.True(t, w.Submit(validSample(OpsSampleBatchSize)))
	select {
	case size := <-store.calls:
		require.Equal(t, 1, size)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("timer flush missing")
	}

	w.Stop()
	w.Stop()
	w.Start()
	require.False(t, w.Submit(validSample(OpsSampleBatchSize+1)))
	require.EqualValues(t, OpsSampleBatchSize+1, w.Stats().Written)
}

func TestOpsSampleWriterQueueFullDoesNotWaitForRedis(t *testing.T) {
	gate := make(chan struct{})
	store := &sampleRingStoreStub{calls: make(chan int, 2), block: gate}
	w, logs := testSampleWriterWithStub(store)
	w.Start()
	for i := 0; i < OpsSampleBatchSize; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	<-store.calls // Redis blocked with a full batch.
	for i := 0; i < OpsSampleQueueSize; i++ {
		require.True(t, w.Submit(validSample(i+OpsSampleBatchSize)))
	}
	start := time.Now()
	require.False(t, w.Submit(validSample(99999)))
	require.Less(t, time.Since(start), 100*time.Millisecond, "queue-full Submit must not wait on blocked store")
	close(gate)
	w.Stop()
	require.Contains(t, logs.String(), "queue_full")
	stats := w.Stats()
	require.EqualValues(t, OpsSampleBatchSize+OpsSampleQueueSize, stats.Accepted)
	require.Equal(t, stats.Accepted, stats.Written)
	require.EqualValues(t, 1, stats.Dropped)
}

func TestOpsSampleWriterInvalidAndRedisFailure(t *testing.T) {
	store := &sampleRingStoreStub{pushErr: errors.New("injected Redis error")}
	w, logs := testSampleWriterWithStub(store)
	w.Start()
	require.False(t, w.Submit(monitoring.OpsSample{ObservedAt: time.Now(), RequestID: "   "}))
	require.False(t, w.Submit(monitoring.OpsSample{RequestID: "missing-observation"}))
	require.True(t, w.Submit(validSample(1)), "async Redis error cannot fail the submitting request")
	w.Stop()
	require.EqualValues(t, 1, w.Stats().Accepted)
	require.EqualValues(t, 3, w.Stats().Dropped)
	require.Contains(t, logs.String(), "invalid_sample")
	require.Contains(t, logs.String(), "redis_error")
	require.Contains(t, logs.String(), "injected Redis error")
}

func TestOpsSampleWriterNilRedisDropsWithoutBlocking(t *testing.T) {
	w := NewOpsSampleWriter(nil, 0, 0)
	var logs bytes.Buffer
	w.logger = slog.New(slog.NewTextHandler(&logs, nil))
	w.Start()
	require.True(t, w.Submit(validSample(1)))
	w.Stop()
	require.EqualValues(t, 1, w.Stats().Accepted)
	require.EqualValues(t, 1, w.Stats().Dropped)
	require.EqualValues(t, 0, w.Stats().Written)
	require.Contains(t, logs.String(), "redis_unavailable")

	_, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{})
	require.Error(t, err)
}

func TestOpsSampleWriterShutdownDiscardAccounting(t *testing.T) {
	gate := make(chan struct{})
	store := &sampleRingStoreStub{calls: make(chan int, 2), block: gate}
	w, logs := testSampleWriterWithStub(store)
	w.Start()
	for i := 0; i < OpsSampleBatchSize; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	<-store.calls
	for i := 0; i < OpsSampleQueueSize; i++ {
		require.True(t, w.Submit(validSample(i+OpsSampleBatchSize)))
	}
	w.cancelDrain()
	w.Stop()
	stats := w.Stats()
	require.Equal(t, stats.Accepted, stats.Written+stats.Dropped)
	require.EqualValues(t, OpsSampleBatchSize+OpsSampleQueueSize, stats.Dropped)
	require.Contains(t, logs.String(), "shutdown_timeout")
	require.Contains(t, logs.String(), "redis_error")
}

type blockedSampleLogHandler struct {
	entered, release chan struct{}
	once             sync.Once
}

func (h *blockedSampleLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockedSampleLogHandler) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return nil
}
func (h *blockedSampleLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockedSampleLogHandler) WithGroup(string) slog.Handler      { return h }

func TestOpsSampleWriterBlockedLoggerDoesNotBlockSubmit(t *testing.T) {
	handler := &blockedSampleLogHandler{entered: make(chan struct{}), release: make(chan struct{})}
	store := &sampleRingStoreStub{}
	w := newOpsSampleWriterWithStore(store, DefaultOpsSampleMaxItems, DefaultOpsSampleTTLHours)
	w.logger = slog.New(handler)
	w.Start()
	start := time.Now()
	require.False(t, w.Submit(monitoring.OpsSample{RequestID: ""}))
	require.Less(t, time.Since(start), 100*time.Millisecond)
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("background logger not reached")
	}
	for i := 0; i < OpsSampleQueueSize; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	start = time.Now()
	require.False(t, w.Submit(validSample(9000)))
	elapsed := time.Since(start)
	require.Less(t, elapsed, 100*time.Millisecond, "full queue must not wait for blocked log handler")
	close(handler.release)
	w.Stop()
	require.EqualValues(t, OpsSampleQueueSize, w.Stats().Written)
	require.EqualValues(t, 2, w.Stats().Dropped)
}

func TestOpsSampleWriterRedisRingBufferAndQuery(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// Cap at 5 items, TTL 12h.
	w := NewOpsSampleWriter(rdb, 5, 12)
	w.Start()

	base := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	samples := []monitoring.OpsSample{
		{
			ObservedAt:      base.Add(1 * time.Second),
			RequestID:       "req-0-evicted",
			UserID:          int64Ptr(1),
			UserEmail:       strPtr("evicted@example.com"),
			Model:           strPtr("old-model"),
			Result:          strPtr("success"),
			TotalMs:         intPtr(100),
		},
		{
			ObservedAt:      base.Add(2 * time.Second),
			RequestID:       "req-1",
			ClientRequestID: strPtr("cid-1"),
			UserID:          int64Ptr(10),
			UserEmail:       strPtr("alice@example.com"),
			APIKeyID:        int64Ptr(20),
			APIKeyName:      strPtr("alice-prod"),
			APIKeyMasked:    strPtr("sk-alice…1111"),
			AccountID:       int64Ptr(30),
			AccountName:     strPtr("acct-a"),
			GroupID:         int64Ptr(40),
			GroupName:       strPtr("grp-gold"),
			Platform:        strPtr("anthropic"),
			Model:           strPtr("claude-sonnet"),
			EndpointClass:   strPtr("messages"),
			Endpoint:        strPtr("/v1/messages"),
			Result:          strPtr("success"),
			HTTPStatus:      intPtr(200),
			FailureReason:   strPtr(""),
			AttemptCount:    intPtr(1),
			FailedAttempts:  intPtr(0),
			TotalMs:         intPtr(500),
		},
		{
			ObservedAt:        base.Add(3 * time.Second),
			RequestID:         "req-2",
			UserEmail:         strPtr("bob@example.com"),
			APIKeyName:        strPtr("bob-dev"),
			AccountName:       strPtr("acct-b"),
			GroupName:         strPtr("grp-silver"),
			Model:             strPtr("gpt-5"),
			Endpoint:          strPtr("/v1/responses"),
			HTTPMethod:        strPtr("POST"),
			Result:            strPtr("upstream_error"),
			HTTPStatus:        intPtr(502),
			FailureReason:     strPtr("upstream_server_error"),
			ErrorMessage:      strPtr("bad gateway from origin"),
			UpstreamErrorCode: strPtr("origin_timeout"),
			TotalMs:           intPtr(3500),
		},
		{
			ObservedAt:  base.Add(4 * time.Second),
			RequestID:   "req-3",
			UserEmail:   strPtr("carol@example.com"),
			Model:       strPtr("claude-sonnet"),
			Endpoint:    strPtr("/v1/messages"),
			Result:      strPtr("success"),
			HTTPStatus:  intPtr(200),
			TotalMs:     intPtr(4200),
		},
		{
			ObservedAt:  base.Add(5 * time.Second),
			RequestID:   "req-4",
			UserEmail:   strPtr("dave@example.com"),
			Model:       strPtr("gemini-2.5-pro"),
			Endpoint:    strPtr("/v1beta/models"),
			Result:      strPtr("success"),
			HTTPStatus:  intPtr(200),
			TotalMs:     intPtr(1000),
		},
		{
			ObservedAt:  base.Add(6 * time.Second),
			RequestID:   "req-5-newest",
			UserEmail:   strPtr("eve@example.com"),
			Model:       strPtr("gpt-5"),
			Endpoint:    strPtr("/v1/responses"),
			Result:      strPtr("success"),
			HTTPStatus:  intPtr(200),
			TotalMs:     intPtr(800),
		},
	}

	for _, s := range samples {
		require.True(t, w.Submit(s))
	}
	w.Stop()
	require.EqualValues(t, 6, w.Stats().Written)
	require.Zero(t, w.Stats().Dropped)

	// Verify Redis key length (trimmed to 5) and TTL (12h).
	listLen, err := rdb.LLen(context.Background(), OpsRealtimeRequestsRedisKey).Result()
	require.NoError(t, err)
	require.EqualValues(t, 5, listLen)
	ttl := mr.TTL(OpsRealtimeRequestsRedisKey)
	require.Greater(t, ttl, 11*time.Hour)
	require.LessOrEqual(t, ttl, 12*time.Hour)

	// 1. Default query: newest first, distinct sorted models across whole buffer.
	resp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{})
	require.NoError(t, err)
	require.True(t, resp.Enabled)
	require.Equal(t, 5, resp.BufferSize)
	require.Equal(t, 12, resp.TTLHours)
	require.Equal(t, 5, resp.TotalInBuffer)
	require.Equal(t, []string{"claude-sonnet", "gemini-2.5-pro", "gpt-5"}, resp.Models)
	require.Equal(t, []string{"upstream_server_error"}, resp.FailureReasons)
	require.Len(t, resp.Items, 5)
	require.Equal(t, "req-5-newest", resp.Items[0].RequestID)
	require.Equal(t, "req-4", resp.Items[1].RequestID)
	require.Equal(t, "req-3", resp.Items[2].RequestID)
	require.Equal(t, "req-2", resp.Items[3].RequestID)
	require.Equal(t, "req-1", resp.Items[4].RequestID)
	require.Equal(t, 5, resp.Summary.Total)
	require.Equal(t, 4, resp.Summary.Success)
	require.Equal(t, 1, resp.Summary.Failure)
	require.Equal(t, 80.0, resp.Summary.SuccessRate)
	require.EqualValues(t, (500+3500+4200+1000+800)/5, resp.Summary.AvgTotalMs)
	require.Equal(t, 2, resp.Summary.SlowCount)

	// 2. Result filter: failure only.
	failResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Result: "failure"})
	require.NoError(t, err)
	require.Equal(t, 5, failResp.TotalInBuffer)
	require.Len(t, failResp.Items, 1)
	require.Equal(t, "req-2", failResp.Items[0].RequestID)
	require.Equal(t, 1, failResp.Summary.Total)
	require.Equal(t, 0, failResp.Summary.Success)
	require.Equal(t, 1, failResp.Summary.Failure)
	require.Equal(t, 0.0, failResp.Summary.SuccessRate)
	require.Equal(t, []string{"claude-sonnet", "gemini-2.5-pro", "gpt-5"}, failResp.Models)

	// 3. Model filter: exact match + "__all__" passthrough.
	modelResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Model: "claude-sonnet"})
	require.NoError(t, err)
	require.Len(t, modelResp.Items, 2)
	require.Equal(t, "req-3", modelResp.Items[0].RequestID)
	require.Equal(t, "req-1", modelResp.Items[1].RequestID)

	allModelResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Model: "__all__"})
	require.NoError(t, err)
	require.Len(t, allModelResp.Items, 5)

	// 4. Search filter across display metadata (case-insensitive).
	for _, query := range []string{"CID-1", "ALICE@EXAMPLE", "alice-prod", "acct-a", "grp-gold", "UPSTREAM_SERVER_ERROR", "bad gateway", "ORIGIN_TIMEOUT", "post"} {
		searchResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Search: query})
		require.NoError(t, err)
		require.Len(t, searchResp.Items, 1, "query=%s", query)
	}

	// 4b. FailureReason filter: case-insensitive match + "__all__" passthrough.
	frResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{FailureReason: "UPSTREAM_SERVER_ERROR"})
	require.NoError(t, err)
	require.Len(t, frResp.Items, 1)
	require.Equal(t, "req-2", frResp.Items[0].RequestID)
	require.Equal(t, 1, frResp.Summary.Total)
	require.Equal(t, 1, frResp.Summary.Failure)

	allFrResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{FailureReason: "__all__"})
	require.NoError(t, err)
	require.Len(t, allFrResp.Items, 5)

	// 5. Limit slices items while summary still reflects all filtered items.
	limResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, limResp.Items, 2)
	require.Equal(t, 5, limResp.Summary.Total)

	// 6. JSON marshaling contract check (non-null empty slices).
	emptyResp, err := w.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{Search: "no-match"})
	require.NoError(t, err)
	rawJSON, err := json.Marshal(emptyResp)
	require.NoError(t, err)
	require.Contains(t, string(rawJSON), `"items":[]`)
	require.Contains(t, string(rawJSON), `"failure_reasons":["upstream_server_error"]`)
	require.Contains(t, string(rawJSON), `"success_rate":0`)

	// 7. Nil writer returns disabled empty response cleanly.
	var nilWriter *OpsSampleWriter
	disabledResp, err := nilWriter.ListRealtimeRequests(context.Background(), RealtimeRequestsFilter{})
	require.NoError(t, err)
	require.False(t, disabledResp.Enabled)
	require.NotNil(t, disabledResp.Items)
	require.NotNil(t, disabledResp.Models)
	require.NotNil(t, disabledResp.FailureReasons)
}
