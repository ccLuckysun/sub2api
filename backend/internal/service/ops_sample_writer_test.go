package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/stretchr/testify/require"
)

type sampleStoreStub struct {
	mu    sync.Mutex
	sizes []int
	calls chan int
	block <-chan struct{}
	err   error
}

func (s *sampleStoreStub) InsertBatch(ctx context.Context, rows []monitoring.OpsSample) error {
	s.mu.Lock()
	s.sizes = append(s.sizes, len(rows))
	s.mu.Unlock()
	if s.calls != nil {
		select {
		case s.calls <- len(rows):
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
	return s.err
}
func validSample(id int) monitoring.OpsSample {
	return monitoring.OpsSample{ObservedAt: time.Now().UTC(), RequestID: fmt.Sprintf("sample-%d", id)}
}
func testSampleWriter(store OpsSampleStore) (*OpsSampleWriter, *bytes.Buffer) {
	w := NewOpsSampleWriter(store)
	var logs bytes.Buffer
	w.logger = slog.New(slog.NewTextHandler(&logs, nil))
	return w, &logs
}
func TestOpsSampleWriterBatchAndTimer(t *testing.T) {
	store := &sampleStoreStub{calls: make(chan int, 8)}
	w, _ := testSampleWriter(store)
	w.Start()
	w.Start()
	defer w.Stop()
	start := time.Now()
	for i := 0; i < 500; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	select {
	case size := <-store.calls:
		require.Equal(t, 500, size)
		require.Less(t, time.Since(start), time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("size flush missing")
	}
	require.True(t, w.Submit(validSample(500)))
	select {
	case size := <-store.calls:
		require.Equal(t, 1, size)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("1s timer flush missing")
	}
	w.Stop()
	w.Stop()
	w.Start()
	require.False(t, w.Submit(validSample(501)))
	require.EqualValues(t, 501, w.Stats().Written)
}
func TestOpsSampleWriterQueueFullDoesNotWaitForDB(t *testing.T) {
	gate := make(chan struct{})
	store := &sampleStoreStub{calls: make(chan int, 2), block: gate}
	w, logs := testSampleWriter(store)
	w.Start()
	for i := 0; i < 500; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	<-store.calls // DB blocked with a 500-row batch.
	for i := 0; i < 8192; i++ {
		require.True(t, w.Submit(validSample(i+500)))
	}
	start := time.Now()
	require.False(t, w.Submit(validSample(99999)))
	require.Less(t, time.Since(start), 100*time.Millisecond, "queue-full Submit must not wait on the blocked store")
	close(gate)
	w.Stop()
	require.Contains(t, logs.String(), "queue_full")
	stats := w.Stats()
	require.EqualValues(t, 8692, stats.Accepted)
	require.Equal(t, stats.Accepted, stats.Written)
	require.EqualValues(t, 1, stats.Dropped)
}
func TestOpsSampleWriterInvalidAndDBFailure(t *testing.T) {
	store := &sampleStoreStub{err: errors.New("injected DB error")}
	w, logs := testSampleWriter(store)
	w.Start()
	require.False(t, w.Submit(monitoring.OpsSample{ObservedAt: time.Now(), RequestID: "   "}))
	require.False(t, w.Submit(monitoring.OpsSample{RequestID: "missing-observation"}))
	require.True(t, w.Submit(validSample(1)), "async DB error cannot fail the submitting request")
	w.Stop()
	require.EqualValues(t, 1, w.Stats().Accepted)
	require.EqualValues(t, 3, w.Stats().Dropped)
	require.Contains(t, logs.String(), "invalid_sample")
	require.Contains(t, logs.String(), "database_error")
	require.Contains(t, logs.String(), "injected DB error")
}
func TestOpsSampleWriter500QPSAndConcurrentLifecycle(t *testing.T) {
	store := &sampleStoreStub{}
	w, _ := testSampleWriter(store)
	w.Start()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	var worst time.Duration
	for i := 0; i < 1000; i++ {
		<-ticker.C
		start := time.Now()
		ok := w.Submit(validSample(i))
		elapsed := time.Since(start)
		require.True(t, ok)
		if elapsed > worst {
			worst = elapsed
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.Start(); w.Stop() }()
	}
	wg.Wait()
	require.EqualValues(t, 1000, w.Stats().Written)
	require.Zero(t, w.Stats().Dropped)
	require.Less(t, worst, 100*time.Millisecond)
	t.Logf("500 QPS: 1000 accepted/written, 0 dropped; worst Submit=%s", worst)
	unstarted, _ := testSampleWriter(store)
	unstarted.Stop()
	unstarted.Stop()
	unstarted.Start()
	require.False(t, unstarted.Submit(validSample(1)))
	var disabled *OpsSampleWriter
	disabled.Start()
	disabled.Stop()
	require.False(t, disabled.Submit(validSample(1)))
}

func TestOpsSampleWriterShutdownDiscardAccounting(t *testing.T) {
	gate := make(chan struct{})
	store := &sampleStoreStub{calls: make(chan int, 2), block: gate}
	w, logs := testSampleWriter(store)
	w.Start()
	for i := 0; i < 500; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	<-store.calls
	for i := 0; i < 8192; i++ {
		require.True(t, w.Submit(validSample(i+500)))
	}
	// Simulate Stop reaching its ten-second drain deadline without a slow test.
	w.cancelDrain()
	w.Stop()
	stats := w.Stats()
	require.Equal(t, stats.Accepted, stats.Written+stats.Dropped)
	require.EqualValues(t, 8692, stats.Dropped)
	require.Contains(t, logs.String(), "shutdown_timeout")
	require.Contains(t, logs.String(), "database_error")
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
	store := &sampleStoreStub{}
	w := NewOpsSampleWriter(store)
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
	for i := 0; i < 8192; i++ {
		require.True(t, w.Submit(validSample(i)))
	}
	start = time.Now()
	require.False(t, w.Submit(validSample(9000)))
	elapsed := time.Since(start)
	require.Less(t, elapsed, 100*time.Millisecond, "full queue must not wait for the blocked log handler")
	close(handler.release)
	w.Stop()
	require.EqualValues(t, 8192, w.Stats().Written)
	require.EqualValues(t, 2, w.Stats().Dropped)
	t.Logf("blocked log handler: queue-full Submit returned in %s", elapsed)
}
