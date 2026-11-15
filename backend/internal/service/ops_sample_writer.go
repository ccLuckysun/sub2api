package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
)

const (
	OpsSampleQueueSize     = 8192
	OpsSampleBatchSize     = 500
	OpsSampleFlushInterval = time.Second
	opsSampleDBTimeout     = 5 * time.Second
	opsSampleStopTimeout   = 10 * time.Second
)

type OpsSampleStore interface {
	InsertBatch(context.Context, []monitoring.OpsSample) error
}

// OpsSampleWriter never waits for queue capacity or DB work in Submit.
// Start/Stop are terminal, idempotent lifecycle operations. Stop drains until its
// deadline, then explicitly accounts and logs discarded queued/buffered rows.
type OpsSampleWriter struct {
	store                      OpsSampleStore
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

func NewOpsSampleWriter(store OpsSampleStore) *OpsSampleWriter {
	return &OpsSampleWriter{store: store, logger: slog.Default(), queue: make(chan monitoring.OpsSample, OpsSampleQueueSize), events: make(chan opsSampleDropEvent, 64), stop: make(chan struct{}), done: make(chan struct{})}
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
	// Events are bounded; each emitted warning includes the cumulative drop total.
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
		ctx, cancel := context.WithTimeout(w.drainCtx, opsSampleDBTimeout)
		var err error
		if w.store == nil {
			err = context.Canceled
		} else {
			err = w.store.InsertBatch(ctx, batch)
		}
		cancel()
		if err != nil {
			w.drop("database_error", len(batch), err)
		} else {
			w.written.Add(uint64(len(batch)))
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
