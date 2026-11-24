package monitoring

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsP1_ErrorCodeSanitizationAndPlainTextRedaction(t *testing.T) {
	t.Run("sanitizeOpsErrorCode rejects JSON shapes, normalizes whitespace, redacts secrets, and truncates", func(t *testing.T) {
		require.Empty(t, sanitizeOpsErrorCode(""))
		require.Empty(t, sanitizeOpsErrorCode("   \t\n "))
		require.Empty(t, sanitizeOpsErrorCode(`{"secret":"sk-abc"}`))
		require.Empty(t, sanitizeOpsErrorCode(`["array_code"]`))

		got := sanitizeOpsErrorCode("rate_limit\n  Bearer sk-secret1234567890")
		require.NotContains(t, got, "\n")
		require.NotContains(t, got, "sk-secret1234567890")
		require.Contains(t, got, "Bearer ***")

		longCode := strings.Repeat("c", 200)
		truncated := sanitizeOpsErrorCode(longCode)
		require.LessOrEqual(t, len(truncated), 128)
		require.True(t, strings.HasSuffix(truncated, "..."))
	})

	t.Run("redactOpsSecrets redacts extended key=value secrets while preserving benign diagnostics", func(t *testing.T) {
		input := strings.Join([]string{
			"refresh_token=rt_secret_val_12345",
			"access_token=at_secret_val_12345",
			"client_secret=cs_secret_val_12345",
			"id_token=idt_secret_val_12345",
			"session_token=sess_secret_val_12345",
			"api_secret=apisec_val_12345",
			"password=pw_secret_val_12345",
			"password: pw_header_secret_12345",
			"passwd=pw2_secret_val_12345",
			"private_key=pk_secret_val_12345",
			"token: expired",
			"secret: missing",
		}, " ")
		got := redactOpsSecrets(input)
		for _, leaked := range []string{
			"rt_secret_val_12345",
			"at_secret_val_12345",
			"cs_secret_val_12345",
			"idt_secret_val_12345",
			"sess_secret_val_12345",
			"apisec_val_12345",
			"pw_secret_val_12345",
			"pw_header_secret_12345",
			"pw2_secret_val_12345",
			"pk_secret_val_12345",
		} {
			require.NotContains(t, got, leaked)
		}
		require.Contains(t, got, "token: expired")
		require.Contains(t, got, "secret: missing")

		jsonInput := `{"access_token":"abc123","refresh_token":"def456","client_secret":"ghi789","note":"ok"}`
		jsonGot := redactOpsSecrets(jsonInput)
		for _, leaked := range []string{"abc123", "def456", "ghi789"} {
			require.NotContains(t, jsonGot, leaked)
		}
		require.Contains(t, jsonGot, `"note":"ok"`)
	})

	t.Run("RecordErrorDetails drops raw JSON codes and keeps redacted error codes", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })

		h.r.RecordErrorDetails("upstream failure", http.StatusBadGateway, `{"raw":"json"}`)
		require.Empty(t, h.r.upstreamErrorCode)

		h.r.RecordErrorDetails("upstream failure refresh_token=rt_hidden_999", http.StatusTooManyRequests, "rate_limit Bearer sk-secret999 refresh_token=rt_code_secret999")
		h.body()
		h.clock.at(40)
		h.r.Finalize(FinalizeInput{Status: http.StatusBadGateway})

		require.Len(t, samples, 1)
		require.NotContains(t, *samples[0].ErrorMessage, "rt_hidden_999")
		require.NotContains(t, *samples[0].UpstreamErrorCode, "sk-secret999")
		require.NotContains(t, *samples[0].UpstreamErrorCode, "rt_code_secret999")
		require.Contains(t, *samples[0].UpstreamErrorCode, "Bearer ***")
		require.Contains(t, *samples[0].UpstreamErrorCode, "refresh_token=***")
	})
}

