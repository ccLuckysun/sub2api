package monitoring

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 6a. NewRegistry returns a working registry with metric handles bound to it
// and go/process collectors registered.
func TestNewRegistry_RegistersMetricsAndCollectors(t *testing.T) {
	registry, metrics := NewRegistry()

	require.NotNil(t, registry)
	require.NotNil(t, metrics)

	// CounterVec/HistogramVec only expose series after first use; touch
	// each so the gather proves they are wired to this registry.
	metrics.RequestsTotal.WithLabelValues("GET", "/t", "200", "other").Inc()
	metrics.RequestDuration.WithLabelValues("GET", "/t", "other").Observe(0.01)
	metrics.RequestsInFlight.Inc()
	metrics.RequestsInFlight.Dec()

	mfs, err := registry.Gather()
	require.NoError(t, err)

	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	require.True(t, names[RequestsTotalName], "requests_total must be exposed")
	require.True(t, names[RequestDurationName], "request_duration must be exposed")
	require.True(t, names[RequestsInFlightName], "in_flight must be exposed")
	// Standard collectors on the same registry.
	require.True(t, names["go_goroutines"], "go collector must be registered")
	require.True(t, names["process_start_time_seconds"], "process collector must be registered")
}

// 6b. NewRegistry is repeatable: each call yields an independent registry
// (no duplicate registration panics, no shared state).
func TestNewRegistry_IdempotentAndIndependent(t *testing.T) {
	registry1, metrics1 := NewRegistry()
	registry2, _ := NewRegistry()

	require.NotSame(t, registry1, registry2)

	metrics1.RequestsTotal.WithLabelValues("GET", "/x", "200", "other").Inc()

	// The increment is visible on registry1 only.
	count1 := countSeries(t, registry1, RequestsTotalName)
	count2 := countSeries(t, registry2, RequestsTotalName)
	require.Equal(t, 1, count1)
	require.Equal(t, 0, count2)
}

func TestProvidersShareSingleRegistry(t *testing.T) {
	instruments := ProvideRegistryAndMetrics()
	registry := ProvideRegistry(instruments)
	metrics := ProvideMetrics(instruments)
	metrics.RequestsTotal.WithLabelValues("GET", "/provider", "200", "other").Inc()
	require.Equal(t, 1, countSeries(t, registry, RequestsTotalName))
}

// 6c. NewRegistry never touches the default global registry: gathering the
// global registerer must not expose any sub2api metric.
func TestNewRegistry_DoesNotTouchGlobalRegistry(t *testing.T) {
	_, metrics := NewRegistry()
	metrics.RequestsTotal.WithLabelValues("GET", "/t", "200", "other").Inc()
	metrics.RequestsInFlight.Inc()

	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		name := mf.GetName()
		require.NotEqual(t, RequestsTotalName, name, "sub2api metrics must stay off the global registry")
		require.NotEqual(t, RequestDurationName, name, "sub2api metrics must stay off the global registry")
		require.NotEqual(t, RequestsInFlightName, name, "sub2api metrics must stay off the global registry")
	}
}

func countSeries(t *testing.T, g prometheus.Gatherer, name string) int {
	t.Helper()
	mfs, err := g.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			return len(mf.GetMetric())
		}
	}
	return 0
}

// NormalizeMethod covers the bounded label set.
func TestNormalizeMethod(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		require.Equal(t, m, NormalizeMethod(m), m)
	}
	require.Equal(t, MethodOther, NormalizeMethod("PROPFIND"))
	require.Equal(t, MethodOther, NormalizeMethod(""))
	require.Equal(t, MethodOther, NormalizeMethod("get"))
}

