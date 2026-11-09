package requesttiming

import (
	"context"
	"sync"
)

// RequestLifecycle also tracks hijacked gateway connections, which net/http's
// Shutdown does not await. The usage worker pool cannot be drained until these
// request producers have observed cancellation and returned.
type RequestLifecycle struct {
	mu      sync.Mutex
	closing bool
	next    uint64
	active  map[uint64]context.CancelFunc
	wg      sync.WaitGroup
}

func (l *RequestLifecycle) Begin(parent context.Context) (context.Context, func(), bool) {
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return parent, func() {}, false
	}
	if l.active == nil {
		l.active = make(map[uint64]context.CancelFunc)
	}
	l.next++
	id := l.next
	ctx, cancel := context.WithCancel(parent)
	l.active[id] = cancel
	l.wg.Add(1)
	l.mu.Unlock()
	var once sync.Once
	done := func() { once.Do(func() { cancel(); l.mu.Lock(); delete(l.active, id); l.mu.Unlock(); l.wg.Done() }) }
	return ctx, done, true
}

func (l *RequestLifecycle) Stop(ctx context.Context) error {
	l.mu.Lock()
	l.closing = true
	for _, cancel := range l.active {
		cancel()
	}
	l.mu.Unlock()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
