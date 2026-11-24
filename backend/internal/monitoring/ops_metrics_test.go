package monitoring

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func exerciseOpsMetrics(m *OpsMetrics) {
	m.RequestPhase.WithLabelValues("body_read").Observe(.02)
	m.TransferBytes.WithLabelValues("in_wire").Add(42)
	m.RequestBody.WithLabelValues("responses").Observe(42)
	m.UpstreamRequests.WithLabelValues("unknown", "success", "").Inc()
	m.ModelRequests.WithLabelValues("gpt", "success").Inc()
	m.Attempts.WithLabelValues("first_success").Inc()
	m.AccountSwitches.Inc()
	m.ClientAbort.WithLabelValues("body_read").Inc()
	m.GatewayRequests.WithLabelValues("success", "").Inc()
	m.UpstreamTTFT.WithLabelValues("unknown").Observe(.02)
	m.GatewayRequestDuration.Observe(.15)
	m.WSActiveConnections.WithLabelValues("responses").Set(1)
	m.WSConnectionsTotal.WithLabelValues("responses", "connected").Inc()
	m.RecordAppState(AppStateSnapshot{
		DBActiveConnections:         4,
		DBIdleConnections:           8,
		RedisUp:                     true,
		AccountsActive:              12,
		AccountsConcurrencyCapacity: 120,
		ConcurrencyQueueDepth:       3,
	})
}

func TestOpsMetricContract(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m, err := NewOpsMetrics(reg)
	require.NoError(t, err)
	exerciseOpsMetrics(m)
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 19)
	duration := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}
	reqDuration := RequestDurationBuckets()
	body := []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864, 268435456}
	specs := map[string]struct {
		kind    dto.MetricType
		labels  []string
		buckets []float64
	}{
		"sub2api_request_phase_seconds":            {dto.MetricType_HISTOGRAM, []string{"phase"}, duration},
		"sub2api_transfer_bytes_total":             {dto.MetricType_COUNTER, []string{"direction"}, nil},
		"sub2api_request_body_bytes":               {dto.MetricType_HISTOGRAM, []string{"endpoint_class"}, body},
		"sub2api_upstream_requests_total":          {dto.MetricType_COUNTER, []string{"account", "failure_reason", "result"}, nil},
		"sub2api_model_requests_total":             {dto.MetricType_COUNTER, []string{"model_class", "result"}, nil},
		"sub2api_attempts_total":                   {dto.MetricType_COUNTER, []string{"outcome"}, nil},
		"sub2api_account_switches_total":           {dto.MetricType_COUNTER, nil, nil},
		"sub2api_client_abort_total":               {dto.MetricType_COUNTER, []string{"stage"}, nil},
		"sub2api_metric_coverage_ratio":            {dto.MetricType_GAUGE, []string{"kind"}, nil},
		"sub2api_gateway_requests_total":           {dto.MetricType_COUNTER, []string{"failure_reason", "result"}, nil},
		"sub2api_upstream_ttft_seconds":            {dto.MetricType_HISTOGRAM, []string{"account"}, duration},
		"sub2api_gateway_request_duration_seconds": {dto.MetricType_HISTOGRAM, nil, reqDuration},
		"sub2api_app_db_connections":               {dto.MetricType_GAUGE, []string{"state"}, nil},
		"sub2api_app_redis_up":                     {dto.MetricType_GAUGE, nil, nil},
		"sub2api_app_accounts_active":              {dto.MetricType_GAUGE, nil, nil},
		"sub2api_app_accounts_concurrency_capacity": {dto.MetricType_GAUGE, nil, nil},
		"sub2api_app_concurrency_queue_depth":      {dto.MetricType_GAUGE, nil, nil},
		"sub2api_ws_active_connections":            {dto.MetricType_GAUGE, []string{"endpoint"}, nil},
		"sub2api_ws_connections_total":             {dto.MetricType_COUNTER, []string{"endpoint", "outcome"}, nil},
	}
	for _, family := range families {
		spec, ok := specs[family.GetName()]
		require.True(t, ok, family.GetName())
		require.Equal(t, spec.kind, family.GetType())
		for _, metric := range family.Metric {
			var labels []string
			for _, label := range metric.Label {
				labels = append(labels, label.GetName())
			}
			require.Equal(t, spec.labels, labels, family.GetName())
			if spec.kind == dto.MetricType_HISTOGRAM {
				var boundaries []float64
				for _, bucket := range metric.Histogram.Bucket {
					boundaries = append(boundaries, bucket.GetUpperBound())
				}
				require.Equal(t, spec.buckets, boundaries, family.GetName())
				require.EqualValues(t, 1, metric.Histogram.GetSampleCount())
			}
		}
		t.Logf("%s: %s labels=%v finite_buckets=%v PASS", family.GetName(), spec.kind, spec.labels, spec.buckets)
	}
	require.Equal(t, 4.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("active")))
	require.Equal(t, 8.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("idle")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.AppRedisUp))
	require.Equal(t, 12.0, testutil.ToFloat64(m.AppAccountsActive))
	require.Equal(t, 120.0, testutil.ToFloat64(m.AppAccountsConcurrencyCap))
	require.Equal(t, 3.0, testutil.ToFloat64(m.AppConcurrencyQueueDepth))

	m.RecordAppState(AppStateSnapshot{RedisUp: false})
	require.Equal(t, 0.0, testutil.ToFloat64(m.AppRedisUp))

	// Bucket helpers cannot mutate future histogram definitions.
	OpsDurationBuckets()[0] = 999
	OpsBodyBuckets()[0] = 999
	require.Equal(t, duration, OpsDurationBuckets())
	require.Equal(t, body, OpsBodyBuckets())
}

func TestOpsRegistrationIsolationAndErrors(t *testing.T) {
	// Observe a private registration while the process default registry is empty.
	oldReg, oldGather := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	empty := prometheus.NewRegistry()
	prometheus.DefaultRegisterer, prometheus.DefaultGatherer = empty, empty
	defer func() { prometheus.DefaultRegisterer, prometheus.DefaultGatherer = oldReg, oldGather }()
	reg := prometheus.NewRegistry()
	m, err := NewOpsMetrics(reg)
	require.NoError(t, err)
	exerciseOpsMetrics(m)
	_, err = NewOpsMetrics(reg)
	require.Error(t, err)
	var duplicate prometheus.AlreadyRegisteredError
	require.True(t, errors.As(err, &duplicate))
	got, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, got, 19)
	got, err = empty.Gather()
	require.NoError(t, err)
	require.Empty(t, got)
	_, err = NewOpsMetrics(nil)
	require.Error(t, err)
	// A conflict late in registration must roll back only the new collectors.
	partial := prometheus.NewRegistry()
	existing := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sub2api_gateway_requests_total", Help: "Finished gateway requests, including zero-attempt failures."}, []string{"result", "failure_reason"})
	require.NoError(t, partial.Register(existing))
	_, err = NewOpsMetrics(partial)
	require.Error(t, err)
	require.Contains(t, err.Error(), "M-10")
	require.True(t, partial.Unregister(existing))
	_, err = NewOpsMetrics(partial)
	require.NoError(t, err)
}
