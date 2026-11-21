package monitoring

import "context"

// AppStateSnapshot holds low-cardinality application and infrastructure state
// metrics sampled periodically for Prometheus scraping.
type AppStateSnapshot struct {
	DBActiveConnections         int
	DBIdleConnections           int
	RedisUp                     bool
	AccountsActive              int64
	AccountsConcurrencyCapacity int64
	ConcurrencyQueueDepth       int64
}

// AppStateProbe samples current application state. Implementations must use
// bounded timeouts and degrade gracefully on errors without panicking.
type AppStateProbe interface {
	Probe(ctx context.Context) AppStateSnapshot
}

// AppStateProbeFunc adapts a function to AppStateProbe.
type AppStateProbeFunc func(ctx context.Context) AppStateSnapshot

func (f AppStateProbeFunc) Probe(ctx context.Context) AppStateSnapshot {
	if f == nil {
		return AppStateSnapshot{}
	}
	return f(ctx)
}
