package requesttiming

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// EnricherConfig contains bounded worker settings. Zero values are replaced by
// safe defaults; negative values are treated as defaults too.
type EnricherConfig struct {
	Enabled       bool
	Workers       int
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	SQLTimeout    time.Duration
	MaxAttempts   int
}

const (
	defaultEnricherWorkers       = 2
	defaultEnricherQueueSize     = 8192
	defaultEnricherBatchSize     = 128
	defaultEnricherFlushInterval = 20 * time.Millisecond
	defaultEnricherSQLTimeout    = time.Second
	defaultEnricherMaxAttempts   = 3

	enricherRetryBase = 100 * time.Millisecond
)

// EnricherStats is a lock-free snapshot of enricher counters.
type EnricherStats struct {
	Enqueued         uint64
	DroppedQueueFull uint64
	DroppedStopped   uint64
	Batches          uint64
	PatchesApplied   uint64
	PatchMisses      uint64
	DirtyMinutes     uint64
	FailedBatches    uint64
	Retries          uint64
	ExhaustedBatches uint64
}

// Enricher writes gateway_latency_ms into committed usage rows and marks the
// corresponding minute dirty in the same transaction. It is intentionally
// independent from the usage-record worker pool: timing must never delay,
// block, or fail billing.
type Enricher struct {
	Requests   RequestLifecycle
	workCtx    context.Context
	workCancel context.CancelFunc
	db         *sql.DB
	cfg        EnricherConfig
	ch         chan TimingPatch

	mu      sync.RWMutex
	closed  bool
	wg      sync.WaitGroup
	started bool

	enqueued         atomic.Uint64
	droppedQueueFull atomic.Uint64
	droppedStopped   atomic.Uint64
	batches          atomic.Uint64
	patchesApplied   atomic.Uint64
	patchMisses      atomic.Uint64
	dirtyMinutes     atomic.Uint64
	failedBatches    atomic.Uint64
	retries          atomic.Uint64
	exhaustedBatches atomic.Uint64
}

// NewEnricher starts the bounded patch workers. When Enabled is false or db is
// nil, Enqueue drops patches and Drain is a no-op.
func NewEnricher(db *sql.DB, cfg EnricherConfig) *Enricher {
	cfg = normalizeEnricherConfig(cfg)
	e := &Enricher{db: db, cfg: cfg}
	if !cfg.Enabled || db == nil {
		return e
	}
	e.workCtx, e.workCancel = context.WithCancel(context.Background())
	e.ch = make(chan TimingPatch, cfg.QueueSize)
	for i := 0; i < cfg.Workers; i++ {
		e.wg.Add(1)
		go e.run()
	}
	e.started = true
	return e
}

func normalizeEnricherConfig(cfg EnricherConfig) EnricherConfig {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultEnricherWorkers
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultEnricherQueueSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultEnricherBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultEnricherFlushInterval
	}
	if cfg.SQLTimeout <= 0 {
		cfg.SQLTimeout = defaultEnricherSQLTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultEnricherMaxAttempts
	}
	// Configuration cannot turn best-effort telemetry into an unbounded memory,
	// goroutine or retry budget, even when set through environment variables.
	if cfg.Workers > 16 {
		cfg.Workers = 16
	}
	if cfg.QueueSize > 65536 {
		cfg.QueueSize = 65536
	}
	if cfg.BatchSize > 512 {
		cfg.BatchSize = 512
	}
	if cfg.FlushInterval < 5*time.Millisecond {
		cfg.FlushInterval = 5 * time.Millisecond
	}
	if cfg.FlushInterval > time.Second {
		cfg.FlushInterval = time.Second
	}
	if cfg.SQLTimeout < 100*time.Millisecond {
		cfg.SQLTimeout = 100 * time.Millisecond
	}
	if cfg.SQLTimeout > 5*time.Second {
		cfg.SQLTimeout = 5 * time.Second
	}
	if cfg.MaxAttempts > 5 {
		cfg.MaxAttempts = 5
	}
	return cfg
}

// Enqueue never blocks the request path. It returns false when the patch was
// dropped because the enricher is stopped or its bounded queue is full.
func (e *Enricher) Enqueue(patch TimingPatch) bool {
	if e == nil || !e.cfg.Enabled || e.db == nil {
		if e != nil {
			e.droppedStopped.Add(1)
		}
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		e.droppedStopped.Add(1)
		return false
	}
	select {
	case e.ch <- patch:
		e.enqueued.Add(1)
		return true
	default:
		e.droppedQueueFull.Add(1)
		return false
	}
}

// Drain closes the queue so workers flush buffered patches, then waits for
// them. No sender may still be active when Drain is called (see the serial
// shutdown order in wire).
func (e *Enricher) Drain(ctx context.Context) error {
	if e == nil || !e.started {
		return nil
	}
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		close(e.ch)
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if e.workCancel != nil {
			e.workCancel()
		}
		// Canceled telemetry is disposable. Never leave canceled workers writing
		// after the infrastructure stage closes the DB.
		<-done
		return ctx.Err()
	}
}

