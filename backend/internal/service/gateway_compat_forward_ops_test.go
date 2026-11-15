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
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestGatewayCompatForwardOpsMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	const model = "claude-3-5-sonnet-latest"
	const jsonResponse = `{"id":"msg_fixed","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":11,"output_tokens":5}}`
	const sse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixed\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet-latest\",\"content\":[],\"usage\":{\"input_tokens\":11}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, path := range []string{"chat", "responses", "passthrough"} {
		for _, scenario := range []string{"200", "SSE", "SSE_code_only", "SSE_code_and_type", "SSE_type_only", "SSE_no_code", "SSE_buffered_code_only", "SSE_buffered_type_only", "SSE_buffered_no_code", "429", "500", "connect_timeout", "read_timeout", "abort", "signature_400", "build_failure", "same_account_retry", "400_code_only", "400_type_only", "400_no_code", "400_code_and_type", "404_code_only", "404_type_only", "404_no_code", "404_code_and_type"} {
			if path == "passthrough" && strings.Contains(scenario, "buffered") {
				continue
			}
			t.Run(path+"/"+scenario, func(t *testing.T) {
				type snapshot struct {
					body     string
					headers  http.Header
					status   int
					requests []forwardOpsSnapshot
					usage    *ClaudeUsage
					cost     *CostBreakdown
					failure  string
				}
				var baseline snapshot
				for _, enabled := range []bool{false, true} {
					clientStream := (strings.HasPrefix(scenario, "SSE") && !strings.Contains(scenario, "buffered")) || scenario == "abort"
					input := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, clientStream)
					if path == "responses" {
						input = fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello"}`, model, clientStream)
					}
					reg := prometheus.NewRegistry()
					metrics, err := monitoring.NewOpsMetrics(reg)
					require.NoError(t, err)
					ctx := context.Background()
					var rec *monitoring.OpsRecorder
					if enabled {
						ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/messages", metrics)
						rec.AttachMeta(monitoring.OpsRequestMeta{Model: model})
						rec.MarkBodyRead(int64(len(input)), int64(len(input)), "complete")
						rec.AddBytes("req_decoded", int64(len(input)), "complete")
					}
					writer := httptest.NewRecorder()
					var downstream http.ResponseWriter = writer
					if scenario == "abort" {
						downstream = &compatOpsFailWriter{writer}
					}
					c, _ := gin.CreateTestContext(downstream)
					c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(input)).WithContext(ctx)
					c.Request.Header.Set("X-Ops-Test", "preserved")
					account := newAnthropicAPIKeyAccountForTest()
					if path != "passthrough" {
						account.Extra = nil
					}
					if scenario == "build_failure" {
						account.Credentials["base_url"] = "://bad-url"
					}
					if scenario == "same_account_retry" {
						account.Credentials["custom_error_codes_enabled"] = true
						account.Credentials["custom_error_codes"] = []any{float64(400)}
					}
					upstream := &forwardOpsUpstream{}
					if scenario == "connect_timeout" {
						upstream.failure = &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
					}
					responseBytes := 0
					upstream.responses = func(call int) *http.Response {
						status := 200
						payload := sse
						if path == "passthrough" && scenario != "SSE" && scenario != "abort" {
							payload = jsonResponse
						}
						switch scenario {
						case "429":
							status = 429
						case "500":
							status = 500
						case "signature_400":
							status = 400
						}
						if scenario == "same_account_retry" && call == 1 {
							status = 500
						}
						if status >= 400 {
							payload = `{"error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`
						}
						if strings.HasPrefix(scenario, "400_") || strings.HasPrefix(scenario, "404_") {
							status = 400
							if strings.HasPrefix(scenario, "404_") {
								status = 404
							}
							switch {
							case strings.HasSuffix(scenario, "code_only"):
								payload = `{"error":{"code":"model_not_found","message":"missing model"}}`
							case strings.HasSuffix(scenario, "type_only"):
								payload = `{"error":{"type":"invalid_request_error","message":"invalid model"}}`
							case strings.HasSuffix(scenario, "code_and_type"):
								payload = `{"error":{"code":"model_not_found","type":"invalid_request_error","message":"invalid model"}}`
							default:
								payload = `{"error":{"message":"failure without code"}}`
							}
						}
						var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
						if strings.HasPrefix(scenario, "SSE_") {
							errorBody := `{"message":"fixture failure"}`
							if strings.HasSuffix(scenario, "code_only") {
								errorBody = `{"code":"model_not_found","message":"fixture failure"}`
							}
							if scenario == "SSE_code_and_type" {
								errorBody = `{"code":"model_not_found","type":"api_error","message":"fixture failure"}`
							}
							if strings.HasSuffix(scenario, "type_only") {
								errorBody = `{"type":"invalid_request_error","message":"fixture failure"}`
							}
							payload = "event: error\ndata: {\"type\":\"error\",\"error\":" + errorBody + "}\n\n"
							body = io.NopCloser(strings.NewReader(payload))
						}
						if scenario == "read_timeout" {
							body = &streamReadCloser{err: os.ErrDeadlineExceeded}
							payload = ""
						}
						responseBytes += len(payload)
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"ops-fixed"}}, Body: body}
					}
					svc := newForwardPartialUsageServiceForTest(nil)
					svc.httpUpstream = upstream
					svc.rateLimitService = &RateLimitService{cfg: svc.cfg, accountRepo: &forwardOpsAccountRepo{}}
					svc.billingService = NewBillingService(svc.cfg, nil)
					parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(input)), PlatformAnthropic)
					require.NoError(t, err)
					var result *ForwardResult
					var forwardErr error
					switch path {
					case "chat":
						result, forwardErr = svc.ForwardAsChatCompletions(ctx, c, account, []byte(input), parsed)
					case "responses":
						result, forwardErr = svc.ForwardAsResponses(ctx, c, account, []byte(input), parsed)
					case "passthrough":
						result, forwardErr = svc.Forward(ctx, c, account, parsed)
					}
					got := snapshot{body: writer.Body.String(), headers: writer.Header().Clone(), status: writer.Code, requests: upstream.requests}
					if forwardErr != nil {
						got.failure = forwardErr.Error()
					}
					if result != nil {
						usage := result.Usage
						got.usage = &usage
						got.cost = svc.calculateRecordUsageCost(ctx, result, &APIKey{}, result.Model, 1, 1, time.Time{})
					}
					// Conversion responses contain server timestamps; compare their remaining
					// bytes exactly after replacing only generated timestamp fields.
					got.body = compatOpsNormalizeTimestamps(got.body)
					if !enabled {
						baseline = got
						continue
					}
					require.Equal(t, baseline, got, "observation must preserve requests, headers, response, SSE, errors, usage and cost")
					all := forwardOpsGather(t, reg)
					count := 1.0
					if scenario == "build_failure" {
						count = 0
					}
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "settle before Finalize")
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_model_requests_total"))
					physical := 1
					if scenario == "build_failure" {
						physical = 0
					}
					if scenario == "same_account_retry" && path == "passthrough" {
						physical = 2
					}
					require.Len(t, upstream.requests, physical)
					rec.AddBytes("resp_downstream", int64(writer.Body.Len()), "complete")
					rec.Finalize(monitoring.FinalizeInput{Status: writer.Code})
					all = forwardOpsGather(t, reg)
					require.Equal(t, count, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
					wantResult, wantReason := "success", ""
					switch scenario {
					case "429":
						wantResult, wantReason = "upstream_error", "upstream_rate_limit"
					case "500":
						wantResult, wantReason = "upstream_error", "upstream_server_error"
					case "connect_timeout":
						wantResult, wantReason = "timeout", "upstream_connect_timeout"
					case "read_timeout":
						wantResult, wantReason = "timeout", "upstream_read_timeout"
					case "abort":
						wantResult, wantReason = "client_abort", "client_abort_during_response"
					case "signature_400":
						wantResult, wantReason = "upstream_error", "upstream_model_or_request"
					case "SSE_code_only", "SSE_code_and_type", "SSE_type_only", "SSE_buffered_code_only", "SSE_buffered_type_only":
						wantResult, wantReason = "upstream_error", "upstream_model_or_request"
					case "SSE_no_code", "SSE_buffered_no_code":
						wantResult, wantReason = "upstream_error", "unknown"
					case "build_failure":
						wantResult, wantReason = "gateway_error", "unknown"
					case "same_account_retry":
						if path != "passthrough" {
							wantResult, wantReason = "upstream_error", "upstream_server_error"
						}
					}
					if strings.HasPrefix(scenario, "400_") || strings.HasPrefix(scenario, "404_") {
						wantResult, wantReason = "upstream_error", "upstream_model_or_request"
						if strings.HasSuffix(scenario, "no_code") {
							wantReason = "unknown"
						}
					}
					require.Equal(t, wantResult, all["sub2api_gateway_requests_total"][0].labels["result"])
					require.Equal(t, wantReason, all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
					outcome := "all_failed"
					if wantResult == "success" {
						outcome = "first_success"
					}
					require.Equal(t, outcome, all["sub2api_attempts_total"][0].labels["outcome"])
					if count > 0 {
						require.Equal(t, wantResult, all["sub2api_upstream_requests_total"][0].labels["result"])
						require.Equal(t, wantReason, all["sub2api_upstream_requests_total"][0].labels["failure_reason"])
					}
					reqBytes := 0
					for _, r := range upstream.requests {
						reqBytes += len(r.body)
					}
					bytes := map[string]float64{"in_wire": float64(len(input)), "req_decoded": float64(len(input)), "req_upstream": float64(reqBytes), "resp_upstream": float64(responseBytes), "resp_downstream": float64(writer.Body.Len())}
					for _, m := range all["sub2api_transfer_bytes_total"] {
						require.Equal(t, bytes[m.labels["direction"]], m.counter, m.labels["direction"])
						delete(bytes, m.labels["direction"])
					}
					for dir, n := range bytes {
						require.Zero(t, n, dir)
					}
					ttft := uint64(0)
					for _, m := range all["sub2api_upstream_ttft_seconds"] {
						ttft += m.count
					}
					wantTTFT := uint64(0)
					if scenario == "200" || strings.HasPrefix(scenario, "SSE") || scenario == "abort" || (scenario == "same_account_retry" && path == "passthrough") {
						wantTTFT = 1
					}
					require.Equal(t, wantTTFT, ttft)
					require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
					phases := 0
					for _, m := range all["sub2api_request_phase_seconds"] {
						if m.labels["phase"] != "headers_end" {
							phases++
							require.EqualValues(t, 1, m.count)
							require.GreaterOrEqual(t, m.sum, 0.0)
						}
					}
					require.Equal(t, 6, phases)
					t.Logf("physical=%d M4/M5=%v result=%s/%s M11=%d", physical, count, wantResult, wantReason, ttft)
				}
			})
		}
	}
}