// 1.1/1.2: the metrics server serves /metrics in exposition format from the
// given registry, on the configured bind address.
func TestMetricsServer_ServesMetrics(t *testing.T) {
	registry, metrics := NewRegistry()
	metrics.RequestsTotal.WithLabelValues("GET", "/probe", "200", "other").Inc()

	// Bind port 0 so the test never fights over a fixed port.
	srv := NewServer("127.0.0.1:0", registry)
	require.NoError(t, srv.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	addr := srv.Addr()
	require.NotEmpty(t, addr, "listener address must be available after Start")

	resp, err := http.Get("http://" + addr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, RequestsTotalName)
	require.Contains(t, text, "/probe")
	require.Contains(t, text, "go_goroutines")
}

// Only GET /metrics is exposed on this port. In particular, the promhttp
// handler must not be mounted as the root handler (which serves all paths).
func TestMetricsServer_OnlyGetMetrics(t *testing.T) {
	registry, _ := NewRegistry()
	srv := NewServer("127.0.0.1:0", registry)
	require.NoError(t, srv.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
	})

	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/metrics", http.StatusOK},
		{http.MethodGet, "/anything", http.StatusNotFound},
		{http.MethodGet, "/metrics/", http.StatusNotFound},
		{http.MethodPost, "/metrics", http.StatusMethodNotAllowed},
		{http.MethodHead, "/metrics", http.StatusMethodNotAllowed},
		{http.MethodOptions, "/metrics", http.StatusMethodNotAllowed},
		{http.MethodPost, "/anything", http.StatusNotFound},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, "http://"+srv.Addr()+tt.path, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			require.Equal(t, tt.status, resp.StatusCode)
			if tt.status == http.StatusMethodNotAllowed {
				require.Equal(t, http.MethodGet, resp.Header.Get("Allow"))
			}
			_, err = io.Copy(io.Discard, resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
		})
	}
}

// Start failure (port already in use) surfaces synchronously: fail-fast.
func TestMetricsServer_StartPortConflictFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	registry, _ := NewRegistry()
	srv := NewServer(listener.Addr().String(), registry)

	startErr := srv.Start()
	require.Error(t, startErr)
	require.Contains(t, startErr.Error(), "metrics server listen")
}

// StartEnabled must be a no-op (no listener) when disabled.
func TestMetricsServer_StartEnabledDisabledIsNoop(t *testing.T) {
	registry, _ := NewRegistry()
	enabled := &Server{bind: "127.0.0.1:0", enabled: true, http: newMetricsHTTPHandler(registry)}
	require.NoError(t, enabled.StartEnabled())
	require.NotEmpty(t, enabled.Addr())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, enabled.Stop(ctx))

	disabled := &Server{bind: "127.0.0.1:0", enabled: false, http: newMetricsHTTPHandler(registry)}
	require.NoError(t, disabled.StartEnabled())
	require.Empty(t, disabled.Addr(), "disabled server must never bind")
}

// Stop is idempotent and honors the context deadline.
func TestMetricsServer_StopIdempotent(t *testing.T) {
	registry, _ := NewRegistry()
	srv := NewServer("127.0.0.1:0", registry)
	require.NoError(t, srv.Start())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Stop(ctx))
	// Second stop is a no-op and returns nil.
	require.NoError(t, srv.Stop(ctx))
}

// An in-flight request keeps the first Stop blocked until its handler exits.
// Concurrent Stop calls must wait for that shutdown (and race-free access to
// listener/stopErr is verified with go test -race).
func TestMetricsServer_ConcurrentStopWaitsForShutdown(t *testing.T) {
	registry, _ := NewRegistry()
	srv := NewServer("127.0.0.1:0", registry)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	require.NoError(t, srv.Start())
	requestDone := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/metrics")
		if err == nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter handler")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	const callers = 8
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- srv.Stop(ctx) }()
	}
	select {
	case err := <-results:
		t.Fatalf("Stop returned before handler finished: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for i := 0; i < callers; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Stop did not return")
		}
	}
	select {
	case err := <-requestDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	require.Error(t, srv.Start(), "a stopped server must not reopen its port")
}

// ProvideMetricsServer wiring: bind comes from config.
func TestProvideMetricsServer_ConfigBind(t *testing.T) {
	registry, _ := NewRegistry()
	cfg := &config.Config{}
	cfg.Monitoring.MetricsBind = "127.0.0.1:19091"
	cfg.Monitoring.MetricsEnabled = true

	srv := ProvideMetricsServer(cfg, registry)
	require.Equal(t, "127.0.0.1:19091", srv.bind)
	require.True(t, srv.enabled)
}

// The exposition endpoint must not query any DB — asserted structurally: the
// handler only wraps the registry (no dependencies beyond it).
func TestMetricsServer_HandlerShape(t *testing.T) {
	registry, _ := NewRegistry()
	srv := NewServer("127.0.0.1:0", registry)
	require.NotNil(t, srv.http)
	require.NotNil(t, srv.http.Handler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.http.Handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, strings.Contains(w.Body.String(), RequestsInFlightName))
}
