package monitoring

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const opsAppStatePollInterval = 15 * time.Second

// OpsRuntime owns V2 instruments, the coverage minute-flush lifecycle, and the
// lightweight background app-state poller.
// Metrics, Coverage, Accounts, SampleSink, and AppStateProbe are immutable
// dependencies once built. Construction starts no goroutine; disabled providers
// return nil.
type OpsRuntime struct {
	Metrics          *OpsMetrics
	Coverage         *OpsCoverage
	SampleSink       OpsSampleSink
	Accounts         *AccountLabels // Immutable startup snapshot; nil accepts no accounts.
	AppStateProbe    AppStateProbe
	mu               sync.Mutex
	started, stopped bool
	stop, done       chan struct{}
	cancel           context.CancelFunc
	ticker           func() (<-chan time.Time, func())
	appStateTicker   func() (<-chan time.Time, func())
}

func NewOpsRuntime(metrics *OpsMetrics) *OpsRuntime {
	if metrics == nil {
		return nil
	}
	return &OpsRuntime{
		Metrics:  metrics,
		Coverage: NewOpsCoverage(metrics.Coverage, nil),
		ticker: func() (<-chan time.Time, func()) {
			t := time.NewTicker(time.Minute)
			return t.C, t.Stop
		},
		appStateTicker: func() (<-chan time.Time, func()) {
			t := time.NewTicker(opsAppStatePollInterval)
			return t.C, t.Stop
		},
	}
}

// Start begins the coverage minute ticker and (if AppStateProbe is configured)
// the 15s app-state poller. Start after Stop is a no-op.
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
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	ticks, stopTicker := r.ticker()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
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

	if r.AppStateProbe != nil {
		probe := r.AppStateProbe
		appTicker := r.appStateTicker
		if appTicker == nil {
			appTicker = func() (<-chan time.Time, func()) {
				t := time.NewTicker(opsAppStatePollInterval)
				return t.C, t.Stop
			}
		}
		appTicks, stopAppTicker := appTicker()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer stopAppTicker()
			safeAppStatePoll(ctx, probe, r.Metrics)
			for {
				select {
				case <-r.stop:
					return
				case <-appTicks:
					safeAppStatePoll(ctx, probe, r.Metrics)
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		cancel()
		close(r.done)
	}()
}

// Stop joins all background goroutines. Repeated and concurrent calls all wait
// for completion; stopping an unstarted or disabled runtime is safe.
func (r *OpsRuntime) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		if r.started {
			if r.cancel != nil {
				r.cancel()
			}
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

func safeAppStatePoll(ctx context.Context, probe AppStateProbe, metrics *OpsMetrics) {
	if probe == nil || metrics == nil || ctx.Err() != nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("ops app state probe failed", "panic", recovered)
		}
	}()
	snap := probe.Probe(ctx)
	if ctx.Err() != nil {
		return
	}
	metrics.RecordAppState(snap)
}
