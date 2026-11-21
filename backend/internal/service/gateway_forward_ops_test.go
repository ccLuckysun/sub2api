package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type forwardOpsSettings struct{ betaPolicySettingRepoStub }

func (r *forwardOpsSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

type forwardOpsAccountRepo struct {
	gatewayForwardErrorPolicyRepoStub
}

func (*forwardOpsAccountRepo) SetRateLimited(context.Context, int64, time.Time) error { return nil }
func (*forwardOpsAccountRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}

type forwardOpsSnapshot struct {
	body    string
	headers http.Header
}
type forwardOpsUpstream struct {
	responses        func(int) *http.Response
	failure          error
	client           *http.Client
	transportFailure error
	requests         []forwardOpsSnapshot
}

func (u *forwardOpsUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	a := monitoring.AttemptFromContext(req.Context())
	rec := monitoring.RecorderFromContext(req.Context())
	var handle *monitoring.OpsResponse
	if a != nil {
		handle = a.Sent()
		monitoring.ObserveHTTPRequest(rec, req)
	}
	if u.client != nil {
		u.requests = append(u.requests, forwardOpsSnapshot{headers: req.Header.Clone()})
		resp, err := u.client.Do(req)
		u.transportFailure = err
		if err != nil {
			a.BodyDoneFor(handle, err)
			return resp, err
		}
		a.NoteHeadersFor(handle, resp.StatusCode)
		if a != nil {
			resp.Body = monitoring.ObserveResponseBody(rec, a, handle, resp.Body)
		}
		return resp, nil
	}
	if u.failure != nil {
		a.BodyDoneFor(handle, u.failure)
		u.requests = append(u.requests, forwardOpsSnapshot{headers: req.Header.Clone()})
		return nil, u.failure
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	u.requests = append(u.requests, forwardOpsSnapshot{string(body), req.Header.Clone()})
	resp := u.responses(len(u.requests))
	a.NoteHeadersFor(handle, resp.StatusCode)
	if a != nil {
		resp.Body = monitoring.ObserveResponseBody(rec, a, handle, resp.Body)
	}
	return resp, nil
}
func (u *forwardOpsUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type forwardOpsMetric struct {
	labels       map[string]string
	count        uint64
	sum, counter float64
}

func forwardOpsGather(t *testing.T, reg *prometheus.Registry) map[string][]forwardOpsMetric {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	all := map[string][]forwardOpsMetric{}
	for _, f := range families {
		for _, m := range f.Metric {
			v := forwardOpsMetric{labels: map[string]string{}}
			for _, l := range m.Label {
				v.labels[l.GetName()] = l.GetValue()
			}
			if f.GetType() == dto.MetricType_HISTOGRAM {
				v.count = m.Histogram.GetSampleCount()
				v.sum = m.Histogram.GetSampleSum()
			} else if f.GetType() == dto.MetricType_COUNTER {
				v.counter = m.Counter.GetValue()
			}
			all[f.GetName()] = append(all[f.GetName()], v)
		}
	}
	return all
}
func forwardOpsCounter(all map[string][]forwardOpsMetric, name string) float64 {
	sum := 0.0
	for _, m := range all[name] {
		sum += m.counter
	}
	return sum
}

type forwardOpsFailWriter struct{ *httptest.ResponseRecorder }

func (w *forwardOpsFailWriter) Write([]byte) (int, error)       { return 0, syscall.EPIPE }
func (w *forwardOpsFailWriter) WriteString(string) (int, error) { return 0, syscall.EPIPE }

type forwardOpsCancelBody struct{ cancel context.CancelFunc }

func (b *forwardOpsCancelBody) Read([]byte) (int, error) { b.cancel(); return 0, context.Canceled }
func (b *forwardOpsCancelBody) Close() error             { return nil }

func TestGatewayForwardOpsScenarioMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	jsonBody := `{"id":"msg_ops","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":11,"output_tokens":5}}`
	sseBody := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ops\",\"model\":\"claude-3-5-sonnet-latest\",\"usage\":{\"input_tokens\":11}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct {
		name             string
		status           int
		stream           bool
		failure          error
		readFailure      error
		abort            bool
		signature        bool
		semanticError    bool
		ordinarySSEError bool
		result, reason   string
		ttft             int
		physical         int
	}{
		{name: "200_JSON", status: 200, result: "success", ttft: 1, physical: 1},
		{name: "200_SSE", status: 200, stream: true, result: "success", ttft: 1, physical: 1},
		{name: "429", status: 429, result: "upstream_error", reason: "upstream_rate_limit", physical: 1},
		{name: "500", status: 500, result: "upstream_error", reason: "upstream_server_error", physical: 1},
		{name: "502", status: 502, result: "upstream_error", reason: "upstream_server_error", physical: 1},
		{name: "connect_timeout", failure: &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}, result: "timeout", reason: "upstream_connect_timeout", physical: 1},
		{name: "connection_refused", failure: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, result: "upstream_error", reason: "unknown", physical: 1},
		{name: "read_timeout", status: 200, readFailure: os.ErrDeadlineExceeded, result: "timeout", reason: "upstream_read_timeout", physical: 1},
		{name: "200_SSE_read_timeout", status: 200, stream: true, readFailure: os.ErrDeadlineExceeded, result: "timeout", reason: "upstream_read_timeout", ttft: 1, physical: 1},
		{name: "client_abort", status: 200, abort: true, result: "client_abort", reason: "client_abort_during_response", physical: 1},
		{name: "SSE_client_write_abort", status: 200, stream: true, abort: true, result: "client_abort", reason: "client_abort_during_response", ttft: 1, physical: 1},
		{name: "200_SSE_error_event", status: 200, stream: true, semanticError: true, result: "upstream_error", reason: "upstream_rate_limit", ttft: 1, physical: 1},
		{name: "200_SSE_ordinary_error", status: 200, stream: true, ordinarySSEError: true, result: "upstream_error", reason: "unknown", ttft: 1, physical: 1},
		{name: "unknown_transport_timeout", failure: os.ErrDeadlineExceeded, result: "timeout", reason: "unknown", physical: 1},
		{name: "signature_retry", status: 200, signature: true, result: "success", ttft: 1, physical: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type runResult struct {
				response string
				headers  http.Header
				status   int
				requests []forwardOpsSnapshot
				usage    *ClaudeUsage
				cost     *CostBreakdown
				failure  string
			}
			var baseline *runResult
			for _, enabled := range []bool{false, true} {
				reg := prometheus.NewRegistry()
				m, err := monitoring.NewOpsMetrics(reg)
				require.NoError(t, err)
				input := `{"model":"claude-3-5-sonnet-latest","stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] + `,"messages":[{"role":"user","content":"hello"}]}`
				if tc.signature {
					input = `{"model":"claude-3-5-sonnet-latest","stream":false,"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"retained thought","signature":"invalid-test-signature"},{"type":"text","text":"previous answer"}]},{"role":"user","content":"hello"}]}`
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := parent
				var rec *monitoring.OpsRecorder
				entryBefore := time.Now()
				if enabled {
					ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/messages", m)
					rec.AttachMeta(monitoring.OpsRequestMeta{Model: "claude-3-5-sonnet-latest"})
					rec.MarkBodyRead(int64(len(input)), int64(len(input)), "complete")
					rec.AddBytes("req_decoded", int64(len(input)), "complete")
				}
				entryAfter := time.Now()
				w := httptest.NewRecorder()
				var writer http.ResponseWriter = w
				if tc.stream && tc.abort {
					writer = &forwardOpsFailWriter{w}
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(input)).WithContext(ctx)
				c.Request.Header.Set("User-Agent", "ops-contract-test")
				c.Request.Header.Set("X-Test-Input", "unchanged")
				upstream := &forwardOpsUpstream{failure: tc.failure}
				consumedResponseBytes := 0
				upstream.responses = func(call int) *http.Response {
					status := tc.status
					payload := jsonBody
					if status >= 400 {
						payload = `{"error":{"type":"upstream_error","message":"failure"}}`
					}
					if tc.signature && call == 1 {
						status = 400
						payload = `{"error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`
					}
					if tc.stream {
						payload = sseBody
					}
					if tc.semanticError {
						payload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
					}
					if tc.ordinarySSEError {
						payload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"ordinary upstream error\"}}\n\n"
					}
					header := http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"ops-fixed-id"}}
					if tc.stream {
						header.Set("Content-Type", "text/event-stream")
					}
					var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
					consumedResponseBytes += len(payload)
					if tc.readFailure != nil {
						if tc.stream {
							payload = strings.Split(sseBody, "event: message_stop")[0]
							consumedResponseBytes -= len(sseBody) - len(payload)
						} else {
							consumedResponseBytes -= len(payload)
							payload = ""
						}
						body = &streamReadCloser{payload: []byte(payload), err: tc.readFailure}
					}
					if tc.abort && !tc.stream {
						body = &forwardOpsCancelBody{cancel: cancel}
						consumedResponseBytes -= len(payload)
					}
					return &http.Response{StatusCode: status, Header: header, Body: body}
				}
				cfgService := newForwardPartialUsageServiceForTest(nil)
				cfgService.httpUpstream = upstream
				cfgService.rateLimitService = &RateLimitService{accountRepo: &forwardOpsAccountRepo{}, cfg: cfgService.cfg}
				settings := DefaultRectifierSettings()
				settings.Enabled = true
				settings.APIKeySignatureEnabled = true
				settings.ThinkingSignatureEnabled = true
				settingsJSON, _ := json.Marshal(settings)
				cfgService.settingService = &SettingService{settingRepo: &forwardOpsSettings{betaPolicySettingRepoStub{values: map[string]string{SettingKeyRectifierSettings: string(settingsJSON)}}}}
				cfgService.billingService = NewBillingService(cfgService.cfg, nil)
				account := newAnthropicAPIKeyAccountForTest()
				account.Extra = nil
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(input)), PlatformAnthropic)
				require.NoError(t, err)
				forwardBefore := time.Now()
				result, forwardErr := cfgService.Forward(ctx, c, account, parsed)
				forwardAfter := time.Now()
				if tc.semanticError || tc.ordinarySSEError {
					var semanticFailure *UpstreamFailoverError
					require.ErrorAs(t, forwardErr, &semanticFailure)
					wantStatus := 403
					if tc.semanticError {
						wantStatus = 529
					}
					require.Equal(t, wantStatus, semanticFailure.StatusCode, "business semantic status is unchanged")
				}
				if forwardErr != nil && !c.Writer.Written() {
					c.AbortWithStatus(502)
				}
				c.Writer.WriteHeaderNow()
				got := runResult{response: w.Body.String(), headers: w.Header().Clone(), status: w.Code, requests: upstream.requests}
				if forwardErr != nil {
					got.failure = forwardErr.Error()
				}
				if result != nil {
					usage := result.Usage
					got.usage = &usage
					got.cost = cfgService.calculateRecordUsageCost(ctx, result, &APIKey{}, result.Model, 1, 1, time.Time{})
				}
				if !enabled {
					baseline = &got
					continue
				}
				require.Equal(t, *baseline, got, "observability must not change requests/headers/response/SSE/error/usage/cost")
				require.Len(t, upstream.requests, tc.physical)
				if tc.signature {
					require.NotEqual(t, upstream.requests[0].body, upstream.requests[1].body, "signature retry must exercise the existing body rewrite")
				}
				require.Equal(t, 1.0, forwardOpsCounter(forwardOpsGather(t, reg), "sub2api_upstream_requests_total"), "Done settles before Finalize")
				rec.AddBytes("resp_downstream", int64(max(0, c.Writer.Size())), "complete")
				rec.Finalize(monitoring.FinalizeInput{Status: c.Writer.Status()})
				all := forwardOpsGather(t, reg)
				require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
				require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_model_requests_total"))
				require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_gateway_requests_total"))
				require.Len(t, all["sub2api_upstream_requests_total"], 1)
				require.Equal(t, tc.result, all["sub2api_upstream_requests_total"][0].labels["result"])
				require.Equal(t, tc.reason, all["sub2api_upstream_requests_total"][0].labels["failure_reason"])
				require.Equal(t, tc.result, all["sub2api_gateway_requests_total"][0].labels["result"])
				require.Equal(t, tc.reason, all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
				outcome := "all_failed"
				if tc.result == "success" {
					outcome = "first_success"
				}
				require.Equal(t, outcome, all["sub2api_attempts_total"][0].labels["outcome"])
				reqBytes := 0
				for _, req := range upstream.requests {
					reqBytes += len(req.body)
				}
				wantBytes := map[string]float64{"in_wire": float64(len(input)), "req_decoded": float64(len(input)), "req_upstream": float64(reqBytes), "resp_upstream": float64(consumedResponseBytes), "resp_downstream": float64(w.Body.Len())}
				for _, metric := range all["sub2api_transfer_bytes_total"] {
					require.Equal(t, wantBytes[metric.labels["direction"]], metric.counter, metric.labels["direction"])
					delete(wantBytes, metric.labels["direction"])
				}
				for dir, n := range wantBytes {
					require.Zero(t, n, dir)
				}
				require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
				require.Equal(t, float64(len(input)), all["sub2api_request_body_bytes"][0].sum)
				ttftCount := uint64(0)
				for _, metric := range all["sub2api_upstream_ttft_seconds"] {
					ttftCount += metric.count
				}
				require.EqualValues(t, tc.ttft, ttftCount)
				total := 0.0
				exclusive := 0
				for _, phase := range all["sub2api_request_phase_seconds"] {
					if phase.labels["phase"] == "headers_end" {
						continue
					}
					exclusive++
					require.EqualValues(t, 1, phase.count)
					require.GreaterOrEqual(t, phase.sum, 0.0)
					total += phase.sum
				}
				require.Equal(t, 6, exclusive)
				require.GreaterOrEqual(t, total, forwardBefore.Sub(entryAfter).Seconds()-.001)
				require.LessOrEqual(t, total, forwardAfter.Sub(entryBefore).Seconds()+.001, "six exclusive phases close within observed entry/terminal bounds")
				t.Logf("%s physical=%d M4=1 M5=1 M6=%s M10=%s/%s M11=%d bytes=%d/%d PASS", tc.name, tc.physical, outcome, tc.result, tc.reason, ttftCount, reqBytes, consumedResponseBytes)
			}
		})
	}
}

