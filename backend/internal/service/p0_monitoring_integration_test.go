//go:build integration

package service_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This external package deliberately uses the public repository interface. A
// fake HTTPUpstream that installs its own monitoring wrappers bypasses the outer
// response decorators installed by Do/DoWithTLS and cannot test their identity.
type p0AccountRepo struct{ service.AccountRepository }

func (*p0AccountRepo) SetRateLimited(context.Context, int64, time.Time) error { return nil }
func (*p0AccountRepo) SetOverloaded(context.Context, int64, time.Time) error  { return nil }
func (*p0AccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (*p0AccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}
func (*p0AccountRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}

type p0Unscheduler struct{}

func (*p0Unscheduler) TempUnscheduleRetryableError(context.Context, int64, *service.UpstreamFailoverError) {
}

type p0Settings struct{ service.SettingRepository }

func (*p0Settings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyRectifierSettings {
		return `{"enabled":true,"thinking_signature_enabled":true,"thinking_budget_enabled":true,"apikey_signature_enabled":true}`, nil
	}
	return "", service.ErrSettingNotFound
}
func (*p0Settings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type p0SampleSink struct{ samples []monitoring.OpsSample }

func (s *p0SampleSink) Submit(sample monitoring.OpsSample) bool {
	s.samples = append(s.samples, sample)
	return true
}

type p0Request struct {
	Method, URI, Body string
	Header            http.Header
}

type p0Writer struct {
	*httptest.ResponseRecorder
	fail    bool
	flushes int
	chunks  []string
}

func (w *p0Writer) Write(p []byte) (int, error) {
	if w.fail {
		return 0, syscall.EPIPE
	}
	w.chunks = append(w.chunks, string(p))
	return w.ResponseRecorder.Write(p)
}
func (w *p0Writer) WriteString(p string) (int, error) { return w.Write([]byte(p)) }
func (w *p0Writer) Flush()                            { w.flushes++; w.ResponseRecorder.Flush() }

type p0Snapshot struct {
	Requests []p0Request
	Status   int
	Header   http.Header
	Body     string
	Chunks   []string
	Flushes  int
	Usage    any
	Cost     *service.CostBreakdown
	Failure  string
}

// Replay transport failure evidence without depending on unroutable external
// addresses or timing out the OS TCP stack. Normal cases use the real repository.
type p0ReplayUpstream struct {
	service.HTTPUpstream
	mode    string
	capture func(*http.Request, []byte)
}

func (u *p0ReplayUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	a := monitoring.AttemptFromContext(req.Context())
	h := a.Sent()
	if u.mode == "connect_timeout" {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
		a.BodyDoneFor(h, err)
		return nil, err
	}
	rec := monitoring.RecorderFromContext(req.Context())
	monitoring.ObserveHTTPRequest(rec, req)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	u.capture(req, body)
	a.NoteHeadersFor(h, 200)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(p0ReadTimeout{}), Request: req}
	resp.Body = monitoring.ObserveResponseBody(rec, a, h, resp.Body)
	return resp, nil
}
func (u *p0ReplayUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type p0ReadTimeout struct{}

func (p0ReadTimeout) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

type p0TLSUpstream struct{ service.HTTPUpstream }

func (u p0TLSUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.HTTPUpstream.DoWithTLS(req, proxy, id, concurrency, &tlsfingerprint.Profile{Name: "p0-non-nil"})
}
func (u p0TLSUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func p0Normalize(body string) string {
	body = regexp.MustCompile(`("(?:created|created_at|completed_at)":\s*)\d+`).ReplaceAllString(body, `${1}0`)
	ids := map[string]string{}
	return regexp.MustCompile(`(?:item_|chatcmpl[-_]|msg_)[0-9a-f]{24}`).ReplaceAllStringFunc(body, func(id string) string {
		if ids[id] == "" {
			ids[id] = fmt.Sprintf("%s%024d", id[:len(id)-24], len(ids))
		}
		return ids[id]
	})
}

func p0Construct(cfg *config.Config, upstream service.HTTPUpstream) (*service.GatewayService, *service.OpenAIGatewayService, *service.BillingService) {
	repo := &p0AccountRepo{}
	billing := service.NewBillingService(cfg, nil)
	limits := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	settings := service.NewSettingService(&p0Settings{}, cfg)
	gw := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, billing, limits, nil, nil, upstream, &service.DeferredService{},
		nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil)
	oai := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, billing, limits, nil, upstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, settings, nil)
	return gw, oai, billing
}

func p0Route(path string) string {
	if strings.Contains(path, "responses") {
		return "/v1/responses"
	}
	if strings.Contains(path, "chat") {
		return "/v1/chat/completions"
	}
	return "/v1/messages"
}

func p0Run(t *testing.T, path, scenario string, enabled bool) (p0Snapshot, map[string]*dto.MetricFamily, []monitoring.OpsSample) {
	t.Helper()
	stream := scenario == "SSE" || scenario == "abort"
	model := "gpt-4o"
	input := fmt.Sprintf(`{"model":"gpt-4o","stream":%t,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, stream)
	if strings.Contains(path, "responses") {
		input = fmt.Sprintf(`{"model":"gpt-4o","stream":%t,"input":"hello"}`, stream)
	}
	if scenario == "signature" {
		input = `{"model":"gpt-4o","stream":false,"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"retained thought","signature":"invalid-fixture-signature"},{"type":"text","text":"previous answer"}]},{"role":"user","content":"hello"}]}`
		model = "claude-3-5-sonnet-latest"
		input = strings.Replace(input, "gpt-4o", model, 1)
	}
	var mu sync.Mutex
	var requests []p0Request
	respBytes := 0
	status := 200
	if parsed, err := strconv.Atoi(scenario); err == nil {
		status = parsed
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		mu.Lock()
		requests = append(requests, p0Request{r.Method, r.RequestURI, string(body), r.Header.Clone()})
		index := len(requests)
		mu.Unlock()
		if scenario == "headers_timeout" {
			<-r.Context().Done()
			return
		}
		code := status
		if scenario == "cross_account" && index == 1 {
			code = 429
		}
		if scenario == "signature" && index == 1 {
			code = 400
		}
		payload := p0ResponsesJSON
		if stream {
			payload = p0ResponsesSSE
		}
		if strings.HasPrefix(path, "claude") || strings.HasPrefix(path, "compat") {
			payload = p0AnthropicJSON
			if stream || strings.HasPrefix(path, "compat") {
				payload = p0AnthropicSSE
			}
		} else if path == "native_chat" || path == "messages_raw" {
			payload = p0ChatJSON
			if stream {
				payload = p0ChatSSE
			}
		} else if path == "native_chat_bridge" || path == "messages_bridge" {
			// These conversion pipelines request upstream SSE even for JSON input.
			payload = p0ResponsesSSE
		}
		if code >= 400 {
			payload = `{"error":{"type":"invalid_request_error","message":"fixture failure"}}`
		}
		if scenario == "signature" && index == 1 {
			payload = `{"error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`
		}
		contentType := "application/json"
		if strings.HasPrefix(payload, "data:") || strings.HasPrefix(payload, "event:") {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Request-Id", "p0-fixed")
		w.Header().Set("Date", "Mon, 02 Jan 2006 15:04:05 GMT")
		mu.Lock()
		respBytes += len(payload)
		mu.Unlock()
		if scenario == "gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(code)
			z := gzip.NewWriter(w)
			_, _ = z.Write([]byte(payload))
			_ = z.Close()
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, payload)
		if strings.Contains(contentType, "event-stream") {
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	if scenario == "refused" {
		server.Close()
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 1 << 20, ResponseHeaderTimeout: 1, OpenAIResponseHeaderTimeout: 1}}
	cfg.Monitoring.OpsMetricsEnabled = enabled
	// All test servers bind loopback. Never use real provider endpoints.
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	upstream := repository.NewHTTPUpstream(cfg)
	if scenario == "proxy_502_tls" {
		upstream = p0TLSUpstream{upstream}
	}
	if scenario == "connect_timeout" || scenario == "read_timeout" {
		upstream = &p0ReplayUpstream{HTTPUpstream: upstream, mode: scenario, capture: func(req *http.Request, body []byte) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, p0Request{req.Method, req.URL.RequestURI(), string(body), req.Header.Clone()})
		}}
	}
	gw, oai, billing := p0Construct(cfg, upstream)
	a := &service.Account{ID: 101, Name: "p0-fixture", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Concurrency: 1, Status: service.StatusActive,
		Credentials: map[string]any{"api_key": "fixture-key", "base_url": server.URL},
		Extra:       map[string]any{"openai_responses_supported": true, "openai_responses_mode": "auto"}}
	if strings.HasPrefix(path, "claude") || strings.HasPrefix(path, "compat") {
		a.Platform = service.PlatformAnthropic
	}
	if path == "claude_passthrough" {
		a.Extra["anthropic_passthrough"] = true
	}
	if path == "native_passthrough" {
		a.Extra["openai_passthrough"] = true
	}
	if path == "native_chat" || path == "messages_raw" {
		a.Extra["openai_responses_supported"] = false
	}
	if strings.HasPrefix(scenario, "proxy_502") {
		// net/http returns a CONNECT rejection as a transport error without a
		// response. Do not classify its 502 as an origin HTTP response.
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodConnect, r.Method)
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer proxy.Close()
		proxyURL, parseErr := url.Parse(proxy.URL)
		require.NoError(t, parseErr)
		port, parseErr := strconv.Atoi(proxyURL.Port())
		require.NoError(t, parseErr)
		proxyID := int64(1)
		a.ProxyID = &proxyID
		a.Proxy = &service.Proxy{ID: proxyID, Protocol: "http", Host: proxyURL.Hostname(), Port: port}
		a.Credentials["base_url"] = strings.Replace(server.URL, "http://", "https://", 1)
	}
	reg := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(reg)
	require.NoError(t, err)
	runtime := monitoring.NewOpsRuntime(metrics)
	sink := &p0SampleSink{}
	runtime.SampleSink = sink
	router := gin.New()
	router.Use(middleware.OpsMiddleware(cfg, runtime), middleware.Recovery())
	var got p0Snapshot
	router.POST(p0Route(path), func(c *gin.Context) {
		body, readErr := handler.ReadGatewayJSONRequestBody(c.Request, cfg)
		require.NoError(t, readErr)
		if scenario == "preread" {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			body, readErr = io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
		}
		rec := monitoring.RecorderFromContext(c.Request.Context())
		rec.AttachMeta(monitoring.OpsRequestMeta{Model: model})
		invoke := func() (int, int, error) {
			ctx := c.Request.Context()
			if strings.HasPrefix(path, "claude") || strings.HasPrefix(path, "compat") {
				protocol := "anthropic"
				if path == "compat_chat" {
					protocol = "openai_chat"
				}
				if path == "compat_responses" {
					protocol = "openai_responses"
				}
				parsed, parseErr := service.ParseGatewayRequest(service.NewRequestBodyRef(body), protocol)
				require.NoError(t, parseErr)
				var result *service.ForwardResult
				var forwardErr error
				switch path {
				case "compat_chat":
					result, forwardErr = gw.ForwardAsChatCompletions(ctx, c, a, body, parsed)
				case "compat_responses":
					result, forwardErr = gw.ForwardAsResponses(ctx, c, a, body, parsed)
				default:
					result, forwardErr = gw.Forward(ctx, c, a, parsed)
				}
				if result != nil {
					got.Usage = result.Usage
					return result.Usage.InputTokens, result.Usage.OutputTokens, forwardErr
				}
				return 0, 0, forwardErr
			}
			var result *service.OpenAIForwardResult
			var forwardErr error
			switch path {
			case "native_chat", "native_chat_bridge":
				result, forwardErr = oai.ForwardAsChatCompletions(ctx, c, a, body, "", "")
			case "messages_bridge", "messages_raw":
				result, forwardErr = oai.ForwardAsAnthropic(ctx, c, a, body, "", "")
			default:
				result, forwardErr = oai.Forward(ctx, c, a, body)
			}
			if result != nil {
				got.Usage = result.Usage
				return result.Usage.InputTokens, result.Usage.OutputTokens, forwardErr
			}
			return 0, 0, forwardErr
		}
		in, out, forwardErr := invoke()
		if scenario == "cross_account" {
			failover, ok := forwardErr.(*service.UpstreamFailoverError)
			require.True(t, ok)
			state := handler.NewFailoverState(1, false)
			require.Equal(t, handler.FailoverContinue, state.HandleFailoverError(c.Request.Context(), &p0Unscheduler{}, a.ID, a.Platform, 0, failover))
			a.ID = 102
			in, out, forwardErr = invoke()
		}
		if forwardErr != nil {
			got.Failure = forwardErr.Error()
		}
		if got.Usage != nil {
			price := 0.000001
			group := &service.Group{ID: 1, Platform: a.Platform, ModelPricing: []service.ChannelModelPricing{{Models: []string{model}, BillingMode: service.BillingModeToken, InputPrice: &price, OutputPrice: &price}}}
			got.Cost, err = billing.CalculateTokenCostForRequest(service.TokenCostRequest{Ctx: c.Request.Context(), Model: model, Group: group,
				Tokens: service.UsageTokens{InputTokens: in, OutputTokens: out}, RateMultiplier: 1, Resolver: service.NewModelPricingResolver(nil, billing)})
			require.NoError(t, err)
		}
		// Represents the usage submit return barrier, not a database write.
		rec.MarkFinish()
	})
	w := &p0Writer{ResponseRecorder: httptest.NewRecorder(), fail: scenario == "abort"}
	req := httptest.NewRequest(http.MethodPost, p0Route(path), strings.NewReader(input))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Request-Id", "p0-fixed")
	router.ServeHTTP(w, req)
	got.Status, got.Header, got.Body, got.Flushes = w.Code, w.Header().Clone(), p0Normalize(w.Body.String()), w.flushes
	// Normalize all chunks in one pass so IDs retain their relationship across frames.
	const sep = "\x00P0_CHUNK\x00"
	got.Chunks = strings.Split(p0Normalize(strings.Join(w.chunks, sep)), sep)
	mu.Lock()
	got.Requests = append([]p0Request(nil), requests...)
	readBytes := respBytes
	mu.Unlock()
	if scenario == "signature" {
		require.Len(t, got.Requests, 2, "signature correction performs two sends in one logical attempt")
		require.Contains(t, got.Requests[0].Body, `"signature"`)
		require.NotContains(t, got.Requests[1].Body, `"signature"`)
	}
	families, err := reg.Gather()
	require.NoError(t, err)
	all := map[string]*dto.MetricFamily{}
	for _, family := range families {
		all[family.GetName()] = family
	}
	if enabled {
		p0AssertMetrics(t, all, sink.samples, scenario, len(input), w.Body.Len(), readBytes, got)
	}
	return got, all, sink.samples
}

