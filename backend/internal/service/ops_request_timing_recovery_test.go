package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type timingRecoverySchedulerRepo struct {
	OpsRepository
	RequestTimingRepository
	calls  atomic.Int64
	events chan time.Time
}

func (r *timingRecoverySchedulerRepo) AggregateRequestTiming(ctx context.Context, now time.Time) error {
	r.calls.Add(1)
	select {
	case r.events <- now:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func (r *timingRecoverySchedulerRepo) TimingAggregationPending(context.Context, time.Time) (bool, error) {
	return r.calls.Load() < 2, nil
}

func TestTimingAggregationBacklogSchedulesAnotherQuantumWithoutMinuteWait(t *testing.T) {
	r := &timingRecoverySchedulerRepo{events: make(chan time.Time, 4)}
	s := &OpsAggregationService{opsRepo: r, stopCh: make(chan struct{})}
	s.startTimingAggregation()
	defer s.Stop()
	select {
	case <-r.events:
	case <-time.After(time.Second):
		t.Fatal("startup did not run immediately")
	}
	select {
	case <-r.events:
	case <-time.After(4 * time.Second):
		t.Fatal("backlog still waits a full minute instead of bounded fast catch-up")
	}
	select {
	case <-r.events:
		t.Fatal("idle scheduler busy-looped")
	case <-time.After(50 * time.Millisecond):
	}
}