func TestGatewayForwardOpsLocalShortCircuits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"empty_parsed", "missing_credential", "beta_policy", "request_build"} {
		t.Run(mode, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics, err := monitoring.NewOpsMetrics(reg)
			require.NoError(t, err)
			ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			upstream := &forwardOpsUpstream{responses: func(int) *http.Response { t.Fatal("local failure must not send"); return nil }}
			svc := newForwardPartialUsageServiceForTest(nil)
			svc.httpUpstream = upstream
			account := newAnthropicAPIKeyAccountForTest()
			account.Extra = nil
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			switch mode {
			case "empty_parsed":
				parsed = nil
			case "missing_credential":
				delete(account.Credentials, "api_key")
			case "beta_policy":
				policy, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: "ops-denied-beta", Action: BetaPolicyActionBlock, Scope: BetaPolicyScopeAll, ErrorMessage: "blocked in ops test"}}})
				require.NoError(t, err)
				svc.settingService = &SettingService{settingRepo: &forwardOpsSettings{betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: string(policy)}}}}
				c.Request.Header.Set("anthropic-beta", "ops-denied-beta")
			case "request_build":
				account.Credentials["base_url"] = "://bad-url"
			}
			_, err = svc.Forward(ctx, c, account, parsed)
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			wantAttempts := 0.0
			require.Equal(t, wantAttempts, forwardOpsCounter(forwardOpsGather(t, reg), "sub2api_upstream_requests_total"), "first build failure and local short-circuits create no attempt")
			rec.Finalize(monitoring.FinalizeInput{Status: 400})
			all := forwardOpsGather(t, reg)
			require.Equal(t, wantAttempts, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
			require.Zero(t, forwardOpsCounter(all, "sub2api_model_requests_total"))
			if mode == "request_build" {
				require.Equal(t, "gateway_error", all["sub2api_gateway_requests_total"][0].labels["result"])
			}
			require.Empty(t, all["sub2api_upstream_ttft_seconds"])
		})
	}
}

func TestGatewayForwardOpsResponseHeaderTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accepted := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		accepted <- struct{}{}
		<-release // connection and body accepted; deliberately send no response headers
	}))
	defer server.Close()
	defer close(release)
	transport := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	defer transport.CloseIdleConnections()
	upstream := &forwardOpsUpstream{client: &http.Client{Transport: transport}}
	svc := newForwardPartialUsageServiceForTest(nil)
	svc.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc.httpUpstream = upstream
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra = nil
	account.Credentials["base_url"] = server.URL
	reg := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(reg)
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(ctx, c, account, parsed)
	require.Error(t, err)
	var businessFailure *UpstreamFailoverError
	require.ErrorAs(t, err, &businessFailure)
	require.Equal(t, 502, businessFailure.StatusCode)
	require.ErrorContains(t, upstream.transportFailure, "timeout awaiting response headers")
	select {
	case <-accepted:
	default:
		t.Fatal("upstream must accept the connection and request before timing out")
	}
	all := forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"), "explicit Done before Finalize")
	require.Equal(t, "timeout", all["sub2api_upstream_requests_total"][0].labels["result"])
	require.Equal(t, "upstream_read_timeout", all["sub2api_upstream_requests_total"][0].labels["failure_reason"])
	rec.Finalize(monitoring.FinalizeInput{Status: 502})
	all = forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_model_requests_total"))
	require.Equal(t, "upstream_read_timeout", all["sub2api_gateway_requests_total"][0].labels["failure_reason"])
	require.Empty(t, all["sub2api_upstream_ttft_seconds"])
}

