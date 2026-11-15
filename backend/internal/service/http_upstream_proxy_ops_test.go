package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamProxyConnectOpsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reset  bool
		reason string
	}{
		{"CONNECT_502", 502, false, "proxy_unreachable"},
		{"CONNECT_503", 503, false, "proxy_unreachable"},
		{"CONNECT_504", 504, false, "proxy_unreachable"},
		{"successful_tunnel_not_origin_status", 200, false, "unknown"},
		{"next_send_resets_old_failure", 502, true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/responses", metrics)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			proxyID := int64(1)
			account := &Account{Name: "proxy-test", ProxyID: &proxyID, Proxy: &Proxy{}}
			ops := newCompatForwardOps(ctx, c, account)
			req, err := http.NewRequestWithContext(ctx, "POST", "https://fixture.invalid", nil)
			require.NoError(t, err)
			req = ops.prepare(req)
			NoteHTTPUpstreamProxyConnectResponse(req.Context(), tc.status)
			if tc.reset {
				NoteHTTPUpstreamProxyConnectResponse(req.Context(), 0)
			}
			cause := errors.New("Bad Gateway") // net/http's original untyped error.
			ops.fail(cause, "transport", 0, "", false)
			rec.Finalize(monitoring.FinalizeInput{Status: 502})
			all := forwardOpsGather(t, reg)
			require.Equal(t, "upstream_error", all["sub2api_gateway_requests_total"][0].labels["result"])
			require.Equal(t, tc.reason, all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
			require.NoError(t, c.Request.Context().Err())
			require.Equal(t, "Bad Gateway", cause.Error(), "observation never replaces the business error")
		})
	}
	// Disabled/no observation context remains a no-op.
	NoteHTTPUpstreamProxyConnectResponse(context.Background(), 502)
}
