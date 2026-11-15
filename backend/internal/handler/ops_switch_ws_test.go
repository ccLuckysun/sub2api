package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Attach a recorder while retaining the live WebSocket request cancellation.
// The surrounding existing tests use actual client/upstream WebSocket servers.
func installOpsSwitchRecorder(t *testing.T, router *gin.Engine) func(float64) {
	t.Helper()
	_, _, m := opsRequestFixture(t)
	router.Use(func(c *gin.Context) {
		ctx, rec := monitoring.BeginGatewayRequest(c.Request.Context(), "/v1/responses", m)
		c.Request = c.Request.WithContext(ctx)
		defer func() { rec.Finalize(monitoring.FinalizeInput{Status: c.Writer.Status()}) }()
		c.Next()
	})
	return func(want float64) { t.Helper(); require.Equal(t, want, testutil.ToFloat64(m.AccountSwitches)) }
}
