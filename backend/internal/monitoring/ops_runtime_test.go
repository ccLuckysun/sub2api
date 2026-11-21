package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func opsEnabledConfig() *config.Config {
	return &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
}

func TestConfiguredRegistryV2SharedExposition(t *testing.T) {
	cfg := opsEnabledConfig()
	instruments, err := ProvideConfiguredRegistryAndMetrics(cfg)
	require.NoError(t, err)
	runtime := ProvideOpsRuntime(instruments, nil, nil, nil)
	t.Cleanup(runtime.Stop)
	require.Same(t, instruments.Ops, runtime.Metrics)
	exerciseOpsMetrics(instruments.Ops)
	ProvideMetrics(instruments).RequestsTotal.WithLabelValues("POST", "/v1/messages", "200", "other").Inc()
	srv := ProvideMetricsServer(cfg, ProvideRegistry(instruments))
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, 200, w.Code)
	names := []string{
		"sub2api_request_phase_seconds",
		"sub2api_transfer_bytes_total",
		"sub2api_request_body_bytes",
		"sub2api_upstream_requests_total",
		"sub2api_model_requests_total",
		"sub2api_attempts_total",
		"sub2api_account_switches_total",
		"sub2api_client_abort_total",
		"sub2api_metric_coverage_ratio",
		"sub2api_gateway_requests_total",
		"sub2api_upstream_ttft_seconds",
		"sub2api_gateway_request_duration_seconds",
		"sub2api_app_db_connections",
		"sub2api_app_redis_up",
		"sub2api_app_accounts_active",
		"sub2api_app_accounts_concurrency_capacity",
		"sub2api_app_concurrency_queue_depth",
	}
	for _, name := range names {
		require.Equal(t, 1, strings.Count(w.Body.String(), "# HELP "+name+" "), name)
	}
	require.Equal(t, 1, strings.Count(w.Body.String(), "# HELP sub2api_http_requests_total "))
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotContains(t, names, family.GetName())
	}
	// A failed second registration is returned, preserving the original handles.
	original := instruments.Ops
	require.ErrorContains(t, registerOps(instruments), "monitoring.ops_metrics_enabled")
	require.Same(t, original, instruments.Ops)
}

func TestConfiguredRegistryDisabledAndValidation(t *testing.T) {
	for _, baselineEnabled := range []bool{false, true} {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: baselineEnabled}}
		instruments, err := ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		require.Nil(t, instruments.Ops)
		require.Nil(t, ProvideOpsRuntime(instruments, nil, nil, nil))
		families, err := instruments.Registry.Gather()
		require.NoError(t, err)
		baseline, _ := NewRegistry()
		before, err := baseline.Gather()
		require.NoError(t, err)
		var got, want []string
		for _, f := range families {
			got = append(got, f.GetName())
		}
		for _, f := range before {
			want = append(want, f.GetName())
		}
		require.Equal(t, want, got, "disabled V2 adds no collectors to the existing registry")
	}
	_, err := ProvideConfiguredRegistryAndMetrics(nil)
	require.Error(t, err)
	_, err = ProvideConfiguredRegistryAndMetrics(&config.Config{Monitoring: config.MonitoringConfig{OpsMetricsEnabled: true}})
	require.ErrorContains(t, err, "monitoring.ops_metrics_enabled requires monitoring.metrics_enabled")
	_, err = ProvideConfiguredRegistryAndMetrics(&config.Config{Monitoring: config.MonitoringConfig{OpsSamplesEnabled: true}})
	require.ErrorContains(t, err, "monitoring.ops_samples_enabled requires monitoring.ops_metrics_enabled")
}

func TestOpsRuntimeLifecycleAndIdleFlush(t *testing.T) {
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	r := NewOpsRuntime(m)
	clock := &recorderClock{}
	r.Coverage = NewOpsCoverage(m.Coverage, clock.now)
	ticks := make(chan time.Time)
	var started, stopped atomic.Int32
	r.ticker = func() (<-chan time.Time, func()) { started.Add(1); return ticks, func() { stopped.Add(1) } }
	r.Start()
	r.Start()
	t.Cleanup(r.Stop)
	token := r.Coverage.Entry()
	clock.at(60000)
	ticks <- clock.now()
	require.Eventually(t, func() bool { return testutil.ToFloat64(m.Coverage.WithLabelValues("request")) == 0 }, time.Second, time.Millisecond)
	r.Coverage.Complete(token, CoverageSample{Request: true})
	require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")))
	clock.at(120000)
	ticks <- clock.now()
	require.Eventually(t, func() bool { return testutil.ToFloat64(m.Coverage.WithLabelValues("request")) == -1 }, time.Second, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Stop() }()
	}
	wg.Wait()
	require.EqualValues(t, 1, started.Load())
	require.EqualValues(t, 1, stopped.Load())
	select {
	case <-r.done:
	default:
		t.Fatal("Stop returned before flush goroutine exited")
	}
	r.Start()
	require.EqualValues(t, 1, started.Load(), "stopped runtime cannot relaunch")
	unstarted := NewOpsRuntime(m)
	unstarted.Stop()
	unstarted.Start()
	require.False(t, unstarted.started)
	var disabled *OpsRuntime
	disabled.Start()
	disabled.Stop()
	require.Nil(t, NewOpsRuntime(nil))
}

