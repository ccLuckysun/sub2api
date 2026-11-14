package monitoring

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/wire"
	"github.com/prometheus/client_golang/prometheus"
)

// ProviderSet supplies the registry and the metric handles built in one
// NewRegistry call, plus the internal /metrics server.
var ProviderSet = wire.NewSet(
	ProvideRegistryAndMetrics,
	ProvideRegistry,
	ProvideMetrics,
	ProvideMetricsServer,
)

// RegistryAndMetrics groups the registry with the handles registered on it.
// Wire providers can only return one value (plus an optional error/cleanup),
// so this value is split by ProvideRegistry and ProvideMetrics.
type RegistryAndMetrics struct {
	Registry *prometheus.Registry
	Metrics  *Metrics
}

// ProvideRegistryAndMetrics creates exactly one private registry.
func ProvideRegistryAndMetrics() *RegistryAndMetrics {
	registry, metrics := NewRegistry()
	return &RegistryAndMetrics{Registry: registry, Metrics: metrics}
}

// ProvideRegistry returns the registry used by both server and middleware.
func ProvideRegistry(instruments *RegistryAndMetrics) *prometheus.Registry {
	return instruments.Registry
}

// ProvideMetrics returns the handles registered on that same registry.
func ProvideMetrics(instruments *RegistryAndMetrics) *Metrics {
	return instruments.Metrics
}

// ProvideMetricsServer prepares the metrics endpoint without listening.
// StartEnabled in the application start path binds only when enabled.
func ProvideMetricsServer(cfg *config.Config, registry *prometheus.Registry) *Server {
	return &Server{
		bind:    cfg.Monitoring.MetricsBind,
		enabled: cfg.Monitoring.MetricsEnabled,
		http:    newMetricsHTTPHandler(registry),
	}
}
