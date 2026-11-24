package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
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
		if endpoint, ok := opsWSUpgradeEndpoint(c); ok {
			var obs *monitoring.WSObserver
			safeObserve(func() {
				obs = monitoring.NewWSObserver(runtime.Metrics, endpoint)
				if obs != nil {
					c.Request = c.Request.WithContext(monitoring.WithWSObserver(c.Request.Context(), obs))
				}
			})
			defer func() {
				safeObserve(func() {
					obs.FinalizeFallback(c.Writer.Status())
				})
			}()
			c.Next()
			return
		}
		if !isOpsGatewayRoute(c) {
			c.Next()
			return
		}
		var rec *monitoring.OpsRecorder
		var body *monitoring.OpsBody
		safeObserve(func() {
			ctx := monitoring.WithOpsRecorderDependencies(c.Request.Context(), runtime.Accounts, runtime.Coverage, runtime.SampleSink)
			ctx, rec = monitoring.BeginGatewayRequest(ctx, c.FullPath(), runtime.Metrics)
			rec.SetHTTPMethod(c.Request.Method)
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
				reqID, clientReqID := extractOpsCorrelationIDs(c)
				if reqID != "" || clientReqID != "" {
					rec.MergeMeta(monitoring.OpsRequestMeta{
						RequestID:       reqID,
						ClientRequestID: clientReqID,
					})
				}
				rec.Finalize(monitoring.FinalizeInput{Status: c.Writer.Status(), Panic: c.GetBool(monitoring.OpsPanicKey)})
			})
		}()
		c.Next()
	}
}

// Both completion metrics must use the same allowlist of gateway route templates.
func isOpsGatewayRoute(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	method := c.Request.Method
	switch c.FullPath() {
	case "/v1/responses", "/responses", "/backend-api/codex/responses":
		if method == http.MethodGet {
			return !strings.EqualFold(strings.TrimSpace(c.GetHeader("Upgrade")), "websocket")
		}
		return method == http.MethodPost
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

// opsWSUpgradeEndpoint classifies gateway WebSocket upgrade requests into the
// isolated WS metric endpoint label ("responses", "realtime", or "live") without
// letting them enter OpsRecorder or HTTP 6-phase instrumentation.
func opsWSUpgradeEndpoint(c *gin.Context) (string, bool) {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodGet {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("Upgrade")), "websocket") {
		return "", false
	}
	switch c.FullPath() {
	case "/v1/responses", "/responses", "/backend-api/codex/responses":
		return "responses", true
	case "/v1/realtime", "/realtime":
		return "realtime", true
	case "/v1/live/:call_id", "/backend-api/codex/:call_id":
		return "live", true
	default:
		return "", false
	}
}

func getOpsRecorder(c *gin.Context) *monitoring.OpsRecorder {
	if c == nil || c.Request == nil {
		return nil
	}
	return monitoring.RecorderFromContext(c.Request.Context())
}

func extractOpsCorrelationIDs(c *gin.Context) (requestID, clientRequestID string) {
	if c == nil || c.Request == nil {
		return "", ""
	}
	ctx := c.Request.Context()
	requestID, _ = ctx.Value(ctxkey.RequestID).(string)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("x-request-id"))
	}
	clientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
	clientRequestID = strings.TrimSpace(clientRequestID)
	return requestID, clientRequestID
}

// RecordOpsAbortError records a gateway/middleware abort message and optional
// code onto the active OpsRecorder without overwriting upstream error details.
func RecordOpsAbortError(c *gin.Context, code, message string) {
	rec := getOpsRecorder(c)
	if rec == nil {
		return
	}
	safeObserve(func() {
		rec.RecordFallbackErrorDetails(message, 0, code)
	})
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
