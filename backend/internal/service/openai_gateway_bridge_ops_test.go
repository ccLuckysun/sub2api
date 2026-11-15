package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

const bridgeOpsChatJSON = `{"id":"chatcmpl_fixed","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`
const bridgeOpsChatSSE = "data: {\"id\":\"chatcmpl_fixed\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl_fixed\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\ndata: [DONE]\n\n"
const bridgeOpsPartialChatSSE = "data: {\"id\":\"chatcmpl_fixed\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n"
const bridgeOpsAnthropicJSON = `{"id":"msg_fixed","type":"message","role":"assistant","model":"gpt-4o","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":5}}`
const bridgeOpsAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixed\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-4o\",\"content\":[],\"usage\":{\"input_tokens\":11}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func bridgeOpsAccount(path string) *Account {
	a := &Account{ID: 91, Name: "bridge-ops", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_responses_supported": true, "openai_responses_mode": "auto"}}
	if strings.Contains(path, "raw") {
		a.Extra["openai_responses_supported"] = false
	}
	if strings.Contains(path, "native") {
		a.Platform = PlatformKimi
		a.Credentials["api_protocol"] = APIProtocolAnthropic
		a.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://anthropic.example"}
	}
	if path == "chat_grok" {
		a.Platform = PlatformGrok
		a.Type = AccountTypeOAuth
		a.Credentials = map[string]any{"access_token": "test-token", "refresh_token": "test-refresh", "base_url": "https://cli-chat-proxy.grok.com", "expires_at": "2099-01-01T00:00:00Z"}
	}
	return a
}
func bridgeOpsInvoke(s *OpenAIGatewayService, ctx context.Context, c *gin.Context, a *Account, path string, input []byte) (*OpenAIForwardResult, error) {
	if strings.HasPrefix(path, "messages") {
		return s.ForwardAsAnthropic(ctx, c, a, input, "", "")
	}
	if path == "responses_native" {
		return s.Forward(ctx, c, a, input)
	}
	key := ""
	if path == "chat_grok" {
		key = "ops-cache"
	}
	return s.ForwardAsChatCompletions(ctx, c, a, input, key, "")
}
func TestOpenAIBridgeOpsMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	for _, path := range []string{"chat_responses", "chat_raw", "chat_native", "chat_grok", "messages_responses", "messages_raw", "messages_native", "responses_native"} {
		for _, scenario := range []string{"200", "SSE", "429", "500", "connect_timeout", "read_timeout", "server_cancel", "read_timeout_SSE", "server_cancel_SSE", "abort", "400_code", "400_type", "400_none", "404_code", "404_type", "404_none", "build_failure"} {
			if path == "chat_grok" && scenario == "build_failure" {
				continue
			}
			if strings.HasSuffix(scenario, "_SSE") && path != "chat_raw" {
				continue
			}
			t.Run(path+"/"+scenario, func(t *testing.T) {
				type snapshot struct {
					requests []forwardOpsSnapshot
					body     string
					headers  http.Header
					status   int
					failure  string
					usage    *OpenAIUsage
					cost     *CostBreakdown
				}
				var baseline snapshot
				for _, enabled := range []bool{false, true} {
					stream := scenario == "SSE" || scenario == "abort" || strings.HasSuffix(scenario, "_SSE")
					readFailure := strings.HasPrefix(scenario, "read_timeout") || strings.HasPrefix(scenario, "server_cancel")
					model := "gpt-4o"
					if path == "chat_grok" {
						model = "grok-4.5"
					}
					input := fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, model, stream)
					if path == "chat_grok" {
						input = strings.Replace(input, `"max_tokens":64`, `"max_tokens":128`, 1)
					}
					if path == "responses_native" {
						input = fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello"}`, model, stream)
					}
					reg := prometheus.NewRegistry()
					metrics, e := monitoring.NewOpsMetrics(reg)
					require.NoError(t, e)
					ctx := context.Background()
					var rec *monitoring.OpsRecorder
					entry := time.Now()
					if enabled {
						ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/chat/completions", metrics)
						rec.AttachMeta(monitoring.OpsRequestMeta{Model: model})
						rec.MarkBodyRead(int64(len(input)), int64(len(input)), "complete")
						rec.AddBytes("req_decoded", int64(len(input)), "complete")
					}
					w := httptest.NewRecorder()
					var downstream http.ResponseWriter = w
					if scenario == "abort" {
						downstream = &compatOpsFailWriter{w}
					}
					c, _ := gin.CreateTestContext(downstream)
					c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(input)).WithContext(ctx)
					c.Request.Header.Set("X-Ops-Test", "unchanged")
					if path == "chat_grok" {
						c.Set("api_key", &APIKey{ID: 123})
					}
					a := bridgeOpsAccount(path)
					if scenario == "build_failure" {
						a.Credentials["base_url"] = "://bad-url"
						if strings.Contains(path, "native") {
							a.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "://bad-url"}
						}
					}
					status := 200
					payload := responsesOpsSSE
					if strings.Contains(path, "raw") {
						payload = bridgeOpsChatJSON
						if stream {
							payload = bridgeOpsChatSSE
						}
					}
					if strings.Contains(path, "native") {
						payload = bridgeOpsAnthropicSSE
						if path == "messages_native" && !stream {
							payload = bridgeOpsAnthropicJSON
						}
					}
					if scenario == "429" {
						status = 429
					}
					if scenario == "500" {
						status = 500
					}
					if strings.HasPrefix(scenario, "400") {
						status = 400
					}
					if strings.HasPrefix(scenario, "404") {
						status = 404
					}
					if status >= 400 {
						payload = `{"error":{"message":"failure"}}`
						if strings.HasSuffix(scenario, "code") {
							payload = `{"error":{"code":"model_not_found","type":"other_type","message":"failure"}}`
						}
						if strings.HasSuffix(scenario, "type") {
							payload = `{"error":{"type":"invalid_request_error","message":"failure"}}`
						}
					}
					if strings.HasSuffix(scenario, "_SSE") {
						payload = bridgeOpsPartialChatSSE
					}
					upstream := &forwardOpsUpstream{responses: func(int) *http.Response {
						var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
						if strings.HasPrefix(scenario, "read_timeout") {
							body = &streamReadCloser{err: os.ErrDeadlineExceeded}
							if scenario == "read_timeout_SSE" {
								body = &streamReadCloser{payload: []byte(payload), err: context.DeadlineExceeded}
							}
						}
						if strings.HasPrefix(scenario, "server_cancel") {
							body = &streamReadCloser{err: context.Canceled}
							if scenario == "server_cancel_SSE" {
								body = &streamReadCloser{payload: []byte(payload), err: context.Canceled}
							}
						}
						contentType := "text/event-stream"
						if status >= 400 || payload == bridgeOpsChatJSON || payload == bridgeOpsAnthropicJSON {
							contentType = "application/json"
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}, "X-Request-Id": []string{"bridge-fixed"}}, Body: body}
					}}
					if scenario == "connect_timeout" {
						upstream.failure = &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
					}
					cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}, deferredService: &DeferredService{}}
					svc.billingService = NewBillingService(cfg, nil)
					if path == "chat_grok" {
						svc.grokTokenProvider = NewGrokTokenProvider(nil, nil)
					}
					result, err := bridgeOpsInvoke(svc, ctx, c, a, path, []byte(input))
					if path == "chat_raw" && readFailure && stream {
						require.NoError(t, err, "raw SSE keeps its business return and usage")
						require.NotNil(t, result)
						require.Equal(t, OpenAIUsage{InputTokens: 11, OutputTokens: 5}, result.Usage)
						require.Contains(t, w.Body.String(), "partial")
						require.NoError(t, c.Request.Context().Err(), "upstream failure does not cancel the client")
					}
					if path == "chat_grok" && scenario == "200" {
						require.NoError(t, err, "requests=%v", upstream.requests)
						require.Equal(t, grokChatResponsesEndpoint, result.UpstreamEndpoint)
					}
					terminal := time.Now()
					got := snapshot{requests: upstream.requests, body: normalizeBridgeOpsBody(w.Body.String()), headers: w.Header().Clone(), status: w.Code}
					if err != nil {
						got.failure = err.Error()
					}
					if result != nil {
						usage := result.Usage
						got.usage = &usage
						price := 0.000001
						group := &Group{ID: 1, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{result.Model}, BillingMode: BillingModeToken, InputPrice: &price, OutputPrice: &price}}}
						got.cost, e = svc.billingService.CalculateTokenCostForRequest(TokenCostRequest{Ctx: ctx, Model: result.Model, Group: group, Tokens: UsageTokens{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}, RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, svc.billingService)})
						require.NoError(t, e)
					}
					if !enabled {
						baseline = got
						continue
					}
					require.Equal(t, baseline, got, "observation preserves requests, response, SSE, usage and cost")
					wantCount := 1.0
					physical := 1
					if scenario == "build_failure" {
						wantCount = 0
						physical = 0
					}
					require.Len(t, upstream.requests, physical)
					all := forwardOpsGather(t, reg)
					require.Equal(t, wantCount, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "Done before Finalize")
					rec.AddBytes("resp_downstream", int64(w.Body.Len()), "complete")
					rec.Finalize(monitoring.FinalizeInput{Status: w.Code})
					all = forwardOpsGather(t, reg)
					require.Equal(t, wantCount, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
					require.Equal(t, wantCount, forwardOpsCounter(all, "sub2api_model_requests_total"))
					require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_gateway_requests_total"))
					wantResult, wantReason := "success", ""
					switch scenario {
					case "429":
						wantResult, wantReason = "upstream_error", "upstream_rate_limit"
					case "500":
						wantResult, wantReason = "upstream_error", "upstream_server_error"
					case "connect_timeout":
						wantResult, wantReason = "timeout", "upstream_connect_timeout"
					case "read_timeout", "read_timeout_SSE":
						wantResult, wantReason = "timeout", "upstream_read_timeout"
					case "server_cancel", "server_cancel_SSE":
						wantResult, wantReason = "upstream_error", "unknown"
					case "abort":
						wantResult, wantReason = "client_abort", "client_abort_during_response"
					case "400_code", "400_type", "404_code", "404_type":
						wantResult, wantReason = "upstream_error", "upstream_model_or_request"
					case "400_none", "404_none":
						wantResult, wantReason = "upstream_error", "unknown"
					case "build_failure":
						wantResult, wantReason = "gateway_error", "unknown"
					}
					require.Equal(t, wantResult, all["sub2api_gateway_requests_total"][0].labels["result"])
					require.Equal(t, wantReason, all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
					ttft := uint64(0)
					for _, m := range all["sub2api_upstream_ttft_seconds"] {
						ttft += m.count
					}
					wantTTFT := uint64(0)
					if (scenario == "200" || stream) && (!readFailure || strings.HasSuffix(scenario, "_SSE")) {
						wantTTFT = 1
					}
					require.Equal(t, wantTTFT, ttft)
					reqBytes := 0
					for _, r := range upstream.requests {
						reqBytes += len(r.body)
					}
					respBytes := len(payload)
					if scenario == "connect_timeout" || readFailure || scenario == "build_failure" {
						respBytes = 0
					}
					if strings.HasSuffix(scenario, "_SSE") {
						respBytes = len(payload)
					}
					wantBytes := map[string]float64{"in_wire": float64(len(input)), "req_decoded": float64(len(input)), "req_upstream": float64(reqBytes), "resp_upstream": float64(respBytes), "resp_downstream": float64(w.Body.Len())}
					for _, m := range all["sub2api_transfer_bytes_total"] {
						require.Equal(t, wantBytes[m.labels["direction"]], m.counter, m.labels["direction"])
						delete(wantBytes, m.labels["direction"])
					}
					for dir, n := range wantBytes {
						require.Zero(t, n, dir)
					}
					require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
					require.Equal(t, float64(len(input)), all["sub2api_request_body_bytes"][0].sum)
					phaseCount := 0
					total := 0.0
					for _, m := range all["sub2api_request_phase_seconds"] {
						if m.labels["phase"] != "headers_end" {
							phaseCount++
							total += m.sum
							require.GreaterOrEqual(t, m.sum, 0.0)
							require.EqualValues(t, 1, m.count)
						}
					}
					require.Equal(t, 6, phaseCount)
					require.LessOrEqual(t, total, terminal.Sub(entry).Seconds()+.002)
					outcome := "all_failed"
					if wantResult == "success" {
						outcome = "first_success"
					}
					require.Equal(t, outcome, all["sub2api_attempts_total"][0].labels["outcome"])
					t.Logf("M4/M5=%v result=%s/%s M11=%d bytes=%d/%d", wantCount, wantResult, wantReason, ttft, reqBytes, respBytes)
				}
			})
		}
	}
}

