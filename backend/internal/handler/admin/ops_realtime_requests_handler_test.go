package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubRealtimeRequestsReader struct {
	lastFilter service.RealtimeRequestsFilter
	resp       *service.RealtimeRequestsResponse
	err        error
}

func (s *stubRealtimeRequestsReader) ListRealtimeRequests(_ context.Context, filter service.RealtimeRequestsFilter) (*service.RealtimeRequestsResponse, error) {
	s.lastFilter = filter
	return s.resp, s.err
}

func TestOpsHandlerGetRealtimeRequestsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/realtime-requests", nil)

	h.GetRealtimeRequests(c)

	require.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Code int                              `json:"code"`
		Data service.RealtimeRequestsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Equal(t, 0, envelope.Code)
	require.False(t, envelope.Data.Enabled)
	require.Empty(t, envelope.Data.Items)
	require.Empty(t, envelope.Data.Models)
	require.Empty(t, envelope.Data.FailureReasons)
	require.Zero(t, envelope.Data.Summary.Total)
	require.Contains(t, w.Body.String(), `"items":[]`)
	require.Contains(t, w.Body.String(), `"models":[]`)
	require.Contains(t, w.Body.String(), `"failure_reasons":[]`)
}

func TestOpsHandlerGetRealtimeRequestsFiltersAndClamping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 11, 21, 6, 10, 5, 0, time.UTC)
	reader := &stubRealtimeRequestsReader{
		resp: &service.RealtimeRequestsResponse{
			Enabled:       true,
			BufferSize:    2000,
			TTLHours:      24,
			TotalInBuffer: 1,
			Summary: service.RealtimeRequestsSummary{
				Total:       1,
				Success:     1,
				SuccessRate: 100,
				AvgTotalMs:  420,
			},
			Items:     []monitoring.OpsSample{{ObservedAt: now, RequestID: "req-1"}},
			Models:    []string{"claude-sonnet"},
			Timestamp: now,
		},
	}
	h := NewOpsHandler(nil)
	h.SetRealtimeRequestsReader(reader)

	t.Run("forwards filters and clamps high limit", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/realtime-requests?result=success&model=claude-sonnet&failure_reason=upstream_server_error&search=alice&limit=999", nil)
		h.GetRealtimeRequests(c)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "success", reader.lastFilter.Result)
		require.Equal(t, "claude-sonnet", reader.lastFilter.Model)
		require.Equal(t, "upstream_server_error", reader.lastFilter.FailureReason)
		require.Equal(t, "alice", reader.lastFilter.Search)
		require.Equal(t, 500, reader.lastFilter.Limit)
	})

	t.Run("clamps low limit to 1", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/realtime-requests?limit=0", nil)
		h.GetRealtimeRequests(c)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, reader.lastFilter.Limit)
	})

	t.Run("invalid limit returns 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/realtime-requests?limit=abc", nil)
		h.GetRealtimeRequests(c)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("reader error returns 500", func(t *testing.T) {
		errReader := &stubRealtimeRequestsReader{err: errors.New("redis down")}
		hErr := NewOpsHandler(nil)
		hErr.SetRealtimeRequestsReader(errReader)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/realtime-requests", nil)
		hErr.GetRealtimeRequests(c)

		require.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
