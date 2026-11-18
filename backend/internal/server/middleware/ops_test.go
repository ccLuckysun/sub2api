package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type opsTestContextKey struct{}

type opsTestStack struct {
	instruments *monitoring.RegistryAndMetrics
	runtime     *monitoring.OpsRuntime
	cfg         *config.Config
	now         atomic.Int64
}

func newOpsTestStack(t *testing.T, enabled bool) *opsTestStack {
	t.Helper()
	s := &opsTestStack{cfg: &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: enabled}}}
	var err error
	s.instruments, err = monitoring.ProvideConfiguredRegistryAndMetrics(s.cfg)
	require.NoError(t, err)
	s.runtime = monitoring.NewOpsRuntime(s.instruments.Ops)
	if s.runtime != nil {
		s.runtime.Coverage = monitoring.NewOpsCoverage(s.runtime.Metrics.Coverage, func() time.Time { return time.Unix(0, 0).Add(time.Duration(s.now.Load()) * time.Minute) })
	}
	return s
}
func (s *opsTestStack) router(handler gin.HandlerFunc, inspect func(*gin.Context), ops bool) *gin.Engine {
	r := gin.New()
	if inspect != nil {
		r.Use(func(c *gin.Context) { c.Next(); inspect(c) })
	}
	r.Use(gin.HandlerFunc(ProvideMetricsMiddleware(s.cfg, s.instruments.Metrics)))
	if ops {
		r.Use(OpsMiddleware(s.cfg, s.runtime))
	}
	r.Use(Recovery())
	r.POST("/v1/messages", handler)
	return r
}
func totalOpsCounter(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	n := 0.0
	for _, f := range families {
		if f.GetName() == name {
			for _, m := range f.Metric {
				n += m.GetCounter().GetValue()
			}
		}
	}
	return n
}
func brokenPipePanic() error {
	return &net.OpError{Op: "write", Net: "tcp", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}
}

type failingOpsBody struct{ reads, closes int }

func (b *failingOpsBody) Read(p []byte) (int, error) {
	b.reads++
	return copy(p, "abc"), io.ErrUnexpectedEOF
}
func (b *failingOpsBody) Close() error { b.closes++; return nil }