// Stats returns a snapshot of enricher counters.
func (e *Enricher) Stats() EnricherStats {
	if e == nil {
		return EnricherStats{}
	}
	return EnricherStats{
		Enqueued:         e.enqueued.Load(),
		DroppedQueueFull: e.droppedQueueFull.Load(),
		DroppedStopped:   e.droppedStopped.Load(),
		Batches:          e.batches.Load(),
		PatchesApplied:   e.patchesApplied.Load(),
		PatchMisses:      e.patchMisses.Load(),
		DirtyMinutes:     e.dirtyMinutes.Load(),
		FailedBatches:    e.failedBatches.Load(),
		Retries:          e.retries.Load(),
		ExhaustedBatches: e.exhaustedBatches.Load(),
	}
}

func (e *Enricher) run() {
	defer e.wg.Done()
	for {
		var first TimingPatch
		var ok bool
		select {
		case <-e.workContext().Done():
			for range e.ch {
				e.droppedStopped.Add(1)
			}
			return
		case first, ok = <-e.ch:
		}
		if !ok {
			return
		}
		batch := make([]TimingPatch, 0, e.cfg.BatchSize)
		batch = append(batch, first)

		timer := time.NewTimer(e.cfg.FlushInterval)
	fill:
		for len(batch) < e.cfg.BatchSize {
			select {
			case <-e.workContext().Done():
				timer.Stop()
				e.droppedStopped.Add(uint64(len(batch)))
				for range e.ch {
					e.droppedStopped.Add(1)
				}
				return
			case patch, ok := <-e.ch:
				if !ok {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					e.flush(batch)
					return
				}
				batch = append(batch, patch)
			case <-timer.C:
				break fill
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		e.flush(batch)
	}
}

func (e *Enricher) flush(batch []TimingPatch) {
	if len(batch) == 0 {
		return
	}
	e.batches.Add(1)

	patches := dedupePatches(batch)
	var lastErr error
	for attempt := 1; attempt <= e.cfg.MaxAttempts; attempt++ {
		if e.workContext().Err() != nil {
			e.droppedStopped.Add(uint64(len(patches)))
			return
		}
		if attempt > 1 {
			e.retries.Add(1)
			e.sleepRetry(attempt - 1)
		}
		applied, minutes, err := e.applyBatch(patches)
		if err == nil {
			e.patchesApplied.Add(uint64(applied))
			e.patchMisses.Add(uint64(len(patches) - applied))
			e.dirtyMinutes.Add(uint64(len(minutes)))
			return
		}
		lastErr = err
	}
	e.failedBatches.Add(1)
	e.exhaustedBatches.Add(1)
	logger.L().Warn("request timing enrichment dropped",
		zap.String("component", "requesttiming.enricher"),
		zap.Int("patches", len(patches)),
		zap.Error(lastErr),
	)
}

func (e *Enricher) applyBatch(patches []TimingPatch) (int, map[time.Time]string, error) {
	ctx, cancel := context.WithTimeout(e.workContext(), e.cfg.SQLTimeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	minutes := make(map[time.Time]string)
	applied := 0
	for _, patch := range patches {
		var id int64
		err := tx.QueryRowContext(ctx, PatchGatewayLatencySQL,
			patch.GatewayLatencyMs,
			patch.Row.ID,
			patch.Row.CreatedAt,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		applied++
		minute := patch.MinuteStart
		if minute.IsZero() {
			minute = patch.Row.CreatedAt.UTC().Truncate(time.Minute)
		} else {
			minute = minute.UTC().Truncate(time.Minute)
		}
		reason := patch.Reason
		if reason == "" {
			reason = ReasonLateRegister
		}
		if _, exists := minutes[minute]; !exists {
			minutes[minute] = reason
		}
	}
	for minute, reason := range minutes {
		if _, err := tx.ExecContext(ctx, UpsertDirtyMinuteSQL, minute, reason); err != nil {
			return 0, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return applied, minutes, nil
}

func (e *Enricher) sleepRetry(attempt int) {
	delay := enricherRetryBase
	for i := 1; i < attempt; i++ {
		delay *= 5
		if delay >= 1500*time.Millisecond {
			delay = 1500 * time.Millisecond
			break
		}
	}
	// Small jitter avoids synchronized retries across replicas.
	delay += time.Duration(rand.Int63n(int64(50 * time.Millisecond)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-e.workContext().Done():
	}
}

func (e *Enricher) workContext() context.Context {
	if e.workCtx != nil {
		return e.workCtx
	}
	return context.Background()
}

func dedupePatches(batch []TimingPatch) []TimingPatch {
	out := make([]TimingPatch, 0, len(batch))
	type rowKey struct {
		id int64
		at time.Time
	}
	seen := make(map[rowKey]bool, len(batch))
	for _, patch := range batch {
		if patch.Row.ID <= 0 || patch.Row.CreatedAt.IsZero() {
			continue
		}
		key := rowKey{patch.Row.ID, patch.Row.CreatedAt.UTC()}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = true
		out = append(out, patch)
	}
	return out
}
