package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func maskOpsAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) >= 24 {
		return key[:8] + "…" + key[len(key)-4:]
	}
	if key != "" {
		return "…"
	}
	return ""
}

// attachOpsRequestMeta is observation-only. Always provide the full snapshot:
// AttachMeta replaces its input, so account failover must retain the authenticated
// identity and original (pre-mapping) model. It never reads or rewrites a body,
// and never queries the database.
func attachOpsRequestMeta(c *gin.Context, apiKey *service.APIKey, userID int64, originalModel string, account *service.Account) {
	if c == nil || c.Request == nil {
		return
	}
	ctx := c.Request.Context()
	recorder := monitoring.RecorderFromContext(ctx)
	if recorder == nil {
		return
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("x-request-id"))
	}
	// ClientRequestID is an independently generated UUID, not this request ID.
	// usage_logs.request_id becomes "client:<ClientRequestID>" (or
	// "local:<RequestID>" when absent), so keep it for the dashboard join.
	clientRequestID, _ := ctx.Value(ctxkey.ClientRequestID).(string)
	clientRequestID = strings.TrimSpace(clientRequestID)
	endpoint := GetInboundEndpoint(c)
	meta := monitoring.OpsRequestMeta{
		RequestID:       requestID,
		ClientRequestID: clientRequestID,
		UserID:          userID,
		Model:           originalModel,
		Endpoint:        endpoint,
		EndpointClass:   monitoring.EndpointClass(endpoint),
	}
	if apiKey != nil {
		meta.APIKeyID = apiKey.ID
		meta.APIKeyName = apiKey.Name
		meta.APIKeyMasked = maskOpsAPIKey(apiKey.Key)
		if apiKey.User != nil {
			meta.UserEmail = apiKey.User.Email
		}
		if apiKey.GroupID != nil {
			meta.GroupID = *apiKey.GroupID
		}
		if apiKey.Group != nil {
			meta.Platform = apiKey.Group.Platform
			meta.GroupName = apiKey.Group.Name
		}
	}
	if platform, ok := service.ResolvedTargetPlatformFromContext(ctx); ok {
		meta.Platform = platform
	}
	if account != nil {
		meta.AccountID = account.ID
		meta.AccountName = account.Name
		meta.Platform = account.Platform
	}
	recorder.AttachMeta(meta)
}
