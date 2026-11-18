package middleware

import (
	"log/slog"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpsMiddleware must be installed between baseline Metrics and Recovery, and
// before authentication/rate limiting. It never wraps a writer or pre-reads a body.
func OpsMiddleware(cfg *config.Config, runtime *monitoring.OpsRuntime) gin.HandlerFunc {
	if cfg == nil || !cfg.Monitoring.OpsMetricsEnabled || runtime == nil || runtime.Metrics == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if !isOpsGatewayRoute(c) {
			c.Next()
			return
		}
		var rec *monitoring.OpsRecorder
		var body *monitoring.OpsBody
		safeObserve(func() {
			ctx := monitoring.WithOpsRecorderDependencies(c.Request.Context(), runtime.Accounts, runtime.Coverage, runtime.SampleSink)
			ctx, rec = monitoring.BeginGatewayRequest(ctx, c.FullPath(), runtime.Metrics)
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

// Both completion metrics must use the same allowlist of gateway route templates.
func isOpsGatewayRoute(c *gin.Context) bool {
	method := c.Request.Method
	switch c.FullPath() {
	case "/v1/responses", "/responses", "/backend-api/codex/responses":
		return method == http.MethodPost || method == http.MethodGet
	case "/v1/responses/*subpath", "/responses/*subpath", "/backend-api/codex/responses/*subpath":
		return method == http.MethodPost && service.IsForwardableOpenAIResponsesRequestPath(c) && !service.IsOpenAIResponsesInputTokensRequestPath(c)
	case "/v1/messages", "/antigravity/v1/messages", "/v1/chat/completions", "/chat/completions", "/v1/embeddings", "/embeddings",
		"/v1/images/generations", "/v1/images/edits", "/v1/images/generations/async", "/v1/images/edits/async", "/v1/images/batches/:id/cancel",
		"/v1beta/models/*modelAction", "/antigravity/v1beta/models/*modelAction":
		return method == http.MethodPost
	case "/v1/images/tasks/:task_id", "/v1/images/batches/:id/items", "/v1/images/batches/:id/items/:custom_id/content", "/v1/images/batches/:id/download":
		return method == http.MethodGet
	case "/v1/images/batches", "/v1beta/cachedContents":
		return method == http.MethodGet || method == http.MethodPost
	case "/v1/images/batches/:id":
		return method == http.MethodGet || method == http.MethodDelete
	case "/v1/images/batches/:id/outputs":
		return method == http.MethodDelete
	case "/v1beta/cachedContents/:cacheID":
		return method == http.MethodGet || method == http.MethodPatch || method == http.MethodDelete
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