func TestOpsMiddlewareGatewaySettlement(t *testing.T) {
	tests := []struct {
		name           string
		handler        gin.HandlerFunc
		status         int
		result, reason string
		panicMarker    bool
		body           string
		abort          bool
	}{
		{"normal", func(c *gin.Context) { c.String(200, "ok") }, 200, "success", "", false, "ok", false},
		{"auth_rejected", func(c *gin.Context) { c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"}) }, 401, "rejected", "unknown", false, "", false},
		{"rate_limited", func(c *gin.Context) { c.AbortWithStatusJSON(429, gin.H{"error": "rate limited"}) }, 429, "rejected", "gateway_ratelimit", false, "", false},
		{"body_read_failed", func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				monitoring.RecorderFromContext(c.Request.Context()).MarkBodyRead(int64(len(body)), 999, "partial")
				c.AbortWithStatus(400)
			}
		}, 400, "rejected", "unknown", false, "", false},
		{"panic_unwritten", func(c *gin.Context) { panic("business panic") }, 500, "gateway_error", "gateway_panic", true, "", false},
		{"panic_written_SSE", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.String(200, "data: preserved\n\n")
			c.Writer.Flush()
			panic("stream panic")
		}, 200, "gateway_error", "gateway_panic", true, "data: preserved\n\n", false},
		{"broken_pipe", func(c *gin.Context) { c.String(200, "written"); panic(brokenPipePanic()) }, 200, "client_abort", "client_abort_during_response", false, "written", true},
		{"wrapped_broken_pipe", func(c *gin.Context) { c.String(200, "written"); panic(fmt.Errorf("wrapped: %w", brokenPipePanic())) }, 200, "client_abort", "client_abort_during_response", false, "written", true},
		{"duplicate_finalize", func(c *gin.Context) {
			monitoring.RecorderFromContext(c.Request.Context()).Finalize(monitoring.FinalizeInput{Status: 200})
			c.String(200, "ok")
		}, 200, "success", "", false, "ok", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newOpsTestStack(t, true)
			s.runtime.Coverage.Entry() // Sentinel incomplete request proves the actual denominator.
			calls := 0
			var rec *monitoring.OpsRecorder
			var marked bool
			var observedRequest *http.Request
			route := s.router(func(c *gin.Context) {
				calls++
				rec = monitoring.RecorderFromContext(c.Request.Context())
				observedRequest = c.Request
				tc.handler(c)
			}, func(c *gin.Context) { marked = c.GetBool(monitoring.OpsPanicKey) }, true)
			request := httptest.NewRequest("POST", "/v1/messages", nil)
			failedBody := &failingOpsBody{}
			request.Body = failedBody
			w := httptest.NewRecorder()
			route.ServeHTTP(w, request)
			require.Equal(t, 1, calls)
			require.NotNil(t, rec)
			require.NotSame(t, request, observedRequest, "Recorder context is written back to c.Request")
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.panicMarker, marked)
			if tc.body != "" {
				require.Equal(t, tc.body, w.Body.String())
			}
			m := s.instruments.Ops
			require.Equal(t, 1.0, totalOpsCounter(t, s.instruments.Registry, "sub2api_gateway_requests_total"))
			require.Equal(t, 1.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues(tc.result, tc.reason)))
			require.Equal(t, 1.0, requestCount(t, s.instruments.Metrics, "POST", "/v1/messages", fmt.Sprint(tc.status), responseMode(w.Header().Get("Content-Type"))))
			require.Equal(t, 0.0, totalOpsCounter(t, s.instruments.Registry, "sub2api_upstream_requests_total"))
			if tc.abort {
				require.Equal(t, 1.0, testutil.ToFloat64(m.ClientAbort.WithLabelValues("response")))
			} else {
				require.Equal(t, 0.0, totalOpsCounter(t, s.instruments.Registry, "sub2api_client_abort_total"))
			}
			if tc.name == "body_read_failed" {
				require.Equal(t, 1, failedBody.reads)
			} else {
				require.Zero(t, failedBody.reads, "entry middleware never reads the request body")
			}
			require.Zero(t, failedBody.closes, "entry middleware never closes the request body")
			s.now.Store(1)
			s.runtime.Coverage.Flush()
			require.Equal(t, .5, testutil.ToFloat64(m.Coverage.WithLabelValues("request")), "one completed gateway request and one unfinished entry")
		})
	}
}

func TestOpsMiddlewareExcludedRoutesAndFullPath(t *testing.T) {
	for _, path := range []string{"/api/admin/accounts", "/v1/messages/count_tokens", "/v1/models", "/antigravity/v1/models", "/antigravity/v1/usage", "/v1beta/models/:model"} {
		t.Run(path, func(t *testing.T) {
			s := newOpsTestStack(t, true)
			s.runtime.Coverage.Entry()
			r := gin.New()
			r.Use(OpsMiddleware(s.cfg, s.runtime))
			r.Use(Recovery())
			var rec *monitoring.OpsRecorder
			r.POST(path, func(c *gin.Context) {
				rec = monitoring.RecorderFromContext(c.Request.Context())
				c.String(200, "bypassed")
			})
			actual := strings.ReplaceAll(path, ":model", "test")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", actual, nil))
			require.Equal(t, 200, w.Code)
			require.Nil(t, rec)
			require.Zero(t, totalOpsCounter(t, s.instruments.Registry, "sub2api_gateway_requests_total"))
			s.now.Store(1)
			s.runtime.Coverage.Flush()
			require.Equal(t, 0.0, testutil.ToFloat64(s.instruments.Ops.Coverage.WithLabelValues("request")))
		})
	}
	s := newOpsTestStack(t, true)
	r := gin.New()
	r.Use(OpsMiddleware(s.cfg, s.runtime))
	r.Use(Recovery())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
	require.Equal(t, 404, w.Code)
	require.Zero(t, totalOpsCounter(t, s.instruments.Registry, "sub2api_gateway_requests_total"), "raw gateway-looking URL without matched FullPath is bypassed")
}

