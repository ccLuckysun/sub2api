package monitoring

import (
	"log/slog"
	"sync"
	"time"
)

// OpsRuntime owns V2 instruments and the coverage minute-flush lifecycle.
// Metrics, Coverage and Accounts are immutable dependencies once built.
// Construction starts no goroutine; disabled providers return nil.
type OpsRuntime struct {
	Metrics          *OpsMetrics
	Coverage         *OpsCoverage
	SampleSink       OpsSampleSink
	Accounts         *AccountLabels // Immutable startup snapshot; nil accepts no accounts.
	mu               sync.Mutex
	started, stopped bool
	stop, done       chan struct{}
	ticker           func() (<-chan time.Time, func())
}

func NewOpsRuntime(metrics *OpsMetrics) *OpsRuntime {
	if metrics == nil {
		return nil
	}
	return &OpsRuntime{Metrics: metrics, Coverage: NewOpsCoverage(metrics.Coverage, nil), ticker: func() (<-chan time.Time, func()) {
		t := time.NewTicker(time.Minute)
		return t.C, t.Stop
	}}
}

// Start begins exactly one minute ticker. Start after Stop is a no-op.
func (r *OpsRuntime) Start() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	ticks, stopTicker := r.ticker()
	go func() {
		defer close(r.done)
		defer stopTicker()
		for {
			select {
			case <-r.stop:
				return
			case <-ticks:
				safeCoverageFlush(r.Coverage.Flush)
			}
		}
	}()
}

// Stop joins the flush goroutine. Repeated and concurrent calls all wait for
// completion; stopping an unstarted or disabled runtime is safe.
func (r *OpsRuntime) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		if r.started {
			close(r.stop)
		}
	}
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func safeCoverageFlush(flush func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("ops coverage flush failed", "panic", recovered)
		}
	}()
	flush()
}
