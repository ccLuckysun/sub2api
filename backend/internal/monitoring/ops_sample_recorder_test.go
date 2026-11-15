package monitoring

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sampleSinkFunc func(OpsSample) bool

func (f sampleSinkFunc) Submit(sample OpsSample) bool { return f(sample) }

func TestOpsSampleRecorderFieldMapping(t *testing.T) {
	h := newRecorderHarness(t)
	var samples []OpsSample
	h.r.sampleSink = sampleSinkFunc(func(sample OpsSample) bool { samples = append(samples, sample); return true })
	meta := OpsRequestMeta{RequestID: "provided-request", ClientRequestID: "client-correlation", UserID: 11, APIKeyID: 12, AccountID: 13, GroupID: 14, Platform: "anthropic", Model: "claude-sonnet", Endpoint: "/v1/messages", EndpointClass: "responses"}
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
	require.EqualValues(t, 12, *sample.APIKeyID)
	require.EqualValues(t, 13, *sample.AccountID)
	require.EqualValues(t, 14, *sample.GroupID)
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