func TestOpsRuntimeFlushPanicIsolation(t *testing.T) {
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	r := NewOpsRuntime(m)
	ticks := make(chan time.Time)
	var fail atomic.Bool
	panicked := make(chan struct{})
	clock := &recorderClock{}
	r.Coverage = NewOpsCoverage(m.Coverage, func() time.Time {
		if fail.CompareAndSwap(true, false) {
			close(panicked)
			panic("injected flush failure")
		}
		return clock.now()
	})
	r.ticker = func() (<-chan time.Time, func()) { return ticks, func() {} }
	r.Start()
	t.Cleanup(r.Stop)
	fail.Store(true)
	ticks <- clock.now()
	select {
	case <-panicked:
	case <-time.After(time.Second):
		t.Fatal("flush did not reach injected panic")
	}
	// Sending the next tick waits for the previous recovered flush to return.
	clock.at(60000)
	ticks <- clock.now()
	require.Eventually(t, func() bool {
		r.Coverage.mu.Lock()
		defer r.Coverage.mu.Unlock()
		return r.Coverage.minute.Equal(clock.now().Truncate(time.Minute))
	}, time.Second, time.Millisecond)
}

func TestOpsRuntimeAppStatePoller(t *testing.T) {
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	r := NewOpsRuntime(m)
	covTicks := make(chan time.Time)
	appTicks := make(chan time.Time)
	var appStopped atomic.Int32
	var calls atomic.Int32

	r.ticker = func() (<-chan time.Time, func()) { return covTicks, func() {} }
	r.appStateTicker = func() (<-chan time.Time, func()) { return appTicks, func() { appStopped.Add(1) } }
	r.AppStateProbe = AppStateProbeFunc(func(ctx context.Context) AppStateSnapshot {
		n := calls.Add(1)
		if n == 2 {
			panic("injected probe panic")
		}
		return AppStateSnapshot{
			DBActiveConnections:         int(n),
			DBIdleConnections:           int(n * 2),
			RedisUp:                     n%2 == 1,
			AccountsActive:              int64(n * 10),
			AccountsConcurrencyCapacity: int64(n * 50),
			ConcurrencyQueueDepth:       int64(n),
		}
	})

	r.Start()
	t.Cleanup(r.Stop)

	// Initial sample runs asynchronously on Start().
	require.Eventually(t, func() bool {
		return calls.Load() >= 1 && testutil.ToFloat64(m.AppAccountsActive) == 10
	}, time.Second, time.Millisecond)
	require.Equal(t, 1.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("active")))
	require.Equal(t, 2.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("idle")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.AppRedisUp))
	require.Equal(t, 50.0, testutil.ToFloat64(m.AppAccountsConcurrencyCap))
	require.Equal(t, 1.0, testutil.ToFloat64(m.AppConcurrencyQueueDepth))

	// Tick 2 panics but is recovered cleanly.
	appTicks <- time.Now()
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, time.Second, time.Millisecond)

	// Tick 3 updates gauges normally.
	appTicks <- time.Now()
	require.Eventually(t, func() bool { return testutil.ToFloat64(m.AppAccountsActive) == 30 }, time.Second, time.Millisecond)
	require.Equal(t, 3.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("active")))
	require.Equal(t, 6.0, testutil.ToFloat64(m.AppDBConnections.WithLabelValues("idle")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.AppRedisUp))
	require.Equal(t, 150.0, testutil.ToFloat64(m.AppAccountsConcurrencyCap))
	require.Equal(t, 3.0, testutil.ToFloat64(m.AppConcurrencyQueueDepth))

	r.Stop()
	require.EqualValues(t, 1, appStopped.Load())
}

func TestOpsRuntimeAccountSnapshot(t *testing.T) {
	instruments, err := ProvideConfiguredRegistryAndMetrics(opsEnabledConfig())
	require.NoError(t, err)
	accounts := NewAccountLabels([]string{"Primary.A", "primary a"})
	runtime := ProvideOpsRuntime(instruments, accounts, nil, nil)
	t.Cleanup(runtime.Stop)
	require.Same(t, accounts, runtime.Accounts)
	require.Equal(t, "primarya", runtime.Accounts.Label("Primary.A"))
	require.Equal(t, "unknown", runtime.Accounts.Label("primary a"))
	require.Nil(t, ProvideOpsRuntime(nil, accounts, nil, nil))
	require.Nil(t, ProvideOpsRuntime(&RegistryAndMetrics{}, accounts, nil, nil))
}
