package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestGatewayRoutesOpsEligibilityParity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []struct {
		name    string
		enabled bool
	}{{"ops_enabled", true}, {"ops_disabled", false}} {
		t.Run(mode.name, func(t *testing.T) {
			cfg := &config.Config{
				Gateway:    config.GatewayConfig{MaxBodySize: 1024, TextMaxBodySize: 1024},
				Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: mode.enabled},
			}
			instruments, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
			require.NoError(t, err)
			router := gin.New()
			var matchedTemplate, endpoint string
			var recorder *monitoring.OpsRecorder
			router.Use(func(c *gin.Context) {
				c.Next()
				matchedTemplate = c.FullPath()
				endpoint = handler.GetInboundEndpoint(c)
				recorder = monitoring.RecorderFromContext(c.Request.Context())
			})
			router.Use(gin.HandlerFunc(servermiddleware.ProvideMetricsMiddleware(cfg, instruments.Metrics)))
			router.Use(servermiddleware.OpsMiddleware(cfg, monitoring.NewOpsRuntime(instruments.Ops)), servermiddleware.Recovery())
			RegisterGatewayRoutes(router, &handler.Handlers{
				Gateway:       &handler.GatewayHandler{},
				OpenAIGateway: &handler.OpenAIGatewayHandler{},
			}, func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }, nil, nil, nil, nil, nil, cfg)

			settled := func() float64 {
				t.Helper()
				families, err := instruments.Registry.Gather()
				require.NoError(t, err)
				count := 0.0
				for _, family := range families {
					if family.GetName() == "sub2api_gateway_requests_total" {
						for _, metric := range family.GetMetric() {
							count += metric.GetCounter().GetValue()
						}
					}
				}
				return count
			}
			check := func(method, path, template, wantEndpoint string, eligible bool) {
				t.Run(method+" "+path, func(t *testing.T) {
					eligibleBefore := testutil.ToFloat64(instruments.Metrics.OpsEligibleRequestsTotal)
					settledBefore := settled()
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
					require.Equal(t, template, matchedTemplate)
					if wantEndpoint != "" {
						require.Equal(t, wantEndpoint, endpoint)
					}
					if template != "" {
						require.Equal(t, http.StatusUnauthorized, response.Code)
					}
					if eligible {
						eligibleBefore++
					}
					if eligible && mode.enabled {
						settledBefore++
						require.NotNil(t, recorder)
					} else {
						require.Nil(t, recorder)
					}
					require.Equal(t, eligibleBefore, testutil.ToFloat64(instruments.Metrics.OpsEligibleRequestsTotal))
					require.Equal(t, settledBefore, settled())
				})
			}

			for _, root := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
				check(http.MethodGet, root, root, handler.EndpointResponses, true)
				check(http.MethodPost, root, root, handler.EndpointResponses, true)
				check(http.MethodPost, root+"/compact", root+"/*subpath", handler.EndpointResponsesCompact, true)
				check(http.MethodPost, root+"/resp_123/cancel", root+"/*subpath", handler.EndpointResponses, true)
				check(http.MethodPost, root+"/input_tokens?model=gpt-5", root+"/*subpath", handler.EndpointResponsesInputTokens, false)
				check(http.MethodPost, root+"/%69nput_tokens/", root+"/*subpath", handler.EndpointResponsesInputTokens, false)
				check(http.MethodPost, root+"/../compact", root+"/*subpath", "", false)
				check(http.MethodGet, root+"/compact", "", "", false)
			}
			for _, path := range []string{"/v1/embeddings", "/embeddings"} {
				check(http.MethodPost, path, path, handler.EndpointEmbeddings, true)
				check(http.MethodGet, path, "", "", false)
			}
			for _, path := range []string{"/v1/messages", "/antigravity/v1/messages", "/v1/chat/completions", "/chat/completions", "/v1/images/generations", "/v1beta/cachedContents"} {
				check(http.MethodPost, path, path, "", true)
			}
			for _, prefix := range []string{"/v1beta", "/antigravity/v1beta"} {
				check(http.MethodPost, prefix+"/models/gemini:generateContent", prefix+"/models/*modelAction", "", true)
				check(http.MethodGet, prefix+"/models/gemini", prefix+"/models/:model", "", false)
			}
			for _, path := range []string{"/v1/models", "/models", "/backend-api/codex/models", "/antigravity/models", "/antigravity/v1/models", "/v1/images/batches/models", "/v1/usage", "/v1/sub2api/billing", "/antigravity/v1/usage"} {
				check(http.MethodGet, path, path, "", false)
			}
			for _, path := range []string{"/v1/messages/count_tokens", "/messages/count_tokens", "/antigravity/v1/messages/count_tokens", "/v1/live", "/backend-api/codex/realtime/calls", "/backend-api/codex/alpha/search", "/images/generations"} {
				check(http.MethodPost, path, path, "", false)
			}
			check(http.MethodGet, "/backend-api/codex/call_1", "/backend-api/codex/:call_id", "", false)
			check(http.MethodPost, "/backend-api/codex/responses-evil", "", "", false)
		})
	}
}
