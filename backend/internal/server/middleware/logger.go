package middleware

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TimingOptions controls request-timing collection in the access logger.
type TimingOptions struct {
	Enabled bool
	Sink    requesttiming.Sink
}

// Logger 请求日志中间件
func Logger(timingOptions ...TimingOptions) gin.HandlerFunc {
	var timing TimingOptions
	if len(timingOptions) > 0 {
		timing = timingOptions[0]
	}
	return func(c *gin.Context) {
		// 开始时间
		startTime := time.Now()

		// 请求路径
		path := c.Request.URL.Path

		// Timing handle is created before any route middleware/handler can read
		// the body, then finalized with the same clock reading as access-log
		// latency. A defer keeps panic/early-abort paths finalized; Recovery is
		// registered outside this middleware.
		var (
			timingHandle *requesttiming.Handle
			endTime      time.Time
		)
		if timing.Enabled && shouldCollectRequestTiming(path) {
			timingHandle = requesttiming.New(startTime, timing.Sink)
			c.Request = c.Request.WithContext(requesttiming.WithContext(c.Request.Context(), timingHandle))
			encoding := strings.ToLower(strings.TrimSpace(c.Request.Header.Get("Content-Encoding")))
			if encoding == "" || encoding == "identity" {
				c.Request.Body = requesttiming.ObserveIdentityBody(c.Request.Body, timingHandle)
			}
			defer func() {
				if endTime.IsZero() {
					endTime = time.Now()
				}
				if c.Writer.Status() < 200 || c.Writer.Status() >= 300 || c.Request.Context().Err() != nil {
					timingHandle.Invalidate()
				}
				timingHandle.Finalize(endTime)
			}()
		}

		// 处理请求。若下游 panic，先撤销本请求的 timing 补写资格，再由外层
		// Recovery 处理；本中间件注册的 deferred Finalize 仍会固定总耗时。
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					if timingHandle != nil {
						timingHandle.Invalidate()
					}
					panic(recovered)
				}
			}()
			c.Next()
		}()

		// 跳过健康检查等高频探针路径的日志
		if path == "/health" || path == "/setup/status" {
			return
		}

		endTime = time.Now()
		latency := endTime.Sub(startTime)

		method := c.Request.Method
		statusCode := c.Writer.Status()
		clientIP := ip.GetClientIP(c)
		protocol := c.Request.Proto
		accountID, hasAccountID := c.Request.Context().Value(ctxkey.AccountID).(int64)
		platform, _ := c.Request.Context().Value(ctxkey.Platform).(string)
		model, _ := c.Request.Context().Value(ctxkey.Model).(string)
		reason, rejected := GetIngressRejectReason(c)
		if rejected {
			recordIngressReject(c, reason)
			allowed, droppedSummary := globalIngressRejectAccessSampler.allow(endTime)
			if droppedSummary > 0 {
				logger.FromContext(c.Request.Context()).Info("ingress rejection access logs dropped",
					zap.String("component", "http.access"),
					zap.Uint64("dropped_count", droppedSummary),
					zap.Bool(logger.OpsSystemLogSkipField, true),
				)
			}
			if !allowed {
				return
			}
		}

		fields := []zap.Field{
			zap.String("component", "http.access"),
			zap.Int("status_code", statusCode),
			zap.Int64("latency_ms", latency.Milliseconds()),
			zap.String("client_ip", clientIP),
			zap.String("protocol", protocol),
			zap.String("method", method),
			zap.String("path", path),
		}
		if rejected {
			fields = append(fields,
				zap.String("ingress_reject_reason", string(reason)),
				zap.Bool(logger.OpsSystemLogSkipField, true),
			)
		}
		if hasAccountID && accountID > 0 {
			fields = append(fields, zap.Int64("account_id", accountID))
		}
		if platform != "" {
			fields = append(fields, zap.String("platform", platform))
		}
		if model != "" {
			fields = append(fields, zap.String("model", model))
		}

		l := logger.FromContext(c.Request.Context()).With(fields...)
		l.Info("http request completed", zap.Time("completed_at", endTime))

		if len(c.Errors) > 0 {
			l.Warn("http request contains gin errors", zap.String("errors", c.Errors.String()))
		}
	}
}

// shouldCollectRequestTiming limits the timing handle to gateway/public paths.
// Management APIs, probes and embedded frontend traffic never create one.
func shouldCollectRequestTiming(path string) bool {
	if path == "" || path == "/health" || path == "/setup/status" {
		return false
	}
	return !strings.HasPrefix(path, "/api/")
}
