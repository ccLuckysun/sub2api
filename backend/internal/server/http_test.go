package server

import (
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestProductionObservabilityOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name      string
		reject    int
		written   bool
		panicFlag bool
	}{
		{name: "auth_reject", reject: 401}, {name: "rate_limit_reject", reject: 429}, {name: "panic_before_write", panicFlag: true}, {name: "panic_after_write", panicFlag: true, written: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
			instruments, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
			require.NoError(t, err)
			runtime := monitoring.NewOpsRuntime(instruments.Ops)
			var minute atomic.Int64
			runtime.Coverage = monitoring.NewOpsCoverage(instruments.Ops.Coverage, func() time.Time { return time.Unix(0, 0).Add(time.Duration(minute.Load()) * time.Minute) })
			runtime.Coverage.Entry() // Incomplete sentinel to prove exact entry denominator.
			r := gin.New()
			installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, instruments.Metrics), runtime)
			authSawRecorder := false
			handlerCalls := 0
			r.POST("/v1/messages", func(c *gin.Context) {
				authSawRecorder = monitoring.RecorderFromContext(c.Request.Context()) != nil
				if scenario.reject != 0 {
					c.AbortWithStatus(scenario.reject)
					return
				}
				c.Next()
			}, func(c *gin.Context) {
				handlerCalls++
				if scenario.written {
					c.String(200, "SSE already written")
					c.Writer.Flush()
				}
				if scenario.panicFlag {
					panic("production chain panic")
				}
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
			require.True(t, authSawRecorder, "entry must run before authentication or rate limiting")
			status := scenario.reject
			result, reason := "rejected", "unknown"
			switch scenario.reject {
			case 401:
				reason = "client_auth_failed"
			case 429:
				reason = "gateway_ratelimit"
			}
			if scenario.panicFlag {
				result, reason = "gateway_error", "gateway_panic"
				status = 500
				if scenario.written {
					status = 200
					require.Equal(t, "SSE already written", w.Body.String())
				}
				require.Equal(t, 1, handlerCalls)
			} else {
				require.Zero(t, handlerCalls, "auth rejection must not reach handler")
			}
			require.Equal(t, status, w.Code)
			require.Equal(t, 1.0, testutil.ToFloat64(instruments.Ops.GatewayRequests.WithLabelValues(result, reason)), "Ops must finalize after Recovery")
			require.Equal(t, 1.0, testutil.ToFloat64(instruments.Metrics.RequestsTotal.WithLabelValues("POST", "/v1/messages", fmt.Sprint(status), "other")), "baseline metrics must see Recovery's final status")
			minute.Store(1)
			runtime.Coverage.Flush()
			require.Equal(t, .5, testutil.ToFloat64(instruments.Ops.Coverage.WithLabelValues("request")))
		})
	}
}
