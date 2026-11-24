package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type sampleSinkFunc func(OpsSample) bool

func (f sampleSinkFunc) Submit(sample OpsSample) bool { return f(sample) }

func TestOpsSampleRecorderFieldMapping(t *testing.T) {
	h := newRecorderHarness(t)
	var samples []OpsSample
	h.r.sampleSink = sampleSinkFunc(func(sample OpsSample) bool { samples = append(samples, sample); return true })
	meta := OpsRequestMeta{
		RequestID:       "provided-request",
		ClientRequestID: "client-correlation",
		UserID:          11,
		UserEmail:       "alice@example.com",
		APIKeyID:        12,
		APIKeyName:      "prod-key",
		APIKeyMasked:    "sk-ant-a…9z0x",
		AccountID:       13,
		AccountName:     "acct-primary",
		GroupID:         14,
		GroupName:       "vip-group",
		Platform:        "anthropic",
		Model:           "claude-sonnet",
		Endpoint:        "/v1/messages",
		EndpointClass:   "responses",
	}
	h.r.AttachMeta(meta)
	h.body()
	attempt := h.send()
	h.response(attempt, 200, true)
	h.end(attempt, "success", "", FailureEvidence{}, true)
	h.r.Finalize(FinalizeInput{Status: 200})
	require.Len(t, samples, 1)
	sample := samples[0]
	require.Equal(t, h.r.entry, sample.ObservedAt)
	require.Equal(t, meta.RequestID, sample.RequestID)
	require.Equal(t, "client-correlation", *sample.ClientRequestID)
	require.EqualValues(t, 11, *sample.UserID)
	require.Equal(t, "alice@example.com", *sample.UserEmail)
	require.EqualValues(t, 12, *sample.APIKeyID)
	require.Equal(t, "prod-key", *sample.APIKeyName)
	require.Equal(t, "sk-ant-a…9z0x", *sample.APIKeyMasked)
	require.EqualValues(t, 13, *sample.AccountID)
	require.Equal(t, "acct-primary", *sample.AccountName)
	require.EqualValues(t, 14, *sample.GroupID)
	require.Equal(t, "vip-group", *sample.GroupName)
	require.Equal(t, "anthropic", *sample.Platform)
	require.Equal(t, "claude-sonnet", *sample.Model)
	require.Equal(t, "/v1/messages", *sample.Endpoint)
	require.Equal(t, "responses", *sample.EndpointClass)
	require.Equal(t, "success", *sample.Result)
	require.Equal(t, "", *sample.FailureReason)
	require.Equal(t, 200, *sample.HTTPStatus)
	require.Equal(t, 1, *sample.AttemptCount)
	require.Zero(t, *sample.FailedAttempts)
	require.Equal(t, opsSampleNode, *sample.Node)
	require.NotEmpty(t, *sample.Node)
	for key, got := range map[string]*int{"body_read": sample.BodyReadMs, "gateway": sample.GatewayMs, "wait": sample.WaitMs, "upstream": sample.UpstreamMs, "headers_end": sample.HeadersEndMs, "response": sample.ResponseMs, "finish": sample.FinishMs, "total": sample.TotalMs} {
		want := map[string]int{"body_read": 10, "gateway": 5, "wait": 5, "upstream": 30, "headers_end": 50, "response": 50, "finish": 10, "total": 110}[key]
		require.NotNil(t, got, key)
		require.Equal(t, want, *got, key)
	}
	require.EqualValues(t, 999, *sample.BytesInDeclared)
	require.EqualValues(t, 10, *sample.BytesInWire)
	require.EqualValues(t, 12, *sample.BytesInDecoded)
	require.EqualValues(t, 20, *sample.BytesUpReqTotal)
	require.EqualValues(t, 30, *sample.BytesRespUpstream)
	require.EqualValues(t, 25, *sample.BytesRespDownstream)
	for _, status := range []*string{sample.BytesInWireStatus, sample.BytesUpReqTotalStatus, sample.BytesRespUpstreamStatus, sample.BytesRespDownstreamStatus} {
		require.Equal(t, "complete", *status)
	}
	require.Equal(t, "body_read", *sample.BytesInWireSource)
	require.Equal(t, "request_body_read", *sample.BytesUpReqTotalSource)
	require.Equal(t, "response_body_read", *sample.BytesRespUpstreamSource)
	require.Equal(t, "writer_size", *sample.BytesRespDownstreamSource)
	require.Nil(t, sample.CreatedAt, "DB owns created_at default")
	// Verify sub2api_gateway_request_duration_seconds observed the 110ms total duration.
	allMetrics := readRecorderMetrics(t, h.registry)
	durMetrics := allMetrics["sub2api_gateway_request_duration_seconds"]
	require.Len(t, durMetrics, 1)
	require.EqualValues(t, 1, durMetrics[0].count)
	require.InDelta(t, 0.110, durMetrics[0].sum, 1e-6)
}