func TestOpsMiddlewareResponsesWebSocketCountsOnlyOnCompletion(t *testing.T) {
	stack := newOpsTestStack(t, true)
	router := gin.New()
	router.Use(gin.HandlerFunc(ProvideMetricsMiddleware(stack.cfg, stack.instruments.Metrics)))
	router.Use(OpsMiddleware(stack.cfg, stack.runtime), Recovery())
	router.GET("/backend-api/codex/responses", func(c *gin.Context) {
		conn, err := websocket.Accept(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(c.Request.Context())
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/backend-api/codex/responses", nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	require.Zero(t, testutil.ToFloat64(stack.instruments.Metrics.OpsEligibleRequestsTotal))
	require.Zero(t, totalOpsCounter(t, stack.instruments.Registry, "sub2api_gateway_requests_total"))
	require.NoError(t, conn.CloseNow())
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(stack.instruments.Metrics.OpsEligibleRequestsTotal) == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, 1.0, totalOpsCounter(t, stack.instruments.Registry, "sub2api_gateway_requests_total"))
}

func TestOpsMiddlewareDisabledBehaviorIdentical(t *testing.T) {
	for _, scenario := range []string{"normal", "panic_before_write", "panic_after_write", "broken_pipe"} {
		t.Run(scenario, func(t *testing.T) {
			var baseline *httptest.ResponseRecorder
			for _, installOps := range []bool{false, true} {
				s := newOpsTestStack(t, false)
				require.Nil(t, s.runtime)
				require.Nil(t, s.instruments.Ops)
				request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader("unchanged body")).WithContext(context.WithValue(context.Background(), opsTestContextKey{}, "preserved"))
				originalContext := request.Context()
				var gotRequest *http.Request
				calls := 0
				r := s.router(func(c *gin.Context) {
					calls++
					gotRequest = c.Request
					require.Nil(t, monitoring.RecorderFromContext(c.Request.Context()))
					require.Same(t, originalContext, c.Request.Context())
					switch scenario {
					case "normal":
						body, err := io.ReadAll(c.Request.Body)
						require.NoError(t, err)
						c.String(201, string(body))
					case "panic_before_write":
						panic("same failure")
					case "panic_after_write":
						c.String(200, "preserved")
						panic("same failure")
					case "broken_pipe":
						c.String(200, "preserved")
						panic(brokenPipePanic())
					}
				}, nil, installOps)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, request)
				require.Equal(t, 1, calls)
				require.Same(t, request, gotRequest, "disabled Ops does not replace request/context")
				if baseline == nil {
					baseline = w
				} else {
					require.Equal(t, baseline.Code, w.Code)
					require.Equal(t, baseline.Body.String(), w.Body.String())
					require.Equal(t, baseline.Header(), w.Header())
				}
				families, err := s.instruments.Registry.Gather()
				require.NoError(t, err)
				for _, f := range families {
					require.NotContains(t, []string{"sub2api_request_phase_seconds", "sub2api_gateway_requests_total", "sub2api_metric_coverage_ratio", "sub2api_account_switches_total"}, f.GetName())
				}
			}
		})
	}
	// Disabled operation does not introduce instrumentation context values.
	require.Nil(t, monitoring.RecorderFromContext(context.Background()))
}

