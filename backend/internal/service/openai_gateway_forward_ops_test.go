package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/gin-gonic/gin"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

const responsesOpsJSON = `{"id":"resp_fixed","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg_fixed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}`
const responsesOpsSSE = "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_fixed\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":" + responsesOpsJSON + "}\n\ndata: [DONE]\n\n"

type responsesOpsPlugin struct {
	pluginv1.UnimplementedTransportPluginServer
	mu       sync.Mutex
	status   int
	payload  string
	requests []forwardOpsSnapshot
}

func (p *responsesOpsPlugin) Forward(stream pluginv1.TransportPlugin_ForwardServer) error {
	var body strings.Builder
	var headers http.Header
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if start := frame.GetStart(); start != nil {
			headers = headersFromPlugin(start.Headers)
		}
		body.Write(frame.GetBodyChunk())
		if frame.GetBodyEnd() {
			break
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, forwardOpsSnapshot{body.String(), headers})
	p.mu.Unlock()
	if p.payload == "@headers_timeout" {
		return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "UPSTREAM_TIMEOUT", Message: "net/http: timeout awaiting response headers", RequestSent: true}}})
	}
	if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: int32(p.status), Headers: headersToPlugin(http.Header{"Content-Type": []string{"text/event-stream"}})}}}); err != nil {
		return err
	}
	if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: []byte(p.payload)}}); err != nil {
		return err
	}
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{}}})
}
func newResponsesOpsPlugin(t *testing.T, status int, payload string) (*PluginManager, *responsesOpsPlugin) {
	t.Helper()
	impl := &responsesOpsPlugin{status: status, payload: payload}
	api := dispenseTransportClient(t, impl)
	runtime := &pluginRuntime{client: hcplugin.NewClient(&hcplugin.ClientConfig{}), api: api, done: make(chan struct{})}
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, runtime: runtime})
	return manager, impl
}

func TestOpenAIResponsesOpsMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	for _, path := range []string{"native", "passthrough", "plugin", "plugin_false"} {
		for _, scenario := range []string{"200", "SSE", "json_write_abort", "json_write_reset", "429", "500", "timeout", "read_timeout", "abort", "build_failure", "sse_error", "rejected_field_retry"} {
			if path == "plugin" && (scenario == "read_timeout" || scenario == "rejected_field_retry" || scenario == "build_failure") {
				continue
			}
			t.Run(path+"/"+scenario, func(t *testing.T) {
				type snapshot struct {
					requests []forwardOpsSnapshot
					response string
					status   int
					headers  http.Header
					usage    *OpenAIUsage
					cost     *CostBreakdown
					failure  string
				}
				var baseline snapshot
				for _, enabled := range []bool{false, true} {
					reg := prometheus.NewRegistry()
					metrics, e := monitoring.NewOpsMetrics(reg)
					require.NoError(t, e)
					entryBefore := time.Now()
					ctx := context.Background()
					var rec *monitoring.OpsRecorder
					stream := scenario == "SSE" || scenario == "abort" || scenario == "sse_error"
					input := fmt.Sprintf(`{"model":"gpt-4o","stream":%t,"instructions":"test","input":"hello"}`, stream)
					if scenario == "rejected_field_retry" {
						input = strings.TrimSuffix(input, "}") + `,"truncation":"auto"}`
					}
					if enabled {
						ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/responses", metrics)
						rec.AttachMeta(monitoring.OpsRequestMeta{Model: "gpt-4o"})
						rec.MarkBodyRead(int64(len(input)), int64(len(input)), "complete")
						rec.AddBytes("req_decoded", int64(len(input)), "complete")
					}
					writer := httptest.NewRecorder()
					var w http.ResponseWriter = writer
					if scenario == "abort" || scenario == "json_write_abort" {
						w = &compatOpsFailWriter{writer}
					}
					if scenario == "json_write_reset" {
						w = &responsesOpsResetWriter{writer}
					}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(input)).WithContext(ctx)
					c.Request.Header.Set("User-Agent", "ops-contract-test")
					account := &Account{ID: 89, Name: "ops-openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_responses_supported": true, "openai_passthrough": path == "passthrough"}, Concurrency: 1}
					status := 200
					payload := responsesOpsJSON
					if stream {
						payload = responsesOpsSSE
					}
					if scenario == "429" {
						status = 429
					}
					if scenario == "500" {
						status = 500
					}
					if status >= 400 {
						payload = `{"error":{"type":"api_error","message":"upstream error"}}`
					}
					if scenario == "sse_error" {
						payload = "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixed\",\"error\":{\"code\":\"api_error\",\"message\":\"failed\"}}}\n\n"
					}
					upstream := &forwardOpsUpstream{responses: func(call int) *http.Response {
						responseStatus, responsePayload := status, payload
						if scenario == "rejected_field_retry" && call == 1 {
							responseStatus = 400
							responsePayload = `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: truncation.","param":"truncation"}}`
						}
						var body io.ReadCloser = io.NopCloser(strings.NewReader(responsePayload))
						if scenario == "read_timeout" {
							body = &streamReadCloser{err: os.ErrDeadlineExceeded}
						}
						return &http.Response{StatusCode: responseStatus, Header: http.Header{"Content-Type": []string{map[bool]string{true: "text/event-stream", false: "application/json"}[stream]}}, Body: body}
					}}
					if scenario == "timeout" {
						upstream.failure = &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
					}
					cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}, deferredService: &DeferredService{}}
					svc.billingService = NewBillingService(cfg, nil)
					var plugin *responsesOpsPlugin
					if path == "plugin" {
						account.Type = AccountTypeOAuth
						account.Credentials = map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}
						pluginPayload := payload
						if scenario == "timeout" {
							pluginPayload = "@headers_timeout"
						}
						svc.pluginManager, plugin = newResponsesOpsPlugin(t, status, pluginPayload)
					}
					if path == "plugin_false" {
						svc.pluginManager = &PluginManager{}
					}
					if scenario == "build_failure" {
						account.Credentials["base_url"] = "://bad-url"
						if path == "plugin" {
							continue
						}
					}
					forwardBefore := time.Now()
					result, err := svc.Forward(ctx, c, account, []byte(input))
					if scenario == "json_write_abort" || scenario == "json_write_reset" {
						require.NoError(t, err, "business return remains successful")
						require.NotNil(t, result)
						require.NoError(t, c.Request.Context().Err(), "client ctx has not canceled")
						require.NotEmpty(t, c.Errors, "Gin records the failed JSON/Data write")
					}
					terminalAfter := time.Now()
					requests := upstream.requests
					if plugin != nil {
						require.Empty(t, upstream.requests, "handled plugin must bypass repository")
						plugin.mu.Lock()
						requests = append([]forwardOpsSnapshot(nil), plugin.requests...)
						plugin.mu.Unlock()
					}
					got := snapshot{requests: requests, response: compatOpsNormalizeTimestamps(writer.Body.String()), status: writer.Code, headers: writer.Header().Clone()}
					if err != nil {
						got.failure = err.Error()
					}
					if result != nil {
						usage := result.Usage
						got.usage = &usage
						price := 0.000001
						group := &Group{ID: 1, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{result.Model}, BillingMode: BillingModeToken, InputPrice: &price, OutputPrice: &price}}}
						got.cost, e = svc.billingService.CalculateTokenCostForRequest(TokenCostRequest{Ctx: ctx, Model: result.Model, Group: group, Tokens: UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, svc.billingService)})
						require.NoError(t, e)
					}
					if !enabled {
						baseline = got
						continue
					}
					require.Equal(t, baseline, got, "observation preserves request/response/SSE/error/usage/cost")
					count := 1.0
					if scenario == "build_failure" {
						count = 0
					}
					all := forwardOpsGather(t, reg)
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "Done before Finalize")
					rec.AddBytes("resp_downstream", int64(writer.Body.Len()), "complete")
					rec.Finalize(monitoring.FinalizeInput{Status: writer.Code})
					all = forwardOpsGather(t, reg)
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_model_requests_total"))
					require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_gateway_requests_total"))
					wantResult, wantReason := "success", ""
					switch scenario {
					case "429":
						wantResult, wantReason = "upstream_error", "upstream_rate_limit"
					case "500":
						wantResult, wantReason = "upstream_error", "upstream_server_error"
					case "timeout":
						wantResult, wantReason = "timeout", "upstream_connect_timeout"
						if path == "plugin" {
							wantReason = "upstream_read_timeout"
						}
					case "read_timeout":
						wantResult, wantReason = "timeout", "upstream_read_timeout"
					case "abort", "json_write_abort", "json_write_reset":
						wantResult, wantReason = "client_abort", "client_abort_during_response"
					case "build_failure":
						wantResult, wantReason = "gateway_error", "unknown"
					case "sse_error":
						wantResult, wantReason = "upstream_error", "unknown"
					}
					require.Equal(t, wantResult, all["sub2api_gateway_requests_total"][0].labels["result"])
					require.Equal(t, wantReason, all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
					ttft := uint64(0)
					for _, m := range all["sub2api_upstream_ttft_seconds"] {
						ttft += m.count
					}
					wantTTFT := uint64(0)
					if scenario == "200" || stream || scenario == "rejected_field_retry" || scenario == "json_write_abort" || scenario == "json_write_reset" {
						wantTTFT = 1
					}
					require.Equal(t, wantTTFT, ttft)
					wantPhysical := 1
					if scenario == "build_failure" {
						wantPhysical = 0
					}
					if scenario == "rejected_field_retry" {
						wantPhysical = 2
					}
					require.Len(t, requests, wantPhysical)
					if scenario == "rejected_field_retry" {
						require.NotEqual(t, requests[0].body, requests[1].body)
					}
					reqBytes := 0
					for _, r := range requests {
						reqBytes += len(r.body)
					}
					respBytes := len(payload)
					if scenario == "rejected_field_retry" {
						respBytes += len(`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: truncation.","param":"truncation"}}`)
					}
					if scenario == "timeout" || scenario == "read_timeout" || scenario == "build_failure" {
						respBytes = 0
					}
					bytes := map[string]float64{"in_wire": float64(len(input)), "req_decoded": float64(len(input)), "req_upstream": float64(reqBytes), "resp_upstream": float64(respBytes), "resp_downstream": float64(writer.Body.Len())}
					for _, m := range all["sub2api_transfer_bytes_total"] {
						require.Equal(t, bytes[m.labels["direction"]], m.counter, m.labels["direction"])
						delete(bytes, m.labels["direction"])
					}
					for dir, n := range bytes {
						require.Zero(t, n, dir)
					}
					phases := 0
					totalDuration := 0.0
					for _, m := range all["sub2api_request_phase_seconds"] {
						if m.labels["phase"] != "headers_end" {
							phases++
							totalDuration += m.sum
							require.GreaterOrEqual(t, m.sum, 0.0)
							require.EqualValues(t, 1, m.count)
						}
					}
					require.Equal(t, 6, phases)
					require.GreaterOrEqual(t, totalDuration, forwardBefore.Sub(entryBefore).Seconds()-.002)
					require.LessOrEqual(t, totalDuration, terminalAfter.Sub(entryBefore).Seconds()+.002)
					require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
					outcome := "all_failed"
					if wantResult == "success" {
						outcome = "first_success"
					}
					require.Equal(t, outcome, all["sub2api_attempts_total"][0].labels["outcome"])
					t.Logf("M4/M5=%v result=%s/%s M11=%d req/resp=%d/%d", count, wantResult, wantReason, ttft, reqBytes, respBytes)
				}
			})
		}
	}
}

