package monitoring

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
)

// OpsDurationBuckets returns the explicit M-1/M-11 finite boundaries in seconds.
func OpsDurationBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
}

// OpsBodyBuckets returns the explicit M-3 finite boundaries in bytes.
func OpsBodyBuckets() []float64 {
	return []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864, 268435456}
}

// OpsMetrics holds V2 collectors registered exclusively with the supplied
// registerer. Callers must use the bounded labels defined by the V2 contract.
type OpsMetrics struct {
	RequestPhase     *prometheus.HistogramVec
	TransferBytes    *prometheus.CounterVec
	RequestBody      *prometheus.HistogramVec
	UpstreamRequests *prometheus.CounterVec
	ModelRequests    *prometheus.CounterVec
	Attempts         *prometheus.CounterVec
	AccountSwitches  prometheus.Counter
	ClientAbort      *prometheus.CounterVec
	Coverage         *prometheus.GaugeVec
	GatewayRequests  *prometheus.CounterVec
	UpstreamTTFT     *prometheus.HistogramVec
}

// NewOpsMetrics registers M-1 through M-11 without touching the default registry.
// Registration failures (including duplicates) return errors, never panic.
// Collectors registered by this call are removed if a later registration fails.
func NewOpsMetrics(reg prometheus.Registerer) (*OpsMetrics, error) {
	if reg == nil {
		return nil, fmt.Errorf("ops metrics: nil registerer")
	}
	m := &OpsMetrics{
		RequestPhase:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sub2api_request_phase_seconds", Help: "Gateway request phase durations in seconds; headers_end overlaps other phases.", Buckets: OpsDurationBuckets()}, []string{"phase"}),
		TransferBytes:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_transfer_bytes_total", Help: "Actual transferred bytes by direction."}, []string{"direction"}),
		RequestBody:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sub2api_request_body_bytes", Help: "Actual request body bytes read, including partial reads.", Buckets: OpsBodyBuckets()}, []string{"endpoint_class"}),
		UpstreamRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_upstream_requests_total", Help: "Finished logical upstream attempts."}, []string{"account", "result", "failure_reason"}),
		ModelRequests:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_model_requests_total", Help: "Finished logical upstream attempts by original request model class."}, []string{"model_class", "result"}),
		Attempts:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_attempts_total", Help: "Gateway requests by attempt outcome."}, []string{"outcome"}),
		AccountSwitches:  prometheus.NewCounter(prometheus.CounterOpts{Name: "sub2api_account_switches_total", Help: "Cross-account switches, excluding same-account retries."}),
		ClientAbort:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_client_abort_total", Help: "Client cancellations by request stage."}, []string{"stage"}),
		Coverage:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "sub2api_metric_coverage_ratio", Help: "Per-minute gateway instrumentation coverage; -1 means no samples."}, []string{"kind"}),
		GatewayRequests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_gateway_requests_total", Help: "Finished gateway requests, including zero-attempt failures."}, []string{"result", "failure_reason"}),
		UpstreamTTFT:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sub2api_upstream_ttft_seconds", Help: "Final consumed response first-byte latency per logical attempt.", Buckets: OpsDurationBuckets()}, []string{"account"}),
	}
	collectors := []prometheus.Collector{m.RequestPhase, m.TransferBytes, m.RequestBody, m.UpstreamRequests, m.ModelRequests, m.Attempts, m.AccountSwitches, m.ClientAbort, m.Coverage, m.GatewayRequests, m.UpstreamTTFT}
	for i, c := range collectors {
		if err := reg.Register(c); err != nil {
			for _, added := range collectors[:i] {
				reg.Unregister(added)
			}
			return nil, fmt.Errorf("register ops metric M-%d: %w", i+1, err)
		}
	}
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		m.Coverage.WithLabelValues(kind).Set(-1)
	}
	return m, nil
}
