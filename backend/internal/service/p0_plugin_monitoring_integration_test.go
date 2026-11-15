//go:build integration

package service_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A handled plugin uses the real in-memory gRPC fixture and real Gin ingress.
// The test-only factory exposes the existing fixture to this external package.
func TestP0MonitoringPluginReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"200", "SSE", "429", "500", "502", "timeout", "abort_SSE", "abort_JSON"} {
		t.Run(scenario, func(t *testing.T) {
			var baseline p0Snapshot
			for _, enabled := range []bool{false, true} {
				stream := scenario == "SSE" || scenario == "abort_SSE"
				input := fmt.Sprintf(`{"model":"gpt-4o","stream":%t,"instructions":"test","input":"hello"}`, stream)
				status, payload := 200, p0ResponsesJSON
				if stream {
					payload = p0ResponsesSSE
				}
				if scenario == "429" {
					status = 429
				}
				if scenario == "500" {
					status = 500
				}
				if scenario == "502" {
					status = 502
				}
				if status >= 400 {
					payload = `{"error":{"type":"api_error","message":"fixture failure"}}`
				}
				if scenario == "timeout" {
					payload = "@headers_timeout"
				}
				cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 1 << 20}}
				cfg.Monitoring.OpsMetricsEnabled = enabled
				reg := prometheus.NewRegistry()
				metrics, err := monitoring.NewOpsMetrics(reg)
				require.NoError(t, err)
				svc, billing, capture := service.NewP0PluginFixtureForIntegration(t, cfg, status, payload)
				a := &service.Account{ID: 89, Name: "p0-plugin", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Concurrency: 1,
					Credentials: map[string]any{"access_token": "fixture-only", "chatgpt_account_id": "fixture-account"}}
				var got p0Snapshot
				sink := &p0SampleSink{}
				runtime := monitoring.NewOpsRuntime(metrics)
				runtime.SampleSink = sink
				router := gin.New()
				router.Use(middleware.OpsMiddleware(cfg, runtime), middleware.Recovery())
				router.POST("/v1/responses", func(c *gin.Context) {
					body, readErr := handler.ReadGatewayJSONRequestBody(c.Request, cfg)
					require.NoError(t, readErr)
					rec := monitoring.RecorderFromContext(c.Request.Context())
					rec.AttachMeta(monitoring.OpsRequestMeta{Model: "gpt-4o"})
					result, forwardErr := svc.Forward(c.Request.Context(), c, a, body)
					if forwardErr != nil {
						got.Failure = forwardErr.Error()
					}
					if result != nil {
						got.Usage = result.Usage
						price := 0.000001
						group := &service.Group{ID: 1, Platform: service.PlatformOpenAI, ModelPricing: []service.ChannelModelPricing{{Models: []string{result.Model}, BillingMode: service.BillingModeToken, InputPrice: &price, OutputPrice: &price}}}
						got.Cost, err = billing.CalculateTokenCostForRequest(service.TokenCostRequest{Ctx: c.Request.Context(), Model: result.Model, Group: group,
							Tokens: service.UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, RateMultiplier: 1, Resolver: service.NewModelPricingResolver(nil, billing)})
						require.NoError(t, err)
					}
					rec.MarkFinish()
				})
				w := &p0Writer{ResponseRecorder: httptest.NewRecorder(), fail: strings.HasPrefix(scenario, "abort")}
				router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(input)))
				requests, httpCalls := capture()
				require.Zero(t, httpCalls, "plugin handled=true never sends HTTP")
				for _, request := range requests {
					got.Requests = append(got.Requests, p0Request{Header: request.Header, Body: request.Body})
				}
				got.Body, got.Header, got.Status, got.Flushes, got.Chunks = w.Body.String(), w.Header().Clone(), w.Code, w.flushes, w.chunks
				if !enabled {
					baseline = got
					continue
				}
				assert.Equal(t, baseline, got, "plugin request/response/SSE/chunks/Flush/usage/cost equivalence")
				families, err := reg.Gather()
				require.NoError(t, err)
				all := map[string]*dto.MetricFamily{}
				for _, family := range families {
					all[family.GetName()] = family
				}
				respBytes := len(payload)
				metricScenario := scenario
				if scenario == "timeout" {
					respBytes, metricScenario = 0, "headers_timeout"
				}
				if w.fail {
					metricScenario = "abort"
				}
				p0AssertMetrics(t, all, sink.samples, metricScenario, len(input), w.Body.Len(), respBytes, got)
			}
		})
	}
}