func TestOpsMiddlewareInstrumentationPanicIsolation(t *testing.T) {
	require.NotPanics(t, func() { safeObserve(func() { panic("injected observation failure") }) })
	s := newOpsTestStack(t, true)
	var fail atomic.Bool
	s.runtime.Coverage = monitoring.NewOpsCoverage(s.instruments.Ops.Coverage, func() time.Time {
		if fail.Load() {
			panic("injected Begin failure")
		}
		return time.Now()
	})
	fail.Store(true)
	calls := 0
	r := s.router(func(c *gin.Context) { calls++; c.String(202, "business response") }, nil, true)
	w := httptest.NewRecorder()
	require.NotPanics(t, func() { r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil)) })
	require.Equal(t, 1, calls)
	require.Equal(t, 202, w.Code)
	require.Equal(t, "business response", w.Body.String())
	// A Finalize collector panic also cannot replace an already written response.
	s = newOpsTestStack(t, true)
	s.instruments.Ops.GatewayRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "injected_bad_collector", Help: "test"}, []string{"one_label"})
	calls = 0
	r = s.router(func(c *gin.Context) { calls++; c.String(202, "business response") }, nil, true)
	w = httptest.NewRecorder()
	require.NotPanics(t, func() { r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil)) })
	require.Equal(t, 1, calls)
	require.Equal(t, 202, w.Code)
	require.Equal(t, "business response", w.Body.String())
}

var _ io.ReadCloser = (*failingOpsBody)(nil)

func opsBodyHistogram(t *testing.T, m *monitoring.OpsMetrics) (uint64, float64) {
	t.Helper()
	metric := &dto.Metric{}
	require.NoError(t, m.RequestBody.WithLabelValues("responses").(prometheus.Metric).Write(metric))
	return metric.Histogram.GetSampleCount(), metric.Histogram.GetSampleSum()
}

