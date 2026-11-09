package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// usageRequestTiming returns the handle created by the HTTP logger middleware.
// Handlers pass this pointer explicitly into RecordUsageInput; the usage
// worker context never carries it.
func usageRequestTiming(c *gin.Context) *requesttiming.Handle {
	if c == nil || c.Request == nil {
		return nil
	}
	return requesttiming.FromContext(c.Request.Context())
}

// markUsageTimingEligible marks a normal, successful HTTP request as eligible
// for gateway_latency_ms enrichment. Call it only on the success path, after
// the forward/stream has completed without partial/client-disconnect errors,
// and never for WS/live/cyber requests.
func markUsageTimingEligible(c *gin.Context) {
	if c == nil {
		return
	}
	// Cyber-policy rows are explicitly excluded from the timing candidate set.
	if service.GetOpsCyberPolicy(c) != nil {
		return
	}
	usageRequestTiming(c).MarkEligible()
}
