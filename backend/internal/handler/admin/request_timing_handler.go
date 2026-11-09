package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *UsageHandler) GetTiming(c *gin.Context) {
	if h.timingQueryEnabled != nil && !*h.timingQueryEnabled {
		response.Error(c, http.StatusNotFound, "Request timing is disabled")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid usage id")
		return
	}
	var at *time.Time
	if value := c.Query("created_at"); value != "" {
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			response.BadRequest(c, "Invalid usage created_at")
			return
		}
		at = &t
	}
	log, err := h.usageService.GetTimingUsage(c.Request.Context(), id, at)
	if errors.Is(err, service.ErrUsageLogNotFound) {
		response.Error(c, http.StatusNotFound, "Usage not found")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if at != nil && !at.Equal(log.CreatedAt) {
		response.Error(c, http.StatusNotFound, "Usage not found")
		return
	}
	response.Success(c, service.RequestTimingDetailFromUsage(log))
}

// SetTimingQueryEnabled is called during router construction, before serving.
func (h *UsageHandler) SetTimingQueryEnabled(enabled bool) {
	if h != nil {
		h.timingQueryEnabled = &enabled
	}
}

func parseRequestTimingFilter(c *gin.Context) (*service.RequestTimingFilter, error) {
	if _, err := url.ParseQuery(c.Request.URL.RawQuery); err != nil {
		return nil, errors.New("Invalid request timing query encoding")
	}
	start, end, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		return nil, err
	}
	f := &service.RequestTimingFilter{StartTime: start, EndTime: end, Platform: strings.TrimSpace(c.Query("platform")), Model: strings.TrimSpace(c.Query("model")), Mode: c.Query("query_mode"), Dimension: c.Query("dimension"), Sort: c.Query("sort")}
	for key, target := range map[string]**int64{"group_id": &f.GroupID, "account_id": &f.AccountID, "body_bytes_min": &f.BodyBytesMin} {
		if value := c.Query(key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return nil, errors.New("Invalid " + key)
			}
			*target = &n
		}
	}
	if value := c.Query("bucket_seconds"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, errors.New("Invalid bucket_seconds")
		}
		f.BucketSeconds = n
	}
	if value := c.Query("top"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, errors.New("Invalid top")
		}
		f.Top = n
	}
	if value := c.Query("entities"); value != "" {
		for _, s := range strings.Split(value, ",") {
			s = strings.TrimSpace(s)
			if s == "" || len(s) > 100 {
				return nil, errors.New("Invalid timing entity")
			}
			f.Entities = append(f.Entities, s)
		}
	}
	if err := f.Normalize(time.Now()); err != nil {
		return nil, err
	}
	return f, nil
}

func (h *OpsHandler) GetRequestTimingTrend(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	f, err := parseRequestTimingFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if c.Query("start_time") == "" && c.Query("end_time") == "" {
		duration := f.EndTime.Sub(f.StartTime)
		f.EndTime = f.EndTime.UTC().Truncate(time.Minute)
		f.StartTime = f.EndTime.Add(-duration)
	}
	result, err := h.opsService.GetRequestTimingTrend(c.Request.Context(), f)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpsHandler) GetRequestTimingGroups(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	f, err := parseRequestTimingFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.opsService.GetRequestTimingGroups(c.Request.Context(), f)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpsHandler) GetRequestTimingSlow(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	f, err := parseRequestTimingFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.opsService.GetRequestTimingSlow(c.Request.Context(), f)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