func TestOpsMiddlewareBodyAndDownstreamBytes(t *testing.T) {
	plain := []byte(`{"model":"claude","messages":[],"text":"原始字节"}`)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(plain)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	for _, tc := range []struct {
		name                                                         string
		raw                                                          []byte
		encoding                                                     string
		partial, unread, preread, chunked, panicFlag, noWrite, abort bool
	}{
		{name: "normal", raw: plain}, {name: "empty_body_read", raw: []byte{}}, {name: "unfinished_partial", raw: plain, partial: true},
		{name: "unread_auth_rejected", raw: plain, unread: true}, {name: "allowlist_composite_preread_then_handler_replay", raw: plain, preread: true},
		{name: "gzip_wire_not_decoded_or_declared", raw: compressed.Bytes(), encoding: "gzip"}, {name: "chunked_payload_actual_n", raw: plain, chunked: true},
		{name: "SSE_written_panic", raw: plain, panicFlag: true}, {name: "panic_no_write", raw: plain, panicFlag: true, noWrite: true},
		{name: "client_response_abort", raw: plain, abort: true}, {name: "zero_downstream_body", raw: plain, noWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOpsTestStack(t, true)
			s.runtime.Coverage.Entry()
			r := gin.New()
			var originalWriter gin.ResponseWriter
			r.Use(func(c *gin.Context) { originalWriter = c.Writer; c.Next() })
			r.Use(OpsMiddleware(s.cfg, s.runtime))
			r.Use(Recovery())
			calls := 0
			var consumed []byte
			r.POST("/v1/messages", func(c *gin.Context) {
				rec := monitoring.RecorderFromContext(c.Request.Context())
				require.NotNil(t, rec)
				require.Same(t, originalWriter, c.Writer, "Ops never replaces Writer")
				if tc.unread {
					c.AbortWithStatus(401)
					return
				}
				if tc.preread {
					models, ok := groupModelAllowlistModelsFromBody(c)
					require.True(t, ok)
					require.Contains(t, models, "claude")
					// composite route performs another helper read/reset on the preread body.
					body, readErr := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
					require.NoError(t, readErr)
					requestmodel.ResetRequestBody(c.Request, body)
				}
				c.Next()
			}, func(c *gin.Context) {
				calls++
				rec := monitoring.RecorderFromContext(c.Request.Context())
				var readErr error
				if tc.partial {
					buf := make([]byte, 4)
					n, err := c.Request.Body.Read(buf)
					readErr = err
					consumed = buf[:n]
				} else if tc.encoding != "" {
					consumed, readErr = pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
				} else {
					consumed, readErr = io.ReadAll(c.Request.Body)
				}
				require.NoError(t, readErr)
				// Known-empty upstream directions isolate downstream completeness in M-9;
				// the fixture creates no attempt and sends no upstream request.
				rec.AddBytes("req_upstream", 0, "complete")
				rec.AddBytes("resp_upstream", 0, "complete")
				if tc.abort {
					rec.MarkClientAbort("response")
				}
				if !tc.noWrite {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.Write([]byte("data: 中文\n\n"))
					require.NoError(t, err)
					c.Writer.Flush()
					_, err = c.Writer.Write([]byte("data: done\n\n"))
					require.NoError(t, err)
					c.Writer.Flush()
				}
				if tc.panicFlag {
					panic("body byte accounting panic")
				}
				if tc.noWrite {
					c.Status(204)
				}
			})
			req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(tc.raw))
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			if tc.chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			wantWire := len(tc.raw)
			if tc.partial {
				wantWire = 4
			}
			if tc.unread {
				wantWire = 0
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			require.Equal(t, float64(wantWire), testutil.ToFloat64(s.instruments.Ops.TransferBytes.WithLabelValues("in_wire")))
			require.Equal(t, float64(w.Body.Len()), testutil.ToFloat64(s.instruments.Ops.TransferBytes.WithLabelValues("resp_downstream")), "counts successful body bytes only")
			for _, dir := range []string{"req_decoded", "req_upstream", "resp_upstream"} {
				require.Zero(t, testutil.ToFloat64(s.instruments.Ops.TransferBytes.WithLabelValues(dir)), dir)
			}
			count, sum := opsBodyHistogram(t, s.instruments.Ops)
			if tc.unread {
				require.Zero(t, count)
			} else {
				require.EqualValues(t, 1, count)
				require.Equal(t, float64(wantWire), sum)
			}
			if tc.preread || tc.encoding != "" {
				require.Equal(t, plain, consumed, "pre-read/decompression behavior is unchanged")
			}
			if !tc.unread && !tc.noWrite {
				require.Equal(t, "data: 中文\n\ndata: done\n\n", w.Body.String())
			}
			result, reason := "success", ""
			if tc.unread {
				result, reason = "rejected", "unknown"
			}
			if tc.partial {
				result, reason = "gateway_error", "unknown"
			}
			if tc.abort {
				result, reason = "client_abort", "client_abort_during_response"
			}
			if tc.panicFlag {
				result, reason = "gateway_error", "gateway_panic"
			}
			require.Equal(t, 1.0, totalOpsCounter(t, s.instruments.Registry, "sub2api_gateway_requests_total"))
			require.Equal(t, 1.0, testutil.ToFloat64(s.instruments.Ops.GatewayRequests.WithLabelValues(result, reason)))
			require.Zero(t, totalOpsCounter(t, s.instruments.Registry, "sub2api_upstream_requests_total"))
			require.Zero(t, totalOpsCounter(t, s.instruments.Registry, "sub2api_model_requests_total"))
			s.now.Store(1)
			s.runtime.Coverage.Flush()
			wantBytes := .5
			if tc.unread || tc.partial || tc.abort || tc.panicFlag {
				wantBytes = 0
			}
			require.Equal(t, wantBytes, testutil.ToFloat64(s.instruments.Ops.Coverage.WithLabelValues("bytes")), "partial downstream never becomes complete")
		})
	}
}

