package requesttiming

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu      sync.Mutex
	patches []TimingPatch
}

func (s *recordingSink) Enqueue(p TimingPatch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patches = append(s.patches, p)
	return true
}

func (s *recordingSink) snapshot() []TimingPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TimingPatch, len(s.patches))
	copy(out, s.patches)
	return out
}

func testRow() RowRef {
	return RowRef{
		ID:        42,
		CreatedAt: time.Date(2026, 11, 8, 12, 34, 56, 0, time.FixedZone("x", 8*3600)),
		APIKeyID:  7,
		RequestID: "req-1",
	}
}

func TestHandle_RecordBodyBytesKeepsFirstObservationIncludingZero(t *testing.T) {
	h := New(time.Now(), nil)
	h.RecordBodyBytes(0)
	h.RecordBodyBytes(99)
	if got := h.BodyBytes(); got == nil || *got != 0 {
		t.Fatalf("BodyBytes() = %v, want 0", got)
	}

	unseen := New(time.Now(), nil)
	if got := unseen.BodyBytes(); got != nil {
		t.Fatalf("BodyBytes() = %v, want nil", *got)
	}
}

func TestHandle_EnqueuesOnceWhenEligibleFinalizedAndRegisteredInEitherOrder(t *testing.T) {
	orders := []struct {
		name string
		run  func(h *Handle)
	}{
		{
			name: "finalize_then_register",
			run: func(h *Handle) {
				h.Finalize(h.start.Add(1500 * time.Millisecond))
				h.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
			},
		},
		{
			name: "register_then_finalize",
			run: func(h *Handle) {
				h.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
				h.Finalize(h.start.Add(1500 * time.Millisecond))
			},
		},
	}
	for _, tt := range orders {
		t.Run(tt.name, func(t *testing.T) {
			sink := &recordingSink{}
			h := New(time.Now(), sink)
			h.MarkEligible()
			tt.run(h)

			patches := sink.snapshot()
			if len(patches) != 1 {
				t.Fatalf("enqueued %d patches, want 1", len(patches))
			}
			if patches[0].GatewayLatencyMs != 1500 {
				t.Fatalf("latency = %d, want 1500", patches[0].GatewayLatencyMs)
			}
			if got, want := patches[0].MinuteStart, time.Date(2026, 11, 8, 4, 34, 0, 0, time.UTC); !got.Equal(want) {
				t.Fatalf("MinuteStart = %v, want %v", got, want)
			}
			if patches[0].Reason != ReasonLateRegister {
				t.Fatalf("reason = %q", patches[0].Reason)
			}

			// Late duplicate events must not enqueue again.
			h.MarkEligible()
			h.Finalize(h.start.Add(9 * time.Second))
			h.RegisterRow(InsertReceipt{Inserted: true, Row: RowRef{ID: 99, CreatedAt: time.Now()}})
			if got := len(sink.snapshot()); got != 1 {
				t.Fatalf("enqueued %d patches after duplicates, want 1", got)
			}
			if got, want := h.End(), h.start.Add(1500*time.Millisecond); !got.Equal(want) {
				t.Fatalf("End() = %v, want first finalized %v", got, want)
			}
		})
	}
}

func TestHandle_DoesNotEnqueueWhenIneligibleOrNotInserted(t *testing.T) {
	sink := &recordingSink{}

	ineligible := New(time.Now(), sink)
	ineligible.Finalize(time.Now())
	ineligible.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("ineligible request enqueued %d patches", got)
	}

	conflict := New(time.Now(), sink)
	conflict.MarkEligible()
	conflict.Finalize(time.Now())
	conflict.RegisterRow(InsertReceipt{Inserted: false, Row: testRow()})
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("conflict receipt enqueued %d patches", got)
	}

	invalidRow := New(time.Now(), sink)
	invalidRow.MarkEligible()
	invalidRow.Finalize(time.Now())
	invalidRow.RegisterRow(InsertReceipt{Inserted: true})
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("zero row enqueued %d patches", got)
	}
}

