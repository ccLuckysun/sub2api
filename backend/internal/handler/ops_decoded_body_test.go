package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestOpsDecodedBodyFiveDirections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const decoded = `{"text":"actual bytes"}`
	for _, route := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1beta/models/*modelAction", "/antigravity/v1/messages"} {
		for _, mode := range []string{"plain", "gzip", "BOM", "preread"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				wire := []byte(decoded)
				if mode == "BOM" {
					wire = append([]byte{0xef, 0xbb, 0xbf}, wire...)
				}
				if mode == "gzip" {
					var b bytes.Buffer
					z := gzip.NewWriter(&b)
					_, err := z.Write(wire)
					require.NoError(t, err)
					require.NoError(t, z.Close())
					wire = b.Bytes()
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("upstream read: %v", err)
						return
					}
					_, _ = w.Write(append([]byte("response:"), body...))
				}))
				defer upstream.Close()
				reg := prometheus.NewRegistry()
				m, err := monitoring.NewOpsMetrics(reg)
				require.NoError(t, err)
				cfg := &config.Config{}
				cfg.Monitoring.OpsMetricsEnabled = true
				router := gin.New()
				router.Use(middleware2.OpsMiddleware(cfg, monitoring.NewOpsRuntime(m)))
				var actual []byte
				router.POST(route, func(c *gin.Context) {
					if mode == "preread" {
						body, e := io.ReadAll(c.Request.Body)
						require.NoError(t, e)
						c.Request.Body = pkghttputil.NewPrereadBody(body)
					}
					if strings.Contains(route, "v1beta") {
						actual, err = readRequestBodyWithPrealloc(c.Request)
					} else {
						actual, err = readLenientJSONRequestBodyWithPrealloc(c.Request, cfg)
					}
					require.NoError(t, err)
					rec := monitoring.RecorderFromContext(c.Request.Context())
					a := rec.StartAttempt("unknown")
					req, e := http.NewRequestWithContext(monitoring.WithAttempt(c.Request.Context(), a), "POST", upstream.URL, bytes.NewReader(actual))
					require.NoError(t, e)
					resp, e := repository.NewHTTPUpstream(nil).Do(req, "", 1, 1)
					require.NoError(t, e)
					a.UseResponseFor(monitoring.ResponseHandle(resp))
					out, e := io.ReadAll(resp.Body)
					require.NoError(t, e)
					require.NoError(t, resp.Body.Close())
					_, e = c.Writer.Write(out)
					require.NoError(t, e)
					a.Done("success", "", monitoring.FailureEvidence{})
					rec.MarkFinish()
				})
				url := strings.Replace(route, "*modelAction", "gemini:generateContent", 1)
				req := httptest.NewRequest("POST", url, bytes.NewReader(wire))
				if mode == "gzip" {
					req.Header.Set("Content-Encoding", "gzip")
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, 200, w.Code)
				baseline := httptest.NewRequest("POST", url, bytes.NewReader(wire))
				if mode == "gzip" {
					baseline.Header.Set("Content-Encoding", "gzip")
				}
				var want []byte
				if strings.Contains(route, "v1beta") {
					want, err = readRequestBodyWithPrealloc(baseline)
				} else {
					want, err = readLenientJSONRequestBodyWithPrealloc(baseline, cfg)
				}
				require.NoError(t, err)
				require.Equal(t, want, actual, "nil Recorder preserves decoder output")
				for dir, n := range map[string]int{"in_wire": len(wire), "req_decoded": len(actual), "req_upstream": len(actual), "resp_upstream": len(w.Body.Bytes()), "resp_downstream": w.Body.Len()} {
					require.Equal(t, float64(n), testutil.ToFloat64(m.TransferBytes.WithLabelValues(dir)), dir)
				}
				t.Logf("M2 wire=%d decoded=%d upstream_req=%d upstream_resp=%d downstream=%d", len(wire), len(actual), len(actual), w.Body.Len(), w.Body.Len())
			})
		}
	}
}

func TestOpsDecodedBodyReadFailure(t *testing.T) {
	ctx, rec, m := opsRequestFixture(t)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader("not gzip")).WithContext(ctx)
	req.Header.Set("Content-Encoding", "gzip")
	_, err := readLenientJSONRequestBodyWithPrealloc(req, nil)
	require.Error(t, err)
	rec.Finalize(monitoring.FinalizeInput{Status: 400})
	require.Zero(t, testutil.ToFloat64(m.TransferBytes.WithLabelValues("req_decoded")), "failed decode never publishes complete decoded bytes")
	_, err = readRequestBodyWithPrealloc(httptest.NewRequest("POST", "/v1/messages", nil).WithContext(context.Background()))
	require.NoError(t, err)
}

func TestOpsDecodedBodyP0Handlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"GatewayMessages", "GatewayChat", "GatewayResponses", "OpenAIResponses", "OpenAIMessages", "OpenAIChat", "Gemini", "Antigravity", "unread_auth_rejection"} {
		t.Run(path, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Monitoring.OpsMetricsEnabled = true
			reg := prometheus.NewRegistry()
			m, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			gateway := &GatewayHandler{cfg: cfg}
			oai := &OpenAIGatewayHandler{cfg: cfg, gatewayService: &service.OpenAIGatewayService{}, billingCacheService: &service.BillingCacheService{}, apiKeyService: &service.APIKeyService{}, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(nil), "", time.Second)}
			body := "{"
			route := "/v1/messages"
			platform := service.PlatformAnthropic
			var handle gin.HandlerFunc
			switch path {
			case "GatewayMessages", "unread_auth_rejection":
				handle = gateway.Messages
			case "GatewayChat":
				handle = gateway.ChatCompletions
				route = "/v1/chat/completions"
			case "GatewayResponses":
				handle = gateway.Responses
				route = "/v1/responses"
			case "OpenAIResponses":
				handle = oai.Responses
				route = "/v1/responses"
				platform = service.PlatformOpenAI
			case "OpenAIMessages":
				handle = oai.Messages
				platform = service.PlatformOpenAI
			case "OpenAIChat":
				handle = oai.ChatCompletions
				route = "/v1/chat/completions"
				platform = service.PlatformOpenAI
			case "Gemini", "Antigravity":
				handle = gateway.GeminiV1BetaModels
				route = "/v1beta/models/*modelAction"
				platform = service.PlatformGemini
				body = ""
			}
			if path == "Antigravity" {
				route = "/antigravity/v1beta/models/*modelAction"
			}
			router := gin.New()
			router.Use(middleware2.OpsMiddleware(cfg, monitoring.NewOpsRuntime(m)), middleware2.Recovery())
			if path == "Antigravity" {
				router.Use(middleware2.ForcePlatform(service.PlatformAntigravity))
			}
			router.POST(route, func(c *gin.Context) {
				if path != "unread_auth_rejection" {
					groupID := int64(1)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &groupID, User: &service.User{ID: 1}, Group: &service.Group{ID: 1, Platform: platform, AllowMessagesDispatch: true}})
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1, Concurrency: 1})
				}
				handle(c)
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", strings.Replace(route, "*modelAction", "gemini:generateContent", 1), strings.NewReader(body)))
			if path == "unread_auth_rejection" {
				require.Equal(t, 401, w.Code)
			} else {
				require.Equal(t, 400, w.Code, w.Body.String())
			}
			families, err := reg.Gather()
			require.NoError(t, err)
			found := false
			for _, family := range families {
				if family.GetName() == "sub2api_transfer_bytes_total" {
					for _, metric := range family.Metric {
						for _, label := range metric.Label {
							if label.GetName() == "direction" && label.GetValue() == "req_decoded" {
								found = true
								require.Equal(t, float64(len(body)), metric.GetCounter().GetValue())
							}
						}
					}
				}
			}
			if path == "unread_auth_rejection" {
				require.False(t, found)
			} else {
				require.True(t, found, "real P0 body reader must publish req_decoded, including zero")
			}
		})
	}
}
