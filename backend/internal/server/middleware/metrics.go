package middleware

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
)

// MetricsMiddleware instruments the Gin engine with Prometheus counters
// (see docs/MONITORING_API.md section 2). It must be registered before
// Recovery so that its defer observes the final status code written by the
// inner Recovery middleware on panic.
type MetricsMiddleware gin.HandlerFunc

// NewMetricsMiddleware builds the HTTP metrics middleware from the handles
// provided by the monitoring package. It never wraps or buffers the
// ResponseWriter: status code and Content-Type are read from gin's writer
// after the handler chain finished, so Flush/Hijack/Unwrap behavior stays
// intact (SSE is unaffected).
func NewMetricsMiddleware(metrics *monitoring.Metrics) MetricsMiddleware {
	return MetricsMiddleware(newMetricsHandler(metrics))
}

// NewNoopMetricsMiddleware is used when monitoring.metrics_enabled=false:
// it registers nothing and records nothing, so the middleware chain is
// behaviorally identical to the pre-instrumentation status quo.
func NewNoopMetricsMiddleware() MetricsMiddleware {
	return MetricsMiddleware(func(c *gin.Context) {
		c.Next()
	})
}

// ProvideMetricsMiddleware selects the live middleware when
// monitoring.metrics_enabled is on, and the no-op otherwise. The live
// handles come from the same registry the /metrics server exposes.
func ProvideMetricsMiddleware(cfg *config.Config, metrics *monitoring.Metrics) MetricsMiddleware {
	if cfg == nil || !cfg.Monitoring.MetricsEnabled {
		return NewNoopMetricsMiddleware()
	}
	return NewMetricsMiddleware(metrics)
}

func newMetricsHandler(metrics *monitoring.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Exclusion whitelist (finite list, docs/MONITORING_API.md 1.3).
		if isMetricsExcluded(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}

		metrics.RequestsInFlight.Inc()
		defer metrics.RequestsInFlight.Dec()

		opsEligible := isOpsGatewayRoute(c)
		start := time.Now()
		defer func() {
			if opsEligible {
				metrics.OpsEligibleRequestsTotal.Inc()
			}
			route := c.FullPath()
			if route == "" {
				route = "unmatched"
			}

			method := monitoring.NormalizeMethod(c.Request.Method)
			mode := responseMode(c.Writer.Header().Get("Content-Type"))
			status := strconv.Itoa(c.Writer.Status())

			metrics.RequestsTotal.
				WithLabelValues(method, route, status, mode).
				Inc()
			metrics.RequestDuration.
				WithLabelValues(method, route, mode).
				Observe(time.Since(start).Seconds())
		}()

		c.Next()
	}
}

// isMetricsExcluded reports whether the request matches the exclusion
// whitelist. /health and /setup/status are excluded for every method;
// /api/event_logging/batch is excluded for POST only.
func isMetricsExcluded(method, path string) bool {
	switch path {
	case "/health", "/setup/status":
		return true
	case "/api/event_logging/batch":
		return method == http.MethodPost
	default:
		return false
	}
}

// responseMode classifies a finished response by the media type of its
// final Content-Type header: text/event-stream means SSE, anything else
// (including no Content-Type) is "other" (docs/MONITORING_API.md 2.2).
func responseMode(contentType string) string {
	mediaType := contentType
	// Skip past parameters such as charset to compare the media type only.
	if idx := strings.IndexRune(mediaType, ';'); idx >= 0 {
		mediaType = mediaType[:idx]
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(mediaType))
	if err != nil {
		return monitoring.ResponseModeOther
	}
	if strings.EqualFold(mediaType, "text/event-stream") {
		return monitoring.ResponseModeSSE
	}
	return monitoring.ResponseModeOther
}