func runSampleScenario(h *recorderHarness, name string) FinalizeInput {
	switch name {
	case "200":
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, true)
		return FinalizeInput{Status: 200}
	case "0-attempt":
		h.clock.at(20)
		return FinalizeInput{Status: 401}
	case "panic":
		h.body()
		h.clock.at(30)
		return FinalizeInput{Status: 500, Panic: true}
	case "abort":
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.clock.at(80)
		h.r.MarkClientAbort("response")
		h.end(a, "success", "", FailureEvidence{}, false)
		return FinalizeInput{Status: 200}
	case "partial bytes":
		h.clock.at(10)
		h.r.MarkBodyRead(3, -1, "partial")
		h.clock.at(20)
		return FinalizeInput{Status: 400}
	case "retry":
		h.body()
		a := h.send()
		h.response(a, 503, false)
		h.end(a, "upstream_error", "upstream_server_error", FailureEvidence{}, false)
		h.clock.at(110)
		a = h.r.StartAttempt("Account B")
		a.Sent()
		h.clock.at(120)
		a.NoteHeaders(200)
		a.UseResponse()
		h.clock.at(125)
		a.NoteFirstByte()
		h.clock.at(130)
		a.BodyDone(nil)
		a.Done("success", "", FailureEvidence{})
		return FinalizeInput{Status: 200}
	default:
		panic("invalid test scenario")
	}
}
func TestOpsSampleRecorderAllOutcomesAndUnchangedMetrics(t *testing.T) {
	for _, name := range []string{"200", "0-attempt", "panic", "abort", "partial bytes", "retry"} {
		t.Run(name, func(t *testing.T) {
			with := newRecorderHarness(t)
			without := newRecorderHarness(t)
			var samples []OpsSample
			with.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
			in := runSampleScenario(with, name)
			runSampleScenario(without, name)
			with.r.Finalize(in)
			without.r.Finalize(in)
			require.Len(t, samples, 1)
			require.Equal(t, readRecorderMetrics(t, without.registry), readRecorderMetrics(t, with.registry), "sample emission must not alter any M-1..M-11 metric")
			s := samples[0]
			result := map[string]string{"200": "success", "0-attempt": "rejected", "panic": "gateway_error", "abort": "client_abort", "partial bytes": "rejected", "retry": "success"}[name]
			require.Equal(t, result, *s.Result)
			require.Regexp(t, `^internal-[0-9]+-[0-9]+$`, s.RequestID)
			if name == "0-attempt" {
				require.Zero(t, *s.AttemptCount)
				require.Nil(t, s.HeadersEndMs)
				require.Nil(t, s.BytesInWire)
				require.Equal(t, "unknown", *s.BytesInWireStatus)
				require.Equal(t, 20, *s.TotalMs)
			}
			if name == "panic" {
				require.Equal(t, "gateway_panic", *s.FailureReason)
			}
			if name == "abort" {
				require.Equal(t, "client_abort_during_response", *s.FailureReason)
			}
			if name == "partial bytes" {
				require.EqualValues(t, 3, *s.BytesInWire)
				require.Equal(t, "partial", *s.BytesInWireStatus)
				require.Nil(t, s.BytesInDeclared)
				require.Nil(t, s.BytesRespUpstream)
			}
			if name == "retry" {
				require.Equal(t, 2, *s.AttemptCount)
				require.Equal(t, 1, *s.FailedAttempts)
			}
		})
	}
}
func TestOpsSampleRecorderRequestIDStableAndUnique(t *testing.T) {
	h := newRecorderHarness(t)
	id := h.r.RequestID()
	require.NotEmpty(t, id)
	require.Equal(t, id, h.r.RequestID())
	h.r.AttachMeta(OpsRequestMeta{RequestID: " "})
	require.Equal(t, id, h.r.RequestID())
	h.r.AttachMeta(OpsRequestMeta{RequestID: "external-id"})
	require.Equal(t, "external-id", h.r.RequestID())
	h.r.AttachMeta(OpsRequestMeta{})
	require.Equal(t, id, h.r.RequestID())
	other := newRecorderHarness(t)
	require.NotEqual(t, id, other.r.RequestID(), "sequence distinguishes equal entry unixnano")
	var disabled *OpsRecorder
	require.Empty(t, disabled.RequestID())
}
func TestOpsSampleRecorderReentrantFinalizeAndSinkPanic(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			h := newRecorderHarness(t)
			calls := 0
			h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool {
				calls++
				require.Equal(t, 1.0, counters(readRecorderMetrics(t, h.registry)["sub2api_gateway_requests_total"]), "metrics published before Submit")
				h.r.Finalize(FinalizeInput{Status: 500}) // No lock held during sink call.
				if panics {
					panic("injected sink failure")
				}
				return false
			})
			h.clock.at(10)
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); h.r.Finalize(FinalizeInput{Status: 200}) }()
			}
			wg.Wait()
			require.Equal(t, 1, calls)
		})
	}
}
func TestOpsSampleRecorderFullDelayedSinkIsNonblocking(t *testing.T) {
	// A delayed consumer never drains the bounded queue. The sink implements the
	// same nonblocking select contract as the real writer, without per-request Go routines.
	queue := make(chan OpsSample, 1)
	sink := sampleSinkFunc(func(s OpsSample) bool {
		select {
		case queue <- s:
			return true
		default:
			return false
		}
	})
	h := newRecorderHarness(t)
	h.r.sampleSink = sink
	h.r.Finalize(FinalizeInput{Status: 200})
	require.Len(t, queue, 1)
	full := newRecorderHarness(t)
	full.r.sampleSink = sink
	start := time.Now()
	full.r.Finalize(FinalizeInput{Status: 200})
	elapsed := time.Since(start)
	require.Less(t, elapsed, 100*time.Millisecond)
	require.Len(t, queue, 1)
	require.Equal(t, 1.0, counters(readRecorderMetrics(t, full.registry)["sub2api_gateway_requests_total"]))
	t.Logf("full sink with delayed consumer: Finalize=%s, metric settlement preserved", elapsed)
}
func TestOpsSampleRecorderDependenciesAndNilSink(t *testing.T) {
	h := newRecorderHarness(t)
	queue := make(chan OpsSample, 1)
	sink := sampleSinkFunc(func(s OpsSample) bool { queue <- s; return true })
	ctx := WithOpsRecorderDependencies(context.Background(), nil, h.coverage, sink)
	_, rec := beginGatewayRequestAt(ctx, "/v1/responses", h.m, h.clock.now)
	rec.Finalize(FinalizeInput{Status: 200})
	sample := <-queue
	require.Equal(t, "/v1/responses", *sample.Endpoint)
	require.Equal(t, "responses", *sample.EndpointClass)
	require.Nil(t, sample.UserID)
	require.Nil(t, sample.Model)
	require.True(t, regexp.MustCompile(`^internal-[0-9]+-[0-9]+$`).MatchString(sample.RequestID))
	h.r.Finalize(FinalizeInput{Status: 200}) // nil sink remains a no-op.
}