func TestGatewayForwardOpsRetryBuildFailureSettlesOriginalAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(reg)
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", metrics)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra = nil
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(400)}
	upstream := &forwardOpsUpstream{responses: func(int) *http.Response {
		account.Credentials["base_url"] = "://bad-url"
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"api_error"}}`))}
	}}
	svc := newForwardPartialUsageServiceForTest(nil)
	svc.httpUpstream = upstream
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(ctx, c, account, parsed)
	require.Error(t, err)
	require.Len(t, upstream.requests, 1)
	all := forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
	require.Equal(t, "gateway_error", all["sub2api_upstream_requests_total"][0].labels["result"])
	rec.Finalize(monitoring.FinalizeInput{Status: 502})
	all = forwardOpsGather(t, reg)
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_upstream_requests_total"))
	require.Equal(t, 1.0, forwardOpsCounter(all, "sub2api_model_requests_total"))
	require.Empty(t, all["sub2api_upstream_ttft_seconds"])
}

func TestGatewayForwardOpsTransportStage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		stage string
	}{
		{"dial", &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}, "connect"},
		{"headers", errors.New("net/http: timeout awaiting response headers"), "headers"},
		{"unknown_timeout", os.ErrDeadlineExceeded, "unknown"},
		{"unknown", errors.New("unclassified transport failure"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.stage, opsUpstreamTransportStage(tc.err)) })
	}
}