type responsesOpsCaptureUpstream struct {
	forwardOpsUpstream
	attempts []*monitoring.OpsAttempt
}

func (u *responsesOpsCaptureUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.attempts = append(u.attempts, monitoring.AttemptFromContext(req.Context()))
	return u.forwardOpsUpstream.Do(req, proxy, id, concurrency)
}
func TestOpenAIResponsesOpsPerInvocation(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/responses", metrics)
			cfg := &config.Config{}
			upstream := &responsesOpsCaptureUpstream{forwardOpsUpstream: forwardOpsUpstream{responses: func(int) *http.Response {
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error"}}`))}
			}}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{cfg: cfg, accountRepo: &forwardOpsAccountRepo{}}, deferredService: &DeferredService{}}
			account := &Account{ID: 89, Name: "ops", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_responses_supported": true, "openai_passthrough": passthrough}}
			for i := 0; i < 2; i++ {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				_, err = svc.Forward(ctx, c, account, []byte(`{"model":"gpt-4o","input":"hello"}`))
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
			require.Equal(t, 2.0, forwardOpsCounter(all, "sub2api_model_requests_total"))
			require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_gateway_requests_total"))
		})
	}
}

func TestOpenAIResponsesOpsPanicDoesNotPublishSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(reg)
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/responses", metrics)
	cfg := &config.Config{}
	upstream := &forwardOpsUpstream{responses: func(int) *http.Response { panic("business transport panic") }}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{ID: 89, Name: "ops", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_responses_supported": true}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	require.Panics(t, func() { _, _ = svc.Forward(ctx, c, account, []byte(`{"model":"gpt-4o","input":"hello"}`)) })
	require.Zero(t, forwardOpsCounter(forwardOpsGather(t, reg), "sub2api_upstream_requests_total"), "panic is left to Recovery/Finalize, never success Done")
	rec.Finalize(monitoring.FinalizeInput{Status: 500, Panic: true})
	all := forwardOpsGather(t, reg)
	require.Equal(t, "gateway_error", all["sub2api_gateway_requests_total"][0].labels["result"])
	require.Equal(t, "gateway_panic", all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
	require.Empty(t, all["sub2api_upstream_ttft_seconds"])
}

type responsesOpsResetWriter struct{ *httptest.ResponseRecorder }

func (*responsesOpsResetWriter) Write([]byte) (int, error)       { return 0, syscall.ECONNRESET }
func (*responsesOpsResetWriter) WriteString(string) (int, error) { return 0, syscall.ECONNRESET }

func TestOpenAIResponsesOpsSemanticOutputTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(reg)
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/responses", metrics)
	pr, pw := io.Pipe()
	defer pw.Close()
	// The transport returns headers immediately; the body blocks until the
	// semantic-output timer closes it. No payload bytes arrive before timeout.
	upstream := &forwardOpsUpstream{responses: func(int) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"timeout-fixed"}}, Body: pr}
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, OpenAIFirstOutputTimeoutSeconds: 1}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{ID: 89, Name: "ops-timeout", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_responses_supported": true}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.5","stream":true,"reasoning":{"effort":"low"},"input":"hello"}`))
	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 504, failover.StatusCode, "business failover status is unchanged")
	require.Contains(t, string(failover.ResponseBody), "first_output_timeout")
	require.True(t, failover.SafeToFailoverAfterWrite)
	require.Empty(t, w.Body.String())
	require.NoError(t, c.Request.Context().Err(), "upstream timeout must not cancel client ctx")
	all := forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "explicit timeout settlement before Finalize")
	require.Equal(t, "timeout", all["sub2api_upstream_requests_total"][0].labels["result"])
	require.Equal(t, "upstream_read_timeout", all["sub2api_upstream_requests_total"][0].labels["failure_reason"])
	require.Empty(t, all["sub2api_client_abort_total"])
	require.Empty(t, all["sub2api_upstream_ttft_seconds"])
	rec.Finalize(monitoring.FinalizeInput{Status: 504})
	all = forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
	require.Equal(t, "timeout", all["sub2api_gateway_requests_total"][0].labels["result"])
	require.Equal(t, "upstream_read_timeout", all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
}
