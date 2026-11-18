// Package monitoring provides Prometheus instrumentation for Sub2API:
// a dedicated registry with the HTTP metrics defined in
// docs/MONITORING_API.md, plus the lifecycle of the internal
// /metrics endpoint served on a separate port.
package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metric name constants (see docs/MONITORING_API.md section 2).
const (
	// RequestsTotalName counts finished HTTP requests.
	RequestsTotalName = "sub2api_http_requests_total"
	// RequestDurationName observes full request lifetimes in seconds.
	RequestDurationName = "sub2api_http_request_duration_seconds"
	// RequestsInFlightName gauges requests currently being handled.
	RequestsInFlightName = "sub2api_http_requests_in_flight"
	// OpsEligibleRequestsTotalName counts finished requests eligible for V2 Ops.
	OpsEligibleRequestsTotalName = "sub2api_ops_eligible_requests_total"
)

// Response mode label values.
const (
	ResponseModeSSE   = "sse"
	ResponseModeOther = "other"
)

// MethodOther is the fallback bucket for methods outside the normalized set.
const MethodOther = "OTHER"

// requestDurationBuckets is the explicit bucket list for the duration
// histogram: the client_golang default seconds buckets plus the SSE-aware
// long-tail buckets 30/60/120/300/600. It is spelled out to prevent default
// drift across client_golang upgrades.
var requestDurationBuckets = []float64{
	// client_golang DefBuckets (seconds)
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
	// long-lived streams
	30, 60, 120, 300, 600,
}

// RequestDurationBuckets returns a copy of the explicit histogram bucket
// boundaries (docs/MONITORING_API.md 2.2). Exported for tests and dashboards.
func RequestDurationBuckets() []float64 {
	return append([]float64(nil), requestDurationBuckets...)
}

// Metrics bundles the instrument handles consumed by the HTTP metrics
// middleware. All handles are bound to the registry built by NewRegistry.
type Metrics struct {
	// RequestsTotal counts finished requests by method/route/status_code/response_mode.
	RequestsTotal *prometheus.CounterVec
	// RequestDuration observes the full request lifetime in seconds.
	RequestDuration *prometheus.HistogramVec
	// RequestsInFlight gauges requests currently being handled (no labels).
	RequestsInFlight prometheus.Gauge
	// OpsEligibleRequestsTotal counts completed Ops-eligible requests, even if V2 is disabled.
	OpsEligibleRequestsTotal prometheus.Counter
}

// NormalizeMethod maps a request method onto the bounded label set.
// Unknown or empty methods collapse to MethodOther so the label can never
// become an unbounded value source.
func NormalizeMethod(method string) string {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return method
	default:
		return MethodOther
	}
}

// NewRegistry builds a private registry, registers the sub2api HTTP metrics
// and the standard go/process collectors on it, and returns the registry
// together with the metric handles. The default global registry is never
// touched, so the metrics server only ever exposes what is registered here.
func NewRegistry() (*prometheus.Registry, *Metrics) {
	registry := prometheus.NewRegistry()

	factory := promauto.With(registry)

	metrics := &Metrics{
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: RequestsTotalName,
			Help: "Total number of finished HTTP requests, labeled by method, route template, status code and response mode.",
		}, []string{"method", "route", "status_code", "response_mode"}),
		RequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    RequestDurationName,
			Help:    "Full HTTP request lifetime in seconds (not time-to-first-byte; SSE durations cover the whole connection).",
			Buckets: requestDurationBuckets,
		}, []string{"method", "route", "response_mode"}),
		RequestsInFlight: factory.NewGauge(prometheus.GaugeOpts{
			Name: RequestsInFlightName,
			Help: "Number of HTTP requests currently being handled.",
		}),
		OpsEligibleRequestsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: OpsEligibleRequestsTotalName,
			Help: "Total number of finished HTTP requests eligible for V2 Ops instrumentation, regardless of whether V2 Ops is enabled.",
		}),
	}

	// Runtime metrics come from the standard collectors on the same registry.
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	return registry, metrics
}