func TestOpsP1_ClassifyStreamFailureAndMarkFailureIfUnset(t *testing.T) {
	t.Run("ClassifyStreamFailure maps upstream, intended, code, and unknown cases", func(t *testing.T) {
		res, reason, ev := ClassifyStreamFailure(http.StatusTooManyRequests, 0, "rate_limit_exceeded")
		require.Equal(t, "upstream_error", res)
		require.Equal(t, "upstream_rate_limit", reason)
		require.Equal(t, http.StatusTooManyRequests, ev.StatusCode)
		require.Equal(t, "rate_limit_exceeded", ev.UpstreamCode)

		res, reason, ev = ClassifyStreamFailure(0, http.StatusPaymentRequired, "")
		require.Equal(t, "rejected", res)
		require.NotEmpty(t, reason)
		require.Empty(t, ev.UpstreamCode)

		res, reason, ev = ClassifyStreamFailure(0, 0, "overloaded_error")
		require.Equal(t, "upstream_error", res)
		require.NotEqual(t, "unknown", reason)
		require.Equal(t, http.StatusOK, ev.StatusCode)
		require.Equal(t, "overloaded_error", ev.UpstreamCode)

		res, reason, _ = ClassifyStreamFailure(0, 0, `{"invalid":"code"}`)
		require.Equal(t, "gateway_error", res)
		require.Equal(t, "unknown", reason)
	})

	t.Run("HTTP 200 committed stream settles as non-success without mutating http_status 200", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
		h.body()
		h.clock.at(40)

		res, reason, ev := ClassifyStreamFailure(0, http.StatusTooManyRequests, "")
		h.r.MarkFailureIfUnset(res, reason, ev)
		// Second call must not overwrite an existing failure classification.
		h.r.MarkFailureIfUnset("gateway_error", "unknown", FailureEvidence{})
		h.r.Finalize(FinalizeInput{Status: http.StatusOK})

		require.Len(t, samples, 1)
		require.Equal(t, 200, *samples[0].HTTPStatus)
		require.Equal(t, res, *samples[0].Result)
		require.Equal(t, reason, *samples[0].FailureReason)
		require.NotEqual(t, "success", *samples[0].Result)

		allMetrics := readRecorderMetrics(t, h.registry)
		var matched float64
		for _, rec := range allMetrics["sub2api_gateway_requests_total"] {
			if rec.labels["result"] == res && rec.labels["failure_reason"] == reason {
				matched += rec.counter
			}
		}
		require.EqualValues(t, 1, matched)
	})
}

func TestOpsP2_ErrorScopingFallbackAndMergeMetaIfMissing(t *testing.T) {
	t.Run("attempt 2 transport error clears attempt 1 HTTP status and ignores stale fallback status", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
		h.body()

		a1 := h.send()
		h.response(a1, http.StatusBadRequest, false)
		h.r.RecordErrorDetails("attempt 1 bad request", http.StatusBadRequest, "bad_request")
		h.end(a1, "upstream_error", "upstream_model_or_request", FailureEvidence{StatusCode: http.StatusBadRequest}, false)

		h.r.MarkSwitch()
		a2 := h.send()
		h.r.RecordErrorDetails("dial tcp 10.0.0.1:443: i/o timeout", 0, "")
		// Fallback handler with stale Gin context status from attempt 1 must not override attempt 2 transport failure.
		h.r.RecordFallbackErrorDetails("attempt 1 bad request", http.StatusBadRequest, "bad_request")
		h.end(a2, "timeout", "upstream_connect_timeout", FailureEvidence{Stage: "headers"}, false)

		h.r.Finalize(FinalizeInput{Status: http.StatusGatewayTimeout})
		require.Len(t, samples, 1)
		if samples[0].UpstreamStatus != nil {
			require.Zero(t, *samples[0].UpstreamStatus)
		}
		require.Empty(t, samples[0].UpstreamErrorCode)
		require.Contains(t, *samples[0].ErrorMessage, "dial tcp")
	})

	t.Run("MergeMetaIfMissing fills missing fields without clobbering fallback group", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })

		h.r.MergeMeta(OpsRequestMeta{
			GroupID:   2002,
			GroupName: "fallback-group",
			Model:     "claude-fallback",
		})
		h.r.MergeMetaIfMissing(OpsRequestMeta{
			RequestID:  "req-meta-fill",
			HTTPMethod: http.MethodPost,
			UserID:     99,
			UserEmail:  "fallback@example.com",
			GroupID:    1001,
			GroupName:  "stale-primary-group",
			Model:      "stale-model",
			Endpoint:   "/v1/messages",
		})
		h.body()
		h.clock.at(40)
		h.r.Finalize(FinalizeInput{Status: http.StatusBadGateway})

		require.Len(t, samples, 1)
		require.EqualValues(t, 2002, *samples[0].GroupID)
		require.Equal(t, "fallback-group", *samples[0].GroupName)
		require.Equal(t, "claude-fallback", *samples[0].Model)
		// newRecorderHarness pre-populated UserID=1; MergeMetaIfMissing preserves it while filling missing fields.
		require.EqualValues(t, 1, *samples[0].UserID)
		require.Equal(t, "fallback@example.com", *samples[0].UserEmail)
		require.Equal(t, "POST", *samples[0].HTTPMethod)
		require.Equal(t, "req-meta-fill", samples[0].RequestID)
		require.Equal(t, "/v1/messages", *samples[0].Endpoint)
	})
}