func p0Counter(all map[string]*dto.MetricFamily, name string) float64 {
	var n float64
	for _, metric := range all[name].GetMetric() {
		n += metric.GetCounter().GetValue()
	}
	return n
}
func p0Label(metric *dto.Metric, key string) string {
	for _, label := range metric.Label {
		if label.GetName() == key {
			return label.GetValue()
		}
	}
	return ""
}

func p0AssertMetrics(t *testing.T, all map[string]*dto.MetricFamily, samples []monitoring.OpsSample, scenario string, inputBytes, outputBytes, upstreamBytes int, got p0Snapshot) {
	t.Helper()
	attempts := 1.0
	wantSwitches := 0.0
	outcome, result, reason := "first_success", "success", ""
	if scenario == "cross_account" {
		attempts, wantSwitches, outcome = 2, 1, "retried_success"
	}
	if scenario == "429" {
		outcome, result, reason = "all_failed", "upstream_error", "upstream_rate_limit"
	}
	if scenario == "500" || scenario == "502" {
		outcome, result, reason = "all_failed", "upstream_error", "upstream_server_error"
	}
	if scenario == "headers_timeout" {
		outcome, result, reason = "all_failed", "timeout", "upstream_read_timeout"
	}
	if scenario == "read_timeout" {
		outcome, result, reason = "all_failed", "timeout", "upstream_read_timeout"
	}
	if scenario == "connect_timeout" {
		outcome, result, reason = "all_failed", "timeout", "upstream_connect_timeout"
	}
	if scenario == "refused" {
		outcome, result, reason = "all_failed", "upstream_error", "unknown"
	}
	if strings.HasPrefix(scenario, "proxy_502") {
		outcome, result, reason = "all_failed", "upstream_error", "proxy_unreachable"
	}
	if scenario == "abort" {
		outcome, result, reason = "all_failed", "client_abort", "client_abort_during_response"
	}
	assert.Equal(t, attempts, p0Counter(all, "sub2api_upstream_requests_total"), "M4 logical attempts")
	assert.Equal(t, attempts, p0Counter(all, "sub2api_model_requests_total"), "M5 logical attempts")
	for _, metric := range all["sub2api_upstream_requests_total"].GetMetric() {
		wantResult, wantReason := result, reason
		if scenario == "cross_account" && p0Label(metric, "result") != "success" {
			wantResult, wantReason = "upstream_error", "upstream_rate_limit"
		}
		assert.Equal(t, wantResult, p0Label(metric, "result"), "M4 attempt result")
		assert.Equal(t, wantReason, p0Label(metric, "failure_reason"), "M4 attempt reason")
	}
	assert.Equal(t, 1.0, p0Counter(all, "sub2api_attempts_total"), "M6 once per entrance")
	assert.Equal(t, outcome, p0Label(all["sub2api_attempts_total"].GetMetric()[0], "outcome"))
	assert.Equal(t, wantSwitches, p0Counter(all, "sub2api_account_switches_total"), "M7 actual handler failover")
	assert.Equal(t, 1.0, p0Counter(all, "sub2api_gateway_requests_total"), "M10 once per entrance")
	entrance := all["sub2api_gateway_requests_total"].GetMetric()[0]
	assert.Equal(t, result, p0Label(entrance, "result"))
	assert.Equal(t, reason, p0Label(entrance, "failure_reason"))
	wantAbort := 0.0
	if scenario == "abort" {
		wantAbort = 1
	}
	assert.Equal(t, wantAbort, p0Counter(all, "sub2api_client_abort_total"), "M8 distinguishes EOF/timeout/abort")
	require.Len(t, all["sub2api_request_body_bytes"].GetMetric(), 1, "M3 once per entrance, including prereads")
	for _, metric := range all["sub2api_request_body_bytes"].GetMetric() {
		assert.EqualValues(t, 1, metric.GetHistogram().GetSampleCount(), "M3 single body observation")
		assert.EqualValues(t, inputBytes, metric.GetHistogram().GetSampleSum())
	}
	var reqBytes int
	for _, req := range got.Requests {
		reqBytes += len(req.Body)
	}
	wantBytes := map[string]int{"in_wire": inputBytes, "req_decoded": inputBytes, "req_upstream": reqBytes, "resp_upstream": upstreamBytes, "resp_downstream": outputBytes}
	for _, metric := range all["sub2api_transfer_bytes_total"].GetMetric() {
		direction := p0Label(metric, "direction")
		assert.EqualValues(t, wantBytes[direction], metric.GetCounter().GetValue(), "M2 %s actual n (decompressed upstream)", direction)
		delete(wantBytes, direction)
	}
	for direction, n := range wantBytes {
		assert.Zero(t, n, "M2 missing nonzero direction %s", direction)
	}
	var total float64
	var phases int
	for _, metric := range all["sub2api_request_phase_seconds"].GetMetric() {
		if p0Label(metric, "phase") == "headers_end" {
			continue
		}
		phases++
		assert.EqualValues(t, 1, metric.GetHistogram().GetSampleCount())
		assert.GreaterOrEqual(t, metric.GetHistogram().GetSampleSum(), 0.0)
		total += metric.GetHistogram().GetSampleSum()
	}
	assert.Equal(t, 6, phases, "M1 six exclusive phases")
	require.Len(t, samples, 1, "immutable Finalize sample")
	// Samples round duration to integer milliseconds; the histogram retains seconds.
	assert.InDelta(t, float64(*samples[0].TotalMs), total*1000, 1.01, "M1 closure excludes headers_end")
	var ttft uint64
	for _, metric := range all["sub2api_upstream_ttft_seconds"].GetMetric() {
		ttft += metric.GetHistogram().GetSampleCount()
	}
	wantTTFT := uint64(1)
	if scenario == "429" || scenario == "500" || scenario == "502" || strings.Contains(scenario, "timeout") || scenario == "refused" || strings.HasPrefix(scenario, "proxy_502") {
		wantTTFT = 0
	}
	// Keep this assertion strict: the public repository response must retain the
	// observation identity through its in-flight tracking decorator.
	assert.Equal(t, wantTTFT, ttft, "M11 only final locked response; public Do must preserve response identity")
	t.Logf("M1=closed M2=%d/%d/%d/%d/%d M4/M5=%g M6=%s M7=%g M8=%g M10=1 M11=%d(want %d) result=%s failure_reason=%s", inputBytes, inputBytes, reqBytes, upstreamBytes, outputBytes, attempts, outcome, wantSwitches, wantAbort, ttft, wantTTFT, p0Label(entrance, "result"), p0Label(entrance, "failure_reason"))
}

