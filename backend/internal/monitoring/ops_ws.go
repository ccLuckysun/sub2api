package monitoring

import (
	"context"
	"sync"
)

type wsObserverContextKey struct{}

// WSObserver tracks lifecycle metrics for a single gateway WebSocket upgrade
// request without touching HTTP 6-phase or OpsRecorder metrics.
type WSObserver struct {
	metrics  *OpsMetrics
	endpoint string

	mu       sync.Mutex
	recorded bool
	active   bool
}

// NewWSObserver creates an idempotent lifecycle observer for supported gateway
// WebSocket endpoints ("responses", "realtime", or "live"). Nil metrics or
// unsupported endpoints return nil so all observer methods behave as safe no-ops.
func NewWSObserver(m *OpsMetrics, endpoint string) *WSObserver {
	if m == nil {
		return nil
	}
	switch endpoint {
	case "responses", "realtime", "live":
	default:
		return nil
	}
	return &WSObserver{
		metrics:  m,
		endpoint: endpoint,
	}
}

// WithWSObserver attaches obs to ctx.
func WithWSObserver(ctx context.Context, obs *WSObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, wsObserverContextKey{}, obs)
}

// WSObserverFromContext extracts the WSObserver stored by OpsMiddleware, or nil.
func WSObserverFromContext(ctx context.Context) *WSObserver {
	if ctx == nil {
		return nil
	}
	obs, _ := ctx.Value(wsObserverContextKey{}).(*WSObserver)
	return obs
}

// MarkRejected records a pre-handshake gateway rejection (e.g. auth, quota,
// concurrency, or unavailable upstream accounts).
func (o *WSObserver) MarkRejected() {
	if o == nil || o.metrics == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.recorded || o.active {
		return
	}
	o.recorded = true
	if o.metrics.WSConnectionsTotal != nil {
		o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, "rejected").Inc()
	}
}

// MarkHandshakeFailed records a WebSocket upgrade failure during Accept.
func (o *WSObserver) MarkHandshakeFailed() {
	if o == nil || o.metrics == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.recorded || o.active {
		return
	}
	o.recorded = true
	if o.metrics.WSConnectionsTotal != nil {
		o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, "handshake_failed").Inc()
	}
}

// MarkConnected increments active WebSocket connections and records a
// connected handshake. Must be paired with MarkClosed (or FinalizeFallback).
func (o *WSObserver) MarkConnected() {
	if o == nil || o.metrics == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.recorded || o.active {
		return
	}
	o.recorded = true
	o.active = true
	if o.metrics.WSActiveConnections != nil {
		o.metrics.WSActiveConnections.WithLabelValues(o.endpoint).Inc()
	}
	if o.metrics.WSConnectionsTotal != nil {
		o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, "connected").Inc()
	}
}

// MarkClosed decrements active connections and records closed_normal or
// closed_error once per accepted connection.
func (o *WSObserver) MarkClosed(normal bool) {
	if o == nil || o.metrics == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return
	}
	o.active = false
	if o.metrics.WSActiveConnections != nil {
		o.metrics.WSActiveConnections.WithLabelValues(o.endpoint).Dec()
	}
	outcome := "closed_error"
	if normal {
		outcome = "closed_normal"
	}
	if o.metrics.WSConnectionsTotal != nil {
		o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, outcome).Inc()
	}
}

// FinalizeFallback ensures middleware rejections (>=400 before the handler) and
// panicked active sessions never go unrecorded or leak active connections.
func (o *WSObserver) FinalizeFallback(status int) {
	if o == nil || o.metrics == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active {
		o.active = false
		if o.metrics.WSActiveConnections != nil {
			o.metrics.WSActiveConnections.WithLabelValues(o.endpoint).Dec()
		}
		if o.metrics.WSConnectionsTotal != nil {
			o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, "closed_error").Inc()
		}
		return
	}
	if !o.recorded && status >= 400 {
		o.recorded = true
		if o.metrics.WSConnectionsTotal != nil {
			o.metrics.WSConnectionsTotal.WithLabelValues(o.endpoint, "rejected").Inc()
		}
	}
}
