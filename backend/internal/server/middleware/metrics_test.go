package middleware

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func init() {
	// metrics_test 与同包其他 *_test.go 共享 gin 测试模式（init 幂等）。
	gin.SetMode(gin.TestMode)
}

// newTestMetrics builds a fresh registry + handles so each test has isolated
// metric state (never the global registry).
func newTestMetrics(t *testing.T) (*prometheus.Registry, *monitoring.Metrics) {
	t.Helper()
	return monitoring.NewRegistry()
}

// newMetricsTestRouter wires the metrics middleware OUTSIDE Recovery, exactly
// as the production http.go does: r.Use(metrics) then r.Use(Recovery()).
func newMetricsTestRouter(metrics *monitoring.Metrics) *gin.Engine {
	r := gin.New()
	r.Use(gin.HandlerFunc(NewMetricsMiddleware(metrics)))
	r.Use(Recovery())
	r.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.POST("/items/:id", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"id": c.Param("id")})
	})
	r.GET("/sse", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.String(http.StatusOK, "data: hello\n\n")
	})
	r.GET("/json", func(c *gin.Context) {
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.String(http.StatusOK, `{"ok":true}`)
	})
	r.GET("/panic", func(c *gin.Context) {
		panic("metrics middleware test panic")
	})
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"needs_setup": false})
	})
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.GET("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

func serve(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// requestCount reads the counter for one exact label combination. Panics on
// missing series so tests fail loudly on absent instrumentation.
func requestCount(t *testing.T, metrics *monitoring.Metrics, method, route, status, mode string) float64 {
	t.Helper()
	return promtestutil.ToFloat64(metrics.RequestsTotal.WithLabelValues(method, route, status, mode))
}

// observationCount reads the _count of the duration histogram series.
func observationCount(t *testing.T, metrics *monitoring.Metrics, method, route, mode string) float64 {
	t.Helper()
	obs := metrics.RequestDuration.WithLabelValues(method, route, mode)
	metric, ok := obs.(prometheus.Metric)
	require.True(t, ok, "histogram series must implement prometheus.Metric")
	var m dto.Metric
	require.NoError(t, metric.Write(&m))
	return float64(m.GetHistogram().GetSampleCount())
}

// 1. Known request count -> requests_total delta exactly matches.
func TestMetricsMiddleware_RequestsTotalExactCount(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	const n = 5
	for i := 0; i < n; i++ {
		w := serve(r, http.MethodGet, "/ok")
		require.Equal(t, http.StatusOK, w.Code)
	}

	require.Equal(t, float64(n), requestCount(t, metrics, "GET", "/ok", "200", "other"))

	// A different path/method/status lands in a different series.
	w := serve(r, http.MethodPost, "/items/42")
	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, float64(1), requestCount(t, metrics, "POST", "/items/:id", "201", "other"))
	// The first series is untouched.
	require.Equal(t, float64(n), requestCount(t, metrics, "GET", "/ok", "200", "other"))
}

// 2. Panic path: Recovery (inner) writes 500 first, the outer metrics defer
// must record 500 — not 200.
func TestMetricsMiddleware_PanicRecordsFinalStatus(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/panic")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/panic", "500", "other"))
}

// 3a. Excluded paths produce no metric series at all.
func TestMetricsMiddleware_ExcludedPaths(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/health")
	require.Equal(t, http.StatusOK, w.Code)
	w = serve(r, http.MethodGet, "/setup/status")
	require.Equal(t, http.StatusOK, w.Code)
	w = serve(r, http.MethodPost, "/api/event_logging/batch")
	require.Equal(t, http.StatusOK, w.Code)

	// No series were created for any excluded request.
	require.Equal(t, 0, promtestutil.CollectAndCount(metrics.RequestsTotal, monitoring.RequestsTotalName))
	require.Equal(t, 0, promtestutil.CollectAndCount(metrics.RequestDuration, monitoring.RequestDurationName))
}