func TestP0MonitoringHTTPMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"claude", "claude_passthrough", "compat_chat", "compat_responses", "native_responses", "native_passthrough", "native_chat", "native_chat_bridge", "messages_bridge", "messages_raw"} {
		for _, scenario := range []string{"200", "SSE", "429", "500", "502", "connect_timeout", "headers_timeout", "read_timeout", "refused", "proxy_502", "abort", "gzip", "preread", "cross_account", "signature"} {
			if scenario == "signature" && path != "claude" {
				continue
			}
			t.Run(path+"/"+scenario, func(t *testing.T) {
				baseline, disabled, _ := p0Run(t, path, scenario, false)
				enabled, _, _ := p0Run(t, path, scenario, true)
				assert.Equal(t, baseline, enabled, "request bytes/headers, JSON/SSE/status/chunks/Flush, usage and cost remain identical")
				for _, family := range disabled {
					for _, metric := range family.GetMetric() {
						assert.Zero(t, metric.GetCounter().GetValue(), "disabled mode increments no V2 counter")
						assert.Zero(t, metric.GetHistogram().GetSampleCount(), "disabled mode observes no V2 histogram")
					}
				}
			})
		}
	}
}

func TestP0MonitoringTLSProxyConnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"claude", "claude_passthrough", "compat_chat", "compat_responses", "native_responses", "native_passthrough", "native_chat", "native_chat_bridge", "messages_bridge", "messages_raw"} {
		t.Run(path, func(t *testing.T) {
			baseline, _, _ := p0Run(t, path, "proxy_502_tls", false)
			enabled, _, _ := p0Run(t, path, "proxy_502_tls", true)
			assert.Equal(t, baseline, enabled, "non-nil TLS profile preserves CONNECT error and all business outputs")
		})
	}
}

const p0ChatJSON = `{"id":"chatcmpl_fixed","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`

const p0ChatSSE = "data: {\"id\":\"chatcmpl_fixed\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl_fixed\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\ndata: [DONE]\n\n"

const p0AnthropicJSON = `{"id":"msg_fixed","type":"message","role":"assistant","model":"gpt-4o","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":5}}`

const p0AnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixed\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-4o\",\"content\":[],\"usage\":{\"input_tokens\":11}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const p0ResponsesJSON = `{"id":"resp_fixed","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg_fixed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}`

const p0ResponsesSSE = "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_fixed\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":" + p0ResponsesJSON + "}\n\ndata: [DONE]\n\n"