type compatOpsFailWriter struct{ *httptest.ResponseRecorder }

func (*compatOpsFailWriter) Write([]byte) (int, error)       { return 0, syscall.EPIPE }
func (*compatOpsFailWriter) WriteString(string) (int, error) { return 0, syscall.EPIPE }

func compatOpsNormalizeTimestamps(body string) string {
	ids := map[string]string{}
	body = regexp.MustCompile(`item_[0-9a-f]{24}`).ReplaceAllStringFunc(body, func(id string) string {
		if ids[id] == "" {
			ids[id] = fmt.Sprintf("item_%024d", len(ids))
		}
		return ids[id]
	})
	// Preserve JSON and SSE framing while normalizing only generated timestamp numbers.
	for _, field := range []string{"created", "created_at", "completed_at"} {
		marker := `"` + field + `":`
		offset := 0
		for {
			at := strings.Index(body[offset:], marker)
			if at < 0 {
				break
			}
			start := offset + at + len(marker)
			end := start
			for end < len(body) && ((body[end] >= '0' && body[end] <= '9') || body[end] == '.') {
				end++
			}
			if end > start {
				body = body[:start] + "0" + body[end:]
				offset = start + 1
			} else {
				offset = start
			}
		}
	}
	return body
}

// A handler retry invokes the application forwarder again. Each invocation must
// allocate a fresh attempt even if the incoming context contains an old handle.
func TestGatewayCompatForwardOpsPerInvocation(t *testing.T) {
	for _, path := range []string{"chat", "responses", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
			upstream := &compatOpsCaptureUpstream{forwardOpsUpstream: forwardOpsUpstream{responses: func(int) *http.Response {
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`))}
			}}}
			svc := newForwardPartialUsageServiceForTest(nil)
			svc.httpUpstream = upstream
			svc.rateLimitService = &RateLimitService{cfg: svc.cfg, accountRepo: &forwardOpsAccountRepo{}}
			account := newAnthropicAPIKeyAccountForTest()
			if path != "passthrough" {
				account.Extra = nil
			}
			for i := 0; i < 2; i++ {
				body := []byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}]}`)
				if path == "responses" {
					body = []byte(`{"model":"claude-3-5-sonnet-latest","input":"hello"}`)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
				parsed, e := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, e)
				switch path {
				case "chat":
					_, e = svc.ForwardAsChatCompletions(ctx, c, account, body, parsed)
				case "responses":
					_, e = svc.ForwardAsResponses(ctx, c, account, body, parsed)
				case "passthrough":
					_, e = svc.Forward(ctx, c, account, parsed)
				}
				require.Error(t, e)
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
			require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_attempts_total"))
		})
	}
}