func TestOpsSampleRecorderHTTP101And4xxReasons(t *testing.T) {
	t.Run("http_101_success_suppresses_error_fields", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
		h.r.SetHTTPMethod("get")
		h.r.RecordErrorDetails("stale message", 502, "bad_gateway")
		h.r.Finalize(FinalizeInput{Status: http.StatusSwitchingProtocols})
		require.Len(t, samples, 1)
		s := samples[0]
		require.Equal(t, "success", *s.Result)
		require.Equal(t, "", *s.FailureReason)
		require.Equal(t, http.StatusSwitchingProtocols, *s.HTTPStatus)
		require.Equal(t, "GET", *s.HTTPMethod)
		require.Nil(t, s.ErrorMessage)
		require.Nil(t, s.UpstreamStatus)
		require.Nil(t, s.UpstreamErrorCode)
	})

	for _, tc := range []struct {
		status     int
		bodyAbort  bool
		wantResult string
		wantReason string
	}{
		{400, false, "rejected", "client_bad_request"},
		{401, false, "rejected", "client_auth_failed"},
		{403, false, "rejected", "client_forbidden"},
		{404, false, "rejected", "client_not_found"},
		{413, false, "rejected", "body_too_large"},
		{429, false, "rejected", "gateway_ratelimit"},
		{499, false, "client_abort", "client_abort"},
		{499, true, "client_abort", "client_abort_during_body_read"},
		{405, false, "rejected", "unknown"},
	} {
		t.Run(fmt.Sprintf("status_%d_abort_%v", tc.status, tc.bodyAbort), func(t *testing.T) {
			h := newRecorderHarness(t)
			var samples []OpsSample
			h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })
			if tc.bodyAbort {
				h.r.MarkClientAbort("body_read")
			}
			h.r.Finalize(FinalizeInput{Status: tc.status})
			require.Len(t, samples, 1)
			require.Equal(t, tc.wantResult, *samples[0].Result)
			require.Equal(t, tc.wantReason, *samples[0].FailureReason)
		})
	}
}