// 3b. /api/event_logging/batch is excluded only for POST: GET is measured.
func TestMetricsMiddleware_ExclusionIsMethodScoped(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/api/event_logging/batch")
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/api/event_logging/batch", "200", "other"))
}

// 3c. Unmatched route collapses to the bounded "unmatched" label.
func TestMetricsMiddleware_UnmatchedRoute(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/definitely/not/registered")
	require.Equal(t, http.StatusNotFound, w.Code)

	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "unmatched", "404", "other"))
}

// 3d. Unknown methods collapse to OTHER (bounded method label set).
func TestMetricsMiddleware_UnknownMethodOther(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)
	// Gin only routes registered method+path pairs; an unregistered method
	// on a registered path still reaches the middleware chain with no
	// FullPath (route=unmatched, status 404).
	r.Handle("PROPFIND", "/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := serve(r, "PROPFIND", "/ok")
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, float64(1), requestCount(t, metrics, monitoring.MethodOther, "/ok", "200", "other"))
}

// 4. Response mode: text/event-stream -> sse; application/json -> other.
func TestMetricsMiddleware_ResponseMode(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/sse")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/sse", "200", monitoring.ResponseModeSSE))
	// Duration histogram carries the sse label too.
	require.Equal(t, float64(1), observationCount(t, metrics, "GET", "/sse", monitoring.ResponseModeSSE))

	w = serve(r, http.MethodGet, "/json")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/json", "200", monitoring.ResponseModeOther))
}

// 5a. In-flight: zero after normal completion.
func TestMetricsMiddleware_InFlightReturnsToZero(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	serve(r, http.MethodGet, "/ok")
	require.Equal(t, float64(0), promtestutil.ToFloat64(metrics.RequestsInFlight))
}

// 5b. In-flight: zero after a handler panic (Recovery inside).
func TestMetricsMiddleware_InFlightAfterPanicZero(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := newMetricsTestRouter(metrics)

	w := serve(r, http.MethodGet, "/panic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, float64(0), promtestutil.ToFloat64(metrics.RequestsInFlight))
}

// 5c. In-flight: increments while the handler runs, and requests_total stays
// untouched until the request finishes.
func TestMetricsMiddleware_InFlightDuringRequest(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewMetricsMiddleware(metrics)))
	r.Use(Recovery())

	var observedInFlight float64
	r.GET("/slow", func(c *gin.Context) {
		observedInFlight = promtestutil.ToFloat64(metrics.RequestsInFlight)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	serve(r, http.MethodGet, "/slow")

	require.Equal(t, float64(1), observedInFlight, "handler should observe in_flight=1")
	require.Equal(t, float64(0), promtestutil.ToFloat64(metrics.RequestsInFlight))
}

// Cancellation does not end a server-side request by itself: the handler
// must observe the cancellation and return before the outer defer decrements
// in_flight. Use a real client/server to exercise request context propagation.
func TestMetricsMiddleware_InFlightAfterClientCancellation(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewMetricsMiddleware(metrics)))
	r.Use(Recovery())
	entered := make(chan struct{})
	handlerExited := make(chan struct{})
	r.GET("/cancel", func(c *gin.Context) {
		close(entered)
		<-c.Request.Context().Done()
		close(handlerExited)
	})
	ts := httptest.NewServer(r)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/cancel", nil)
	require.NoError(t, err)
	clientDone := make(chan error, 1)
	go func() {
		resp, err := ts.Client().Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	require.Equal(t, float64(1), promtestutil.ToFloat64(metrics.RequestsInFlight))
	require.Equal(t, 0, promtestutil.CollectAndCount(metrics.RequestsTotal, monitoring.RequestsTotalName),
		"an in-flight request must not be counted as completed")
	cancel()
	select {
	case <-handlerExited:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not observe cancellation")
	}
	select {
	case err := <-clientDone:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("client request did not finish")
	}
	require.Eventually(t, func() bool {
		return promtestutil.ToFloat64(metrics.RequestsInFlight) == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/cancel", "200", "other"))
}

// Flush must deliver the first SSE frame while the handler is still blocked;
// the metrics middleware must not buffer or wrap Gin's ResponseWriter.
func TestMetricsMiddleware_SSEFlushIsTimely(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewMetricsMiddleware(metrics)))
	r.Use(Recovery())
	release := make(chan struct{})
	var releaseOnce sync.Once
	r.GET("/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = c.Writer.WriteString("data: first\n\n")
		c.Writer.Flush()
		<-release
		_, _ = c.Writer.WriteString("data: last\n\n")
		c.Writer.Flush()
	})
	ts := httptest.NewServer(r)
	defer ts.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := ts.Client()
	client.Timeout = 3 * time.Second
	resp, err := client.Get(ts.URL + "/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "text/event-stream; charset=utf-8", resp.Header.Get("Content-Type"))
	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	require.NoError(t, err, "first frame should arrive before handler finishes")
	require.Equal(t, "data: first\n", first)
	require.Equal(t, float64(1), promtestutil.ToFloat64(metrics.RequestsInFlight))
	require.Equal(t, 0, promtestutil.CollectAndCount(metrics.RequestsTotal, monitoring.RequestsTotalName))
	releaseOnce.Do(func() { close(release) })
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return promtestutil.ToFloat64(metrics.RequestsInFlight) == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, float64(1), requestCount(t, metrics, "GET", "/stream", "200", monitoring.ResponseModeSSE))
}