type compatOpsCaptureUpstream struct {
	forwardOpsUpstream
	attempts []*monitoring.OpsAttempt
}

func (u *compatOpsCaptureUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.attempts = append(u.attempts, monitoring.AttemptFromContext(req.Context()))
	return u.forwardOpsUpstream.DoWithTLS(req, proxy, id, concurrency, profile)
}

func TestCompatOpsUpstreamCodeEvidence(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"code", `{"error":{"code":"model_not_found"}}`, "model_not_found"},
		{"type", `{"error":{"type":"invalid_request_error"}}`, "invalid_request_error"},
		{"neither", `{"error":{"message":"failure"}}`, ""},
		{"code_priority", `{"error":{"code":"model_not_found","type":"invalid_request_error"}}`, "model_not_found"},
		{"blank_code", `{"error":{"code":" ","type":"invalid_request_error"}}`, "invalid_request_error"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, compatOpsUpstreamCode([]byte(tc.body))) })
	}
}

func TestCompatOpsErrorEvidenceUsesOnlyConsumerReads(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx := context.Background()
			var rec *monitoring.OpsRecorder
			if enabled {
				ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/messages", metrics)
			}
			ops := newCompatForwardOps(ctx, nil, newAnthropicAPIKeyAccountForTest())
			payload := `{"error":{"type":"invalid_request_error"}}`
			source := &compatOpsCountingErrorBody{payload: payload}
			resp := &http.Response{StatusCode: 400, Body: source}
			businessFailure := &UpstreamFailoverError{StatusCode: 400, ResponseBody: []byte(payload)}
			businessResult := &ForwardResult{Model: "unchanged"}
			result, failure := ops.consumeHTTPError(resp, func() (*ForwardResult, error) {
				require.Zero(t, source.reads, "installing evidence capture must not read")
				p := make([]byte, len(payload))
				n, e := resp.Body.Read(p)
				require.NoError(t, e)
				require.Equal(t, payload, string(p[:n]))
				require.NoError(t, resp.Body.Close())
				return businessResult, businessFailure
			})
			require.Same(t, businessResult, result)
			require.Same(t, businessFailure, failure, "business failover returned unchanged")
			require.Equal(t, 1, source.reads, "evidence extraction must not read again")
			require.Equal(t, 1, source.closes)
			if enabled {
				rec.Finalize(monitoring.FinalizeInput{Status: 400})
				all := forwardOpsGather(t, reg)
				require.Equal(t, "upstream_model_or_request", all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
			}
		})
	}
}

type compatOpsCountingErrorBody struct {
	payload       string
	reads, closes int
}

func (b *compatOpsCountingErrorBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reads > 1 {
		return 0, io.EOF
	}
	return copy(p, b.payload), nil
}
func (b *compatOpsCountingErrorBody) Close() error { b.closes++; return nil }