func normalizeBridgeOpsBody(body string) string {
	body = compatOpsNormalizeTimestamps(body)
	ids := map[string]string{}
	return regexp.MustCompile(`(?:chatcmpl[-_]|msg_)[0-9a-f]{24}`).ReplaceAllStringFunc(body, func(id string) string {
		if ids[id] == "" {
			ids[id] = fmt.Sprintf("generated_%d", len(ids))
		}
		return ids[id]
	})
}

type bridgeOpsCaptureUpstream struct {
	*forwardOpsUpstream
	attempts []*monitoring.OpsAttempt
}

func (u *bridgeOpsCaptureUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.attempts = append(u.attempts, monitoring.AttemptFromContext(req.Context()))
	return u.forwardOpsUpstream.Do(req, proxy, id, concurrency)
}
func TestOpenAIBridgeOpsInternalRetries(t *testing.T) {
	for _, path := range []string{"chat_responses", "messages_responses"} {
		t.Run(path, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
			account := bridgeOpsAccount(path)
			first := `{"error":{"code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
			input := []byte(`{"model":"gpt-4o","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
			finalPayload := bridgeOpsChatJSON
			if path == "messages_responses" {
				first = `{"error":{"type":"invalid_request_error","message":"previous_response_id is not available for this user"}}`
				finalPayload = responsesOpsSSE
				input = []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
			}
			upstream := &bridgeOpsCaptureUpstream{forwardOpsUpstream: &forwardOpsUpstream{responses: func(call int) *http.Response {
				status := 200
				body := finalPayload
				contentType := "application/json"
				if call == 1 {
					status = 400
					body = first
				} else if path == "messages_responses" {
					contentType = "text/event-stream"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
			}}}
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			if path == "messages_responses" {
				svc.bindOpenAICompatSessionResponseID(ctx, nil, account, "stable-cache-key", "resp_missing")
				_, err = svc.ForwardAsAnthropic(ctx, c, account, input, "stable-cache-key", "gpt-5.3-codex")
			} else {
				_, err = svc.ForwardAsChatCompletions(ctx, c, account, input, "", "")
			}
			require.NoError(t, err)
			require.Len(t, upstream.attempts, 2)
			require.NotNil(t, upstream.attempts[0])
			require.Same(t, upstream.attempts[0], upstream.attempts[1])
			all := forwardOpsGather(t, reg)
			require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "one Done for internal recursion/fallback")
			require.Equal(t, "success", all["sub2api_upstream_requests_total"][0].labels["result"])
			rec.Finalize(monitoring.FinalizeInput{Status: 200})
			all = forwardOpsGather(t, reg)
			require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
			require.EqualValues(t, 1, all["sub2api_upstream_ttft_seconds"][0].count)
			require.Equal(t, "first_success", all["sub2api_attempts_total"][0].labels["outcome"])
		})
	}
}
func TestOpenAIBridgeOpsPerInvocation(t *testing.T) {
	for _, path := range []string{"chat_responses", "chat_raw", "messages_responses", "messages_native"} {
		t.Run(path, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
			account := bridgeOpsAccount(path)
			upstream := &bridgeOpsCaptureUpstream{forwardOpsUpstream: &forwardOpsUpstream{responses: func(int) *http.Response {
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error"}}`))}
			}}}
			cfg := &config.Config{}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}}
			for i := 0; i < 2; i++ {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
				_, err = bridgeOpsInvoke(svc, ctx, c, account, path, []byte(`{"model":"gpt-4o","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
				require.Error(t, err)
				require.Len(t, upstream.attempts, i+1)
				require.NotNil(t, upstream.attempts[i])
				ctx = monitoring.WithAttempt(ctx, upstream.attempts[i])
			}
			require.NotSame(t, upstream.attempts[0], upstream.attempts[1])
			require.Equal(t, 2.0, forwardOpsCounter(forwardOpsGather(t, reg), "sub2api_upstream_requests_total"))
			rec.Finalize(monitoring.FinalizeInput{Status: 429})
			all := forwardOpsGather(t, reg)
			require.Equal(t, 2.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
			require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_gateway_requests_total"))
		})
	}
}