func TestOpsP2_LiveWSMetricsAndPreInit(t *testing.T) {
	h := newRecorderHarness(t)

	allMetrics := readRecorderMetrics(t, h.registry)
	endpoints := []string{"responses", "realtime", "live"}
	outcomes := []string{"connected", "closed_normal", "closed_error", "rejected", "handshake_failed"}
	for _, ep := range endpoints {
		foundActive := false
		for _, rec := range allMetrics["sub2api_ws_active_connections"] {
			if rec.labels["endpoint"] == ep && rec.gauge == 0 {
				foundActive = true
			}
		}
		require.True(t, foundActive, "missing pre-initialized active gauge for endpoint=%s", ep)

		for _, outcome := range outcomes {
			foundTotal := false
			for _, rec := range allMetrics["sub2api_ws_connections_total"] {
				if rec.labels["endpoint"] == ep && rec.labels["outcome"] == outcome {
					foundTotal = true
				}
			}
			require.True(t, foundTotal, "missing pre-initialized total counter for endpoint=%s outcome=%s", ep, outcome)
		}
	}

	obs := NewWSObserver(h.m, "live")
	require.NotNil(t, obs)
	ctx := WithWSObserver(context.Background(), obs)
	require.Same(t, obs, WSObserverFromContext(ctx))

	obs.MarkConnected()
	obs.MarkClosed(true)

	after := readRecorderMetrics(t, h.registry)
	for _, wantOutcome := range []string{"connected", "closed_normal"} {
		var val float64
		for _, rec := range after["sub2api_ws_connections_total"] {
			if rec.labels["endpoint"] == "live" && rec.labels["outcome"] == wantOutcome {
				val = rec.counter
			}
		}
		require.EqualValues(t, 1, val, "expected outcome=%s to equal 1 for live endpoint", wantOutcome)
	}
}

func TestOpsP2_TruncateHugeLimitSafe(t *testing.T) {
	require.Equal(t, "short", truncateOpsErrorMessage("short", 1_000_000_000))
	require.Empty(t, truncateOpsErrorMessage("short", 0))
}

func TestOpsR2_SingleQuotedEscapedRedactionAndAttemptPriority(t *testing.T) {
	t.Run("redacts single-quoted, escaped-quoted, and escaped JSON credentials without known prefixes", func(t *testing.T) {
		msg := strings.Join([]string{
			`refresh_token='opaque-single-98765'`,
			`refresh_token='a\'b-opaque'`,
			`client_secret="c\"d-opaque"`,
			`token: 'opaque-colon-single'`,
			`{\"refresh_token\":\"escaped-opaque-54321\"}`,
			`token: expired`,
			`secret: missing`,
		}, " ")
		gotMsg := sanitizeOpsErrorMessage(msg)
		for _, leaked := range []string{
			"opaque-single-98765",
			"b-opaque",
			"d-opaque",
			"opaque-colon-single",
			"escaped-opaque-54321",
		} {
			require.NotContains(t, gotMsg, leaked)
		}
		require.Contains(t, gotMsg, "token: expired")
		require.Contains(t, gotMsg, "secret: missing")

		gotCode := sanitizeOpsErrorCode(`refresh_token='opaque-code-77889'`)
		require.NotContains(t, gotCode, "opaque-code-77889")
		require.Contains(t, gotCode, "refresh_token=***")
	})

	t.Run("MarkFailureIfUnset preserves concrete completed attempt reason over fallback status", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
		h.body()
		a := h.send()
		h.response(a, http.StatusOK, true)
		h.end(a, "upstream_error", "upstream_model_or_request", FailureEvidence{
			StatusCode:   http.StatusOK,
			Stage:        "body",
			UpstreamCode: "invalid_request_error",
		}, false)

		// Fallback 403 -> upstream_auth must NOT overwrite concrete upstream_model_or_request.
		res, reason, ev := ClassifyStreamFailure(http.StatusForbidden, http.StatusBadGateway, "upstream_error")
		h.r.MarkFailureIfUnset(res, reason, ev)
		h.r.Finalize(FinalizeInput{Status: http.StatusOK})

		require.Len(t, samples, 1)
		require.Equal(t, "upstream_error", *samples[0].Result)
		require.Equal(t, "upstream_model_or_request", *samples[0].FailureReason)
	})
}
