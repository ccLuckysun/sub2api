package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetRealtimeRequests returns recent gateway request samples buffered in Redis.
// GET /api/v1/admin/ops/realtime-requests
func (h *OpsHandler) GetRealtimeRequests(c *gin.Context) {
	if h == nil || h.realtimeRequestsReader == nil {
		response.Success(c, &service.RealtimeRequestsResponse{
			Enabled:   false,
			Items:     []monitoring.OpsSample{},
			Models:    []string{},
			Timestamp: time.Now().UTC(),
		})
		return
	}

	limit := 100
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			response.BadRequest(c, "Invalid limit")
			return
		}
		if parsed < 1 {
			parsed = 1
		} else if parsed > 500 {
			parsed = 500
		}
		limit = parsed
	}

	filter := service.RealtimeRequestsFilter{
		Result: strings.TrimSpace(c.Query("result")),
		Model:  strings.TrimSpace(c.Query("model")),
		Search: strings.TrimSpace(c.Query("search")),
		Limit:  limit,
	}

	resp, err := h.realtimeRequestsReader.ListRealtimeRequests(c.Request.Context(), filter)
	if err != nil {
		if isOpsRealtimeRequestCanceled(c, err) {
			return
		}
		response.Error(c, http.StatusInternalServerError, "Failed to load realtime requests")
		return
	}

	response.Success(c, resp)
}