// 6. Noop middleware records nothing (metrics_enabled=false path).
func TestMetricsMiddleware_NoopRecordsNothing(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewNoopMetricsMiddleware()))
	r.Use(Recovery())
	r.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	serve(r, http.MethodGet, "/ok")

	require.Equal(t, 0, promtestutil.CollectAndCount(metrics.RequestsTotal, monitoring.RequestsTotalName))
}

// 7. Duration histogram: observations land in the expected buckets.
func TestMetricsMiddleware_DurationBuckets(t *testing.T) {
	_, metrics := newTestMetrics(t)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewMetricsMiddleware(metrics)))
	r.Use(Recovery())
	r.GET("/fast", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	serve(r, http.MethodGet, "/fast")

	require.Equal(t, float64(1), observationCount(t, metrics, "GET", "/fast", monitoring.ResponseModeOther),
		"exactly one observation expected")

	// Every cumulative bucket boundary must be present, including the
	// explicitly appended long-tail buckets (30/60/120/300/600), and the
	// +Inf bucket must hold the full observation count.
	requireBucketBounds(t, metrics, "GET", "/fast", monitoring.ResponseModeOther)
}

// requireBucketBounds extracts the le boundaries and per-bucket counts from
// the duration histogram series and asserts the full explicit bucket list
// plus +Inf.
func requireBucketBounds(t *testing.T, metrics *monitoring.Metrics, method, route, mode string) {
	t.Helper()

	obs := metrics.RequestDuration.WithLabelValues(method, route, mode)
	metric, ok := obs.(prometheus.Metric)
	require.True(t, ok, "histogram series must implement prometheus.Metric")
	var m dto.Metric
	require.NoError(t, metric.Write(&m))

	h := m.GetHistogram()
	require.NotNil(t, h)
	require.Equal(t, uint64(1), h.GetSampleCount())

	// The DTO carries the 16 finite buckets; +Inf exists only in the text
	// exposition format, so sample_count is the +Inf count here.
	expected := monitoring.RequestDurationBuckets()
	require.Len(t, h.Bucket, len(expected))

	for i, b := range h.Bucket {
		require.InDelta(t, expected[i], b.GetUpperBound(), 1e-9, "bucket %d", i)
		// Cumulative counts never decrease.
		if i > 0 {
			require.GreaterOrEqual(t, b.GetCumulativeCount(), h.Bucket[i-1].GetCumulativeCount())
		}
	}
	// Last finite bucket (le=600) already contains the single observation.
	require.Equal(t, uint64(1), h.Bucket[len(h.Bucket)-1].GetCumulativeCount())
}