func TestOpsSampleRecorderMergeMetaAndSetHTTPMethod(t *testing.T) {
	h := newRecorderHarness(t)
	var samples []OpsSample
	h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })

	h.r.SetHTTPMethod("post")
	h.r.MergeMeta(OpsRequestMeta{
		RequestID:       "req-1",
		ClientRequestID: "client-1",
		UserID:          42,
		UserEmail:       "u@example.com",
		APIKeyID:        7,
		APIKeyName:      "k1",
		APIKeyMasked:    "sk-test1…9999",
		GroupID:         3,
		GroupName:       "g1",
		Platform:        "openai",
		Endpoint:        "/v1/responses",
	})
	// Subsequent empty MergeMeta must not clear existing values.
	h.r.MergeMeta(OpsRequestMeta{Model: "gpt-5"})
	// Wholesale AttachMeta must NOT wipe httpMethod.
	h.r.AttachMeta(OpsRequestMeta{
		RequestID: "req-2",
		UserID:    42,
		APIKeyID:  7,
		Model:     "gpt-5",
		Endpoint:  "/v1/responses",
	})
	h.r.Finalize(FinalizeInput{Status: 400})
	// Post-Finalize updates are ignored.
	h.r.SetHTTPMethod("DELETE")
	h.r.MergeMeta(OpsRequestMeta{Model: "ignored"})

	require.Len(t, samples, 1)
	s := samples[0]
	require.Equal(t, "POST", *s.HTTPMethod)
	require.Equal(t, "req-2", s.RequestID)
	require.Equal(t, "gpt-5", *s.Model)

	// Verify MergeMeta alone keeps EndpointClass & earlier fields intact.
	h2 := newRecorderHarness(t)
	var samples2 []OpsSample
	h2.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples2 = append(samples2, s); return true })
	h2.r.MergeMeta(OpsRequestMeta{
		RequestID: "req-merge",
		UserID:    99,
		APIKeyID:  88,
		Platform:  "composite",
		Endpoint:  "/v1/responses",
	})
	h2.r.MergeMeta(OpsRequestMeta{
		Platform:    "openai",
		AccountID:   55,
		AccountName: "acct-openai",
	})
	h2.r.Finalize(FinalizeInput{Status: 200})
	require.Len(t, samples2, 1)
	require.EqualValues(t, 99, *samples2[0].UserID)
	require.EqualValues(t, 88, *samples2[0].APIKeyID)
	require.Equal(t, "openai", *samples2[0].Platform)
	require.EqualValues(t, 55, *samples2[0].AccountID)
	require.Equal(t, "responses", *samples2[0].EndpointClass)
}