func TestHandle_ClampsNegativeLatencyAndIgnoresLateFinalize(t *testing.T) {
	h := New(time.Now(), nil)
	end := h.start.Add(-time.Second)
	h.Finalize(end)
	if !h.Finalized() {
		t.Fatal("Finalize must set finalized")
	}
	if got := h.GatewayLatencyMs(); got == nil || *got != 0 {
		t.Fatalf("GatewayLatencyMs() = %v, want 0", got)
	}
	later := end.Add(2 * time.Second)
	h.Finalize(later)
	if got := h.End(); !got.Equal(end) {
		t.Fatalf("late Finalize changed end to %v, want %v", got, end)
	}
}

func TestHandle_ContextRoundTripAndNilSafety(t *testing.T) {
	var nilHandle *Handle
	nilHandle.RecordBodyBytes(1)
	nilHandle.MarkEligible()
	nilHandle.Finalize(time.Now())
	nilHandle.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
	if nilHandle.BodyBytes() != nil || nilHandle.GatewayLatencyMs() != nil || nilHandle.Finalized() || !nilHandle.End().IsZero() {
		t.Fatal("nil handle must be a no-op")
	}

	ctx := context.Background()
	if FromContext(ctx) != nil {
		t.Fatal("empty context returned a handle")
	}
	h := New(time.Now(), nil)
	ctx = WithContext(ctx, h)
	if got := FromContext(ctx); got != h {
		t.Fatalf("FromContext() = %p, want %p", got, h)
	}
	if FromContext(nil) != nil {
		t.Fatal("nil context returned a handle")
	}
	if WithContext(nil, h) != nil {
		t.Fatal("WithContext(nil, h) must stay nil")
	}
}

func TestHandle_ConcurrentEventsEnqueueExactlyOnce(t *testing.T) {
	sink := &recordingSink{}
	h := New(time.Now(), sink)
	row := testRow()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			h.RecordBodyBytes(10)
		}()
		go func() {
			defer wg.Done()
			h.MarkEligible()
		}()
		go func() {
			defer wg.Done()
			h.Finalize(time.Now())
		}()
		go func() {
			defer wg.Done()
			h.RegisterRow(InsertReceipt{Inserted: true, Row: row})
		}()
	}
	wg.Wait()

	if got := len(sink.snapshot()); got != 1 {
		t.Fatalf("enqueued %d patches, want exactly 1", got)
	}
	if got := h.BodyBytes(); got == nil || *got != 10 {
		t.Fatalf("BodyBytes() = %v, want 10", got)
	}
}

func TestHandle_OverflowKeepsLatencyNullAndCountsOnce(t *testing.T) {
	before := LatencyOverflowCount()
	sink := &recordingSink{}
	h := New(time.Now(), sink)
	h.MarkEligible()
	h.Finalize(h.start.Add(time.Duration(maxGatewayLatencyMs+1) * time.Millisecond))
	h.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})

	if h.GatewayLatencyMs() != nil {
		t.Fatal("overflowing latency must stay nil")
	}
	if !h.LatencyOverflow() {
		t.Fatal("LatencyOverflow() = false")
	}
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("overflow enqueued %d patches", got)
	}
	if got := LatencyOverflowCount(); got != before+1 {
		t.Fatalf("LatencyOverflowCount() = %d, want %d", got, before+1)
	}
	h.Finalize(h.start.Add(time.Second))
	if got := LatencyOverflowCount(); got != before+1 {
		t.Fatal("repeated Finalize counted overflow twice")
	}
}

func TestHandle_InvalidateSuppressesLateEnrichment(t *testing.T) {
	sink := &recordingSink{}
	h := New(time.Now(), sink)
	h.MarkEligible()
	h.Invalidate()
	h.Finalize(time.Now())
	h.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("invalidated request enqueued %d patches", got)
	}
}

type reentrantSink struct {
	h      *Handle
	called int
}

func (s *reentrantSink) Enqueue(p TimingPatch) bool {
	_ = s.h.BodyBytes()
	_ = s.h.GatewayLatencyMs()
	s.called++
	return true
}

func TestHandle_SinkMayReenterHandleAfterEnqueueLockReleased(t *testing.T) {
	sink := &reentrantSink{}
	h := New(time.Now(), sink)
	sink.h = h
	h.MarkEligible()
	h.Finalize(h.start.Add(time.Second))
	h.RegisterRow(InsertReceipt{Inserted: true, Row: testRow()})
	if sink.called != 1 {
		t.Fatalf("sink called %d times, want 1", sink.called)
	}
}