func TestOpsMiddlewareBodyReadFailureObservation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			s := newOpsTestStack(t, true)
			var bodyErr error = io.ErrUnexpectedEOF
			if canceled {
				bodyErr = context.Canceled
			}
			raw := &opsFailingReadBody{err: bodyErr}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			recSeen := false
			r := s.router(func(c *gin.Context) {
				_, err := io.ReadAll(c.Request.Body)
				require.Same(t, bodyErr, err)
				recSeen = monitoring.RecorderFromContext(c.Request.Context()).ClientAborted()
				c.AbortWithStatus(400)
			}, nil, true)
			req := httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			req.Body = raw
			req.ContentLength = 999
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, canceled, recSeen)
			require.Equal(t, 1, raw.reads)
			require.Zero(t, raw.closes)
			count, sum := opsBodyHistogram(t, s.instruments.Ops)
			require.EqualValues(t, 1, count)
			require.Equal(t, 3.0, sum)
			require.Equal(t, 3.0, testutil.ToFloat64(s.instruments.Ops.TransferBytes.WithLabelValues("in_wire")))
			result, reason := "rejected", "unknown"
			if canceled {
				result, reason = "client_abort", "client_abort_during_body_read"
			}
			require.Equal(t, 1.0, testutil.ToFloat64(s.instruments.Ops.GatewayRequests.WithLabelValues(result, reason)))
		})
	}
}

type opsFailingReadBody struct {
	err           error
	reads, closes int
}

func (b *opsFailingReadBody) Read(p []byte) (int, error) { b.reads++; return copy(p, "abc"), b.err }
func (b *opsFailingReadBody) Close() error               { b.closes++; return nil }

func TestOpsMiddlewareUsesRuntimeAccountSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, account, want string
		snapshot            *monitoring.AccountLabels
		coverage            float64
	}{
		{"known", "Primary.A", "primarya", monitoring.NewAccountLabels([]string{"Primary.A", "primary a"}), 1},
		{"collision", "primary a", "unknown", monitoring.NewAccountLabels([]string{"Primary.A", "primary a"}), 0},
		{"new account", "new", "unknown", monitoring.NewAccountLabels([]string{"Primary.A"}), 0},
		{"nil snapshot", "Primary.A", "unknown", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOpsTestStack(t, true)
			s.runtime.Accounts = tc.snapshot
			router := s.router(func(c *gin.Context) {
				rec := monitoring.RecorderFromContext(c.Request.Context())
				attempt := rec.StartAttempt(tc.account)
				attempt.Sent()
				attempt.NoteHeaders(200)
				attempt.UseResponse()
				attempt.NoteFirstByte()
				attempt.BodyDone(nil)
				attempt.Done("success", "", monitoring.FailureEvidence{})
				c.String(200, "ok")
			}, nil, true)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
			require.Equal(t, 200, w.Code)
			require.Equal(t, 1.0, testutil.ToFloat64(s.runtime.Metrics.UpstreamRequests.WithLabelValues(tc.want, "success", "")))
			families, err := s.instruments.Registry.Gather()
			require.NoError(t, err)
			found := false
			for _, family := range families {
				if family.GetName() != "sub2api_upstream_ttft_seconds" {
					continue
				}
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						if label.GetName() == "account" && label.GetValue() == tc.want {
							require.EqualValues(t, 1, metric.GetHistogram().GetSampleCount())
							found = true
						}
					}
				}
			}
			require.True(t, found, "M-11 must use the same snapshot account label")
			// Advance the test minute and flush the completed request's coverage.
			s.now.Store(1)
			s.runtime.Coverage.Flush()
			require.Equal(t, tc.coverage, testutil.ToFloat64(s.runtime.Metrics.Coverage.WithLabelValues("account")))
		})
	}
}

type middlewareSampleSink struct{ samples chan monitoring.OpsSample }

func (s middlewareSampleSink) Submit(sample monitoring.OpsSample) bool {
	select {
	case s.samples <- sample:
		return true
	default:
		return false
	}
}
func TestOpsMiddlewarePassesRuntimeSampleSink(t *testing.T) {
	s := newOpsTestStack(t, true)
	samples := make(chan monitoring.OpsSample, 1)
	s.runtime.SampleSink = middlewareSampleSink{samples: samples}
	router := s.router(func(c *gin.Context) { c.AbortWithStatus(401) }, nil, true)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
	require.Equal(t, 401, w.Code)
	select {
	case sample := <-samples:
		require.Equal(t, "rejected", *sample.Result)
		require.Zero(t, *sample.AttemptCount)
		require.NotEmpty(t, sample.RequestID)
	default:
		t.Fatal("runtime sink was not propagated")
	}
}
