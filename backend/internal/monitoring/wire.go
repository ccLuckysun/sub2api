package monitoring

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/wire"
	"github.com/prometheus/client_golang/prometheus"
)

// ProviderSet supplies the registry and the metric handles built in one
// NewRegistry call, plus the internal /metrics server.
var ProviderSet = wire.NewSet(
	ProvideConfiguredRegistryAndMetrics,
	ProvideOpsRuntime,
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
	Ops      *OpsMetrics
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

// ProvideConfiguredRegistryAndMetrics registers V2 on the baseline private
// registry only when enabled. Errors propagate through Wire to startup.
func ProvideConfiguredRegistryAndMetrics(cfg *config.Config) (*RegistryAndMetrics, error) {
	if cfg == nil {
		return nil, fmt.Errorf("monitoring: nil config")
	}
	if cfg.Monitoring.OpsMetricsEnabled && !cfg.Monitoring.MetricsEnabled {
		return nil, fmt.Errorf("monitoring.ops_metrics_enabled requires monitoring.metrics_enabled=true")
	}
	if cfg.Monitoring.OpsSamplesEnabled && !cfg.Monitoring.OpsMetricsEnabled {
		return nil, fmt.Errorf("monitoring.ops_samples_enabled requires monitoring.ops_metrics_enabled=true")
	}
	instruments := ProvideRegistryAndMetrics()
	if cfg.Monitoring.OpsMetricsEnabled {
		if err := registerOps(instruments); err != nil {
			return nil, err
		}
	}
	return instruments, nil
}

func registerOps(instruments *RegistryAndMetrics) error {
	ops, err := NewOpsMetrics(instruments.Registry)
	if err != nil {
		return fmt.Errorf("monitoring.ops_metrics_enabled: %w", err)
	}
	instruments.Ops = ops
	return nil
}

// ProvideOpsRuntime starts coverage flushing after successful registration.
// The application cleanup provider must call Stop before exiting.
func ProvideOpsRuntime(instruments *RegistryAndMetrics, accounts *AccountLabels, sink OpsSampleSink) *OpsRuntime {
	if instruments == nil {
		return nil
	}
	runtime := NewOpsRuntime(instruments.Ops)
	if runtime != nil {
		runtime.Accounts = accounts
		runtime.SampleSink = sink
	}
	runtime.Start()
	return runtime
}
