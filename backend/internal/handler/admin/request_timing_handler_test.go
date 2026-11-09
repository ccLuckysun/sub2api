package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestTimingFilterRejectsUnboundedOrUnsafeQueries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, query := range []string{
		"?sort=upload_ms;DROP+TABLE+usage_logs", "?sort=upload_ms%3BDROP+TABLE+usage_logs", "?dimension=ip", "?top=11", "?bucket_seconds=1", "?query_mode=unknown", "?body_bytes_min=-1", "?dimension=account&entities=not-an-id", "?start_time=2026-01-01T00:00:00Z&end_time=2026-02-01T00:00:00Z", "?start_time=bad",
	} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/timing"+query, nil)
			_, err := parseRequestTimingFilter(c)
			require.Error(t, err)
		})
	}
}

func TestRequestTimingOpsUnavailableIsHandled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, handler := range []gin.HandlerFunc{(&OpsHandler{}).GetRequestTimingTrend, (&OpsHandler{}).GetRequestTimingGroups, (&OpsHandler{}).GetRequestTimingSlow} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/timing", nil)
		handler(c)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
	}
}
