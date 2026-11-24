package monitoring

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountLabelsSnapshot(t *testing.T) {
	names := []string{" A.B ", "a b", "Ａ", "unknown", strings.Repeat("X", 40), strings.Repeat("X", 33)}
	for i := 0; i < 20; i++ {
		names = append(names, fmt.Sprintf("Account-%d", i))
	}
	a := NewAccountLabels(names)
	require.Equal(t, "ab", a.Label(" A.B "))
	for _, name := range []string{"a b", "ab", "Ａ", "unknown", strings.Repeat("X", 33), "Account-8", "new"} {
		require.Equal(t, "unknown", a.Label(name), name)
	}
	require.Equal(t, strings.Repeat("x", 32), a.Label(strings.Repeat("X", 40)))
	names[0] = "changed"
	require.Equal(t, "ab", a.Label(" A.B "))
	require.Equal(t, "unknown", (*AccountLabels)(nil).Label("any"))
	require.Equal(t, "unknown", NewAccountLabels(nil).Label("any"))
	require.Equal(t, "name_-123", normalizeAccount(" Na!me_-123 "))
}

func TestAccountLabelsAdversarialBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	names := make([]string, 1000)
	for i := range names {
		var b strings.Builder
		for j := 0; j < 100; j++ {
			b.WriteRune(rune(rng.Intn(0x110000)))
		}
		names[i] = fmt.Sprintf("%d/%s", i, b.String())
	}
	a := NewAccountLabels(names)
	labels := map[string]bool{"unknown": true}
	valid := regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	for _, name := range names {
		label := a.Label(name)
		require.Regexp(t, valid, label)
		labels[label] = true
	}
	require.LessOrEqual(t, len(labels), 11)
	t.Logf("1000 adversarial account names: %d labels (limit 11)", len(labels))
}

func TestEndpointAndModelClasses(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/messages": "responses", "/antigravity/v1/messages": "responses", "/v1/responses": "responses",
		"/responses": "responses", "/backend-api/codex/responses": "responses",
		"/v1/responses/*subpath": "responses", "/responses/*subpath": "responses", "/backend-api/codex/responses/*subpath": "responses",
		"/v1/responses/compact": "responses", "/responses/compact": "responses", "/backend-api/codex/responses/compact": "responses",
		"/v1/chat/completions": "chat", "/chat/completions": "chat", "/v1/embeddings": "embeddings", "/embeddings": "embeddings",
		"/v1/images/generations": "images", "/v1/images/*path": "images", "/v1/images/edits": "images",
		"/v1/messages/count_tokens": "other", "/v1/models": "other", "/admin": "other", "": "other", "/v1/images-evil": "other",
	} {
		require.Equal(t, want, EndpointClass(path), path)
	}
	for model, want := range map[string]string{"GPT-4": "gpt", "GPT-CoDeX": "codex", "gpt-CLAUDE": "claude", "gpt-gemini": "gemini", "codex-claude-gemini-gpt": "codex", "": "other", "private-model": "other"} {
		require.Equal(t, want, ModelClass(model), model)
	}
	for _, result := range []string{"success", "upstream_error", "timeout", "client_abort", "gateway_error", "rejected"} {
		require.Equal(t, result, NormalizeResult(result))
	}
	for _, result := range []string{"", "unknown", "arbitrary"} {
		require.Equal(t, "gateway_error", NormalizeResult(result))
	}
}

func TestFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		ev   FailureEvidence
		want string
	}{
		{"429", FailureEvidence{StatusCode: 429}, "upstream_rate_limit"},
		{"SSE model code", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "model_not_found"}, "upstream_model_or_request"},
		{"SSE request type", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "invalid_request_error"}, "upstream_model_or_request"},
		{"SSE overload", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "overloaded_error"}, "upstream_rate_limit"},
		{"SSE auth", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "authentication_error"}, "upstream_auth"},
		{"SSE generic error remains unknown", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "api_error"}, "unknown"},
		{"SSE unknown code remains unknown", FailureEvidence{StatusCode: 200, Stage: "body", UpstreamCode: "unmapped_provider_code"}, "unknown"},
		{"header code alone is insufficient", FailureEvidence{StatusCode: 200, Stage: "headers", UpstreamCode: "invalid_request_error"}, "unknown"},
		{"529", FailureEvidence{StatusCode: 529}, "upstream_rate_limit"},
		{"401", FailureEvidence{StatusCode: 401}, "upstream_auth"},
		{"403", FailureEvidence{StatusCode: 403}, "upstream_auth"},
		{"400 code", FailureEvidence{StatusCode: 400, UpstreamCode: "invalid_model"}, "upstream_model_or_request"},
		{"404 code", FailureEvidence{StatusCode: 404, UpstreamCode: "invalid_model"}, "upstream_model_or_request"},
		{"400 no code", FailureEvidence{StatusCode: 400}, "unknown"},
		{"connect timeout", FailureEvidence{Stage: "connect", TransportKind: "timeout", ProxyUsed: true}, "upstream_connect_timeout"},
		{"headers timeout", FailureEvidence{Stage: "headers", TransportKind: "timeout"}, "upstream_read_timeout"},
		{"body timeout", FailureEvidence{Stage: "body", TransportKind: "timeout"}, "upstream_read_timeout"},
		{"proxy refused", FailureEvidence{Stage: "connect", TransportKind: "refused", ProxyUsed: true}, "proxy_unreachable"},
		{"proxy dns", FailureEvidence{Stage: "connect", TransportKind: "dns", ProxyUsed: true}, "proxy_unreachable"},
		{"proxy tls", FailureEvidence{Stage: "connect", TransportKind: "tls", ProxyUsed: true}, "proxy_unreachable"},
		{"origin refused", FailureEvidence{Stage: "connect", TransportKind: "refused"}, "unknown"},
		{"body abort", FailureEvidence{Stage: "body_read", ClientCanceled: true}, "client_abort_during_body_read"},
		{"response abort", FailureEvidence{Stage: "response", ClientCanceled: true}, "client_abort_during_response"},
		{"panic", FailureEvidence{Stage: "panic", DBError: true, RateLimited: true}, "gateway_panic"},
		{"db", FailureEvidence{DBError: true, RateLimited: true}, "gateway_db"},
		{"ratelimit", FailureEvidence{RateLimited: true}, "gateway_ratelimit"},
		{"empty", FailureEvidence{}, "unknown"},
		{"normal eof", FailureEvidence{Stage: "response", TransportKind: "eof"}, "unknown"},
		{"unexpected eof", FailureEvidence{Stage: "body_read", TransportKind: "eof"}, "unknown"},
		{"status beats db", FailureEvidence{StatusCode: 429, DBError: true, RateLimited: true}, "upstream_rate_limit"},
		{"auth beats timeout", FailureEvidence{StatusCode: 403, Stage: "body", TransportKind: "timeout"}, "upstream_auth"},
		{"code beats timeout", FailureEvidence{StatusCode: 400, UpstreamCode: "bad", Stage: "body", TransportKind: "timeout"}, "upstream_model_or_request"},
		{"server beats timeout", FailureEvidence{StatusCode: 503, Stage: "body", TransportKind: "timeout"}, "upstream_server_error"},
		{"abort beats db", FailureEvidence{Stage: "response", ClientCanceled: true, DBError: true}, "client_abort_during_response"},
	}
	for _, status := range []int{500, 502, 503, 504} {
		tests = append(tests, struct {
			name string
			ev   FailureEvidence
			want string
		}{fmt.Sprintf("origin %d", status), FailureEvidence{StatusCode: status}, "upstream_server_error"})
		tests = append(tests, struct {
			name string
			ev   FailureEvidence
			want string
		}{fmt.Sprintf("origin via proxy %d", status), FailureEvidence{StatusCode: status, ProxyUsed: true, Stage: "headers"}, "upstream_server_error"})
		tests = append(tests, struct {
			name string
			ev   FailureEvidence
			want string
		}{fmt.Sprintf("ambiguous proxy %d", status), FailureEvidence{StatusCode: status, ProxyUsed: true}, "unknown"})
		if status != 500 {
			tests = append(tests, struct {
				name string
				ev   FailureEvidence
				want string
			}{fmt.Sprintf("proxy %d", status), FailureEvidence{StatusCode: status, ProxyUsed: true, Stage: "connect"}, "proxy_unreachable"})
		}
	}
	reasons := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ClassifyFailure("upstream_error", tt.ev))
			require.Empty(t, ClassifyFailure("success", tt.ev))
		})
		reasons[tt.want] = true
	}
	require.Len(t, reasons, 13)
}

func TestClassifyRejectedStatus(t *testing.T) {
	tests := []struct {
		status     int
		wantResult string
		wantReason string
	}{
		{200, "", ""},
		{399, "", ""},
		{400, "rejected", "client_bad_request"},
		{401, "rejected", "client_auth_failed"},
		{403, "rejected", "client_forbidden"},
		{404, "rejected", "client_not_found"},
		{413, "rejected", "body_too_large"},
		{429, "rejected", "gateway_ratelimit"},
		{499, "client_abort", "client_abort"},
		{405, "rejected", "unknown"},
		{422, "rejected", "unknown"},
		{500, "gateway_error", "unknown"},
		{502, "gateway_error", "unknown"},
	}
	for _, tc := range tests {
		result, reason := ClassifyRejectedStatus(tc.status)
		require.Equal(t, tc.wantResult, result, "status=%d", tc.status)
		require.Equal(t, tc.wantReason, reason, "status=%d", tc.status)
	}
}