func TestOpsSampleRecorderRecordErrorDetailsPriorityAndConcurrency(t *testing.T) {
	t.Run("upstream_wins_and_retry_updates_latest", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })

		// Generic gateway message first, then upstream attempt 1, then upstream attempt 2 without code, then final generic wrapper.
		h.r.RecordErrorDetails("generic first", 0, "")
		h.r.RecordErrorDetails("generic second should not overwrite", 0, "")
		h.r.RecordErrorDetails("rate limited with sk-proj-secretkey1234", 429, "rate_limit_exceeded")
		h.r.RecordErrorDetails(strings.Repeat("错", 600), 503, "")
		h.r.RecordErrorDetails("Upstream request failed", 0, "ignored_code")

		a := h.r.StartAttempt("acct")
		a.Sent()
		a.Done("upstream_error", "upstream_server_error", FailureEvidence{StatusCode: 503})
		h.r.Finalize(FinalizeInput{Status: 503})

		require.Len(t, samples, 1)
		s := samples[0]
		require.NotNil(t, s.ErrorMessage)
		require.True(t, utf8.ValidString(*s.ErrorMessage))
		require.Equal(t, maxOpsErrorMessageRunes, utf8.RuneCountInString(*s.ErrorMessage))
		require.NotNil(t, s.UpstreamStatus)
		require.Equal(t, 503, *s.UpstreamStatus)
		require.Nil(t, s.UpstreamErrorCode, "stale upstream code from 429 must be cleared on 503 with empty code")
	})

	t.Run("generic_first_non_empty_preserved", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool { samples = append(samples, s); return true })

		h.r.RecordErrorDetails("API key is disabled sk-ant-secret12345", 0, "API_KEY_DISABLED")
		h.r.RecordErrorDetails("generic fallback wrapper", 0, "GENERIC")
		h.r.Finalize(FinalizeInput{Status: 401})

		require.Len(t, samples, 1)
		s := samples[0]
		require.NotNil(t, s.ErrorMessage)
		require.NotContains(t, *s.ErrorMessage, "sk-ant-secret12345")
		require.Contains(t, *s.ErrorMessage, "API key is disabled sk-***")
		require.Nil(t, s.UpstreamStatus)
		require.NotNil(t, s.UpstreamErrorCode)
		require.Equal(t, "API_KEY_DISABLED", *s.UpstreamErrorCode)
	})

	t.Run("concurrent_mutations_race_free", func(t *testing.T) {
		h := newRecorderHarness(t)
		var samples []OpsSample
		var mu sync.Mutex
		h.r.sampleSink = sampleSinkFunc(func(s OpsSample) bool {
			mu.Lock()
			samples = append(samples, s)
			mu.Unlock()
			return true
		})
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				h.r.SetHTTPMethod("POST")
				h.r.MergeMeta(OpsRequestMeta{RequestID: fmt.Sprintf("r-%d", idx), UserID: int64(idx + 1)})
				h.r.RecordErrorDetails(fmt.Sprintf("err %d Bearer sec-%d", idx, idx), 500+(idx%3), "err_code")
			}(i)
		}
		wg.Wait()
		h.r.Finalize(FinalizeInput{Status: 502})
		mu.Lock()
		require.Len(t, samples, 1)
		mu.Unlock()
	})
}
