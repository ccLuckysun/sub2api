package middleware

import (
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
)

// OpsMiddleware must be installed between baseline Metrics and Recovery, and
// before authentication/rate limiting. It never wraps a writer or pre-reads a body.
func OpsMiddleware(cfg *config.Config, runtime *monitoring.OpsRuntime) gin.HandlerFunc {
	if cfg == nil || !cfg.Monitoring.OpsMetricsEnabled || runtime == nil || runtime.Metrics == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		path := c.FullPath()
		if !isOpsGatewayRoute(c.Request.Method, path) {
			c.Next()
			return
		}
		var rec *monitoring.OpsRecorder
		var body *monitoring.OpsBody
		safeObserve(func() {
			ctx := monitoring.WithOpsRecorderDependencies(c.Request.Context(), runtime.Accounts, runtime.Coverage, runtime.SampleSink)
			ctx, rec = monitoring.BeginGatewayRequest(ctx, path, runtime.Metrics)
			c.Request = c.Request.WithContext(ctx)
			if c.Request.Body != nil {
				body = monitoring.NewOpsBody(c.Request.Body, c.Request.ContentLength, ctx, rec)
				c.Request.Body = body
			}
		})
		defer func() {
			// Finish the original body before the Finalize publication barrier,
			// including after prereaders replaced c.Request.Body with a replay.
			safeObserve(func() { body.Finish() })
			safeObserve(func() {
				status := "complete"
				if c.GetBool(monitoring.OpsPanicKey) || rec.ClientAborted() {
					status = "partial"
				}
				size := c.Writer.Size()
				if size < 0 {
					size = 0
				} // Gin uses -1 for a response with no body write.
				rec.AddBytes("resp_downstream", int64(size), status)
			})
			safeObserve(func() {
				rec.Finalize(monitoring.FinalizeInput{Status: c.Writer.Status(), Panic: c.GetBool(monitoring.OpsPanicKey)})
			})
		}()
		c.Next()
	}
}

// Keep aligned with routes/gateway.go and MONITORING_V2_TASKS section 3.3.
// Match route templates, never arbitrary raw URLs. Token counting, model lists,
// usage/balance/probes and management routes are deliberately excluded.
func isOpsGatewayRoute(method, path string) bool {
	if path == "" {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		switch segment {
		case "count_tokens", "usage", "balance", "balances", "probe", "health", "admin":
			return false
		}
	}
	if strings.HasSuffix(path, "/models") || strings.HasSuffix(path, "/models/:model") {
		return false
	}
	if strings.Contains(path, "/models/") && method != "POST" {
		return false
	}
	switch path {
	case "/v1/messages", "/v1/chat/completions", "/chat/completions", "/v1/responses", "/v1/embeddings":
		return true
	}
	for _, prefix := range []string{"/v1/responses/", "/v1/images/", "/v1beta/", "/antigravity/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// safeObserve isolates only instrumentation. The handler chain must remain
// outside this function so business panics reach Recovery exactly once.
func safeObserve(observe func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("ops observation failed", "panic", recovered)
		}
	}()
	observe()
}
