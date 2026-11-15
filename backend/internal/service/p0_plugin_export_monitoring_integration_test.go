//go:build integration

package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// P0PluginRequest is a test-only value snapshot of the existing gRPC fixture.
type P0PluginRequest struct {
	Body   string
	Header http.Header
}

// NewP0PluginFixtureForIntegration exports the private fixture only to external
// integration tests. This keeps their real middleware/repository imports out of
// package service and avoids an import cycle. No production API is added.
func NewP0PluginFixtureForIntegration(t *testing.T, cfg *config.Config, status int, payload string) (*OpenAIGatewayService, *BillingService, func() ([]P0PluginRequest, int)) {
	t.Helper()
	resetGatewayForwardingSettingsCacheForTest(t)
	upstream := &forwardOpsUpstream{responses: func(int) *http.Response {
		t.Error("handled plugin invoked HTTP fallback")
		return nil
	}}
	billing := NewBillingService(cfg, nil)
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, billingService: billing,
		rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}, deferredService: &DeferredService{}}
	manager, plugin := newResponsesOpsPlugin(t, status, payload)
	svc.pluginManager = manager
	return svc, billing, func() ([]P0PluginRequest, int) {
		plugin.mu.Lock()
		defer plugin.mu.Unlock()
		var requests []P0PluginRequest
		for _, request := range plugin.requests {
			requests = append(requests, P0PluginRequest{request.body, request.headers.Clone()})
		}
		return requests, len(upstream.requests)
	}
}
