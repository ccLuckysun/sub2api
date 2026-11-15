package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type recorderClock struct{ millis atomic.Int64 }

func (c *recorderClock) now() time.Time {
	return time.Unix(1700000000, 0).Add(time.Duration(c.millis.Load()) * time.Millisecond)
}
func (c *recorderClock) at(ms int64) { c.millis.Store(ms) }

type recorderHarness struct {
	t        *testing.T
	clock    *recorderClock
	registry *prometheus.Registry
	m        *OpsMetrics
	r        *OpsRecorder
	coverage *OpsCoverage
}

func newRecorderHarness(t *testing.T) *recorderHarness {
	t.Helper()
	h := &recorderHarness{t: t, clock: &recorderClock{}, registry: prometheus.NewPedanticRegistry()}
	var err error
	h.m, err = NewOpsMetrics(h.registry)
	require.NoError(t, err)
	h.coverage = NewOpsCoverage(h.m.Coverage, h.clock.now)
	ctx := WithOpsRecorderDependencies(context.Background(), NewAccountLabels([]string{"Account.A", "Account B"}), h.coverage)
	ctx, h.r = beginGatewayRequestAt(ctx, "/v1/messages", h.m, h.clock.now)
	require.Same(t, h.r, RecorderFromContext(ctx))
	h.r.AttachMeta(OpsRequestMeta{Model: "gpt-CODEX", UserID: 1, APIKeyID: 2})
	return h
}
func (h *recorderHarness) body() {
	h.clock.at(10)
	h.r.MarkBodyRead(10, 999, "complete")
	h.r.AddBytes("req_decoded", 12, "complete")
	h.clock.at(15)
	h.r.AddWait(5 * time.Millisecond)
}
func (h *recorderHarness) send() *OpsAttempt {
	h.clock.at(20)
	a := h.r.StartAttempt("Account.A")
	a.Sent()
	h.r.AddBytes("req_upstream", 20, "complete")
	return a
}
func (h *recorderHarness) response(a *OpsAttempt, status int, first bool) {
	h.clock.at(50)
	a.NoteHeaders(status)
	a.UseResponse()
	if first {
		h.clock.at(70)
		a.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 30, "complete")
		h.r.AddBytes("resp_downstream", 25, "complete")
	}
}
func (h *recorderHarness) end(a *OpsAttempt, result, reason string, ev FailureEvidence, usage bool) {
	h.clock.at(100)
	a.BodyDone(nil)
	a.Done(result, reason, ev)
	if usage {
		h.clock.at(110)
		h.r.MarkFinish()
	}
}

type metricRecord struct {
	labels  map[string]string
	counter float64
	count   uint64
	sum     float64
	gauge   float64
}

func readRecorderMetrics(t *testing.T, reg *prometheus.Registry) map[string][]metricRecord {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	all := make(map[string][]metricRecord)
	for _, family := range families {
		for _, metric := range family.Metric {
			value := metricRecord{labels: map[string]string{}}
			for _, label := range metric.Label {
				value.labels[label.GetName()] = label.GetValue()
			}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				value.counter = metric.Counter.GetValue()
			case dto.MetricType_HISTOGRAM:
				value.count = metric.Histogram.GetSampleCount()
				value.sum = metric.Histogram.GetSampleSum()
			case dto.MetricType_GAUGE:
				value.gauge = metric.Gauge.GetValue()
			}
			all[family.GetName()] = append(all[family.GetName()], value)
		}
	}
	return all
}
func counters(records []metricRecord) float64 {
	n := 0.0
	for _, r := range records {
		n += r.counter
	}
	return n
}
func metricWithLabels(t *testing.T, all map[string][]metricRecord, name string, labels map[string]string) metricRecord {
	t.Helper()
	for _, r := range all[name] {
		if fmt.Sprint(r.labels) == fmt.Sprint(labels) {
			return r
		}
	}
	t.Fatalf("missing %s%v; got %+v", name, labels, all[name])
	return metricRecord{}
}

type expectedAttempt struct {
	account, result, reason string
	ttftMS                  int64
}
type expectedRecorder struct {
	result, reason, outcome string
	phases                  [6]int64 // body_read,wait,gateway,upstream,response,finish (milliseconds)
	total                   int64
	headers                 int64 // -1 means no observation
	attempts                []expectedAttempt
	bytes                   [5]int64 // in_wire,req_decoded,req_upstream,resp_upstream,resp_downstream
	bodyN                   int64    // -1 means MarkBodyRead never called
	switches                int
	abort                   string
	coverage                CoverageSample
}

func normalExpected() expectedRecorder {
	return expectedRecorder{
		result: "success", outcome: "first_success", phases: [6]int64{10, 5, 5, 30, 50, 10}, total: 110, headers: 50,
		attempts: []expectedAttempt{{"accounta", "success", "", 50}}, bytes: [5]int64{10, 12, 20, 30, 25}, bodyN: 10, coverage: CoverageSample{true, true, true, true},
	}
}
func assertRecorderSettlement(t *testing.T, h *recorderHarness, w expectedRecorder) {
	t.Helper()
	all := readRecorderMetrics(t, h.registry)
	phases := []string{"body_read", "wait", "gateway", "upstream", "response", "finish"}
	total := 0.0
	for i, phase := range phases {
		m := metricWithLabels(t, all, "sub2api_request_phase_seconds", map[string]string{"phase": phase})
		require.EqualValues(t, 1, m.count, phase)
		require.InDelta(t, float64(w.phases[i])/1000, m.sum, 1e-9, phase)
		require.GreaterOrEqual(t, m.sum, 0.0)
		total += m.sum
	}
	require.InDelta(t, float64(w.total)/1000, total, .001, "six phases must close, without headers_end")
	if w.headers < 0 {
		require.Len(t, all["sub2api_request_phase_seconds"], 6)
	} else {
		require.Len(t, all["sub2api_request_phase_seconds"], 7)
		m := metricWithLabels(t, all, "sub2api_request_phase_seconds", map[string]string{"phase": "headers_end"})
		require.EqualValues(t, 1, m.count)
		require.InDelta(t, float64(w.headers)/1000, m.sum, 1e-9)
	}
	for i, dir := range []string{"in_wire", "req_decoded", "req_upstream", "resp_upstream", "resp_downstream"} {
		n := 0.0
		for _, m := range all["sub2api_transfer_bytes_total"] {
			if m.labels["direction"] == dir {
				n = m.counter
			}
		}
		require.Equal(t, float64(w.bytes[i]), n, dir)
	}
	require.Equal(t, float64(len(w.attempts)), counters(all["sub2api_upstream_requests_total"]))
	require.Equal(t, float64(len(w.attempts)), counters(all["sub2api_model_requests_total"]))
	expectedUp := map[string]int{}
	expectedModel := map[string]int{}
	ttfts := map[string][2]float64{}
	for _, a := range w.attempts {
		expectedUp[a.account+"/"+a.result+"/"+a.reason]++
		expectedModel[a.result]++
		if a.ttftMS >= 0 {
			v := ttfts[a.account]
			v[0]++
			v[1] += float64(a.ttftMS) / 1000
			ttfts[a.account] = v
		}
	}
	for key, n := range expectedUp {
		got := 0.0
		for _, m := range all["sub2api_upstream_requests_total"] {
			if m.labels["account"]+"/"+m.labels["result"]+"/"+m.labels["failure_reason"] == key {
				got = m.counter
			}
		}
		require.Equal(t, float64(n), got, key)
	}
	for result, n := range expectedModel {
		m := metricWithLabels(t, all, "sub2api_model_requests_total", map[string]string{"model_class": "codex", "result": result})
		require.Equal(t, float64(n), m.counter)
	}
	require.Len(t, all["sub2api_upstream_ttft_seconds"], len(ttfts))
	for account, want := range ttfts {
		m := metricWithLabels(t, all, "sub2api_upstream_ttft_seconds", map[string]string{"account": account})
		require.EqualValues(t, want[0], m.count)
		require.InDelta(t, want[1], m.sum, 1e-9)
	}
	require.Len(t, all["sub2api_attempts_total"], 1)
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_attempts_total", map[string]string{"outcome": w.outcome}).counter)
	require.Len(t, all["sub2api_gateway_requests_total"], 1)
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_gateway_requests_total", map[string]string{"result": w.result, "failure_reason": w.reason}).counter)
	require.Equal(t, float64(w.switches), counters(all["sub2api_account_switches_total"]))
	if w.abort == "" {
		require.Empty(t, all["sub2api_client_abort_total"])
	} else {
		require.Len(t, all["sub2api_client_abort_total"], 1)
		require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_client_abort_total", map[string]string{"stage": w.abort}).counter)
	}
	if w.bodyN < 0 {
		require.Empty(t, all["sub2api_request_body_bytes"])
	} else {
		require.Len(t, all["sub2api_request_body_bytes"], 1)
		m := all["sub2api_request_body_bytes"][0]
		require.EqualValues(t, 1, m.count)
		require.Equal(t, float64(w.bodyN), m.sum)
		require.Equal(t, "responses", m.labels["endpoint_class"])
	}
	// Coherent M-9 sample: one denominator, with all numerators from this request.
	want := coverageCounts{entry: 1}
	if w.coverage.Request {
		want.request = 1
	}
	if w.coverage.Account {
		want.account = 1
	}
	if w.coverage.Bytes {
		want.bytes = 1
	}
	if w.coverage.User {
		want.user = 1
	}
	require.Equal(t, want, h.coverage.counts)
}

func TestOpsRecorderSettlementMatrix(t *testing.T) {
	type testCase struct {
		name  string
		run   func(*recorderHarness)
		input FinalizeInput
		want  expectedRecorder
	}
	normal := normalExpected()
	cases := []testCase{{"normal_200", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, true)
	}, FinalizeInput{Status: 200}, normal}}
	for _, spec := range []struct {
		name           string
		status         int
		result, reason string
		ev             FailureEvidence
	}{
		{"429", 429, "upstream_error", "upstream_rate_limit", FailureEvidence{StatusCode: 429, DBError: true, RateLimited: true}},
		{"500", 500, "upstream_error", "upstream_server_error", FailureEvidence{StatusCode: 500}},
		{"502_origin", 502, "upstream_error", "upstream_server_error", FailureEvidence{StatusCode: 502, ProxyUsed: true, Stage: "headers"}},
		{"200_then_read_timeout", 200, "timeout", "upstream_read_timeout", FailureEvidence{Stage: "body", TransportKind: "timeout"}},
	} {
		w := normalExpected()
		w.result = spec.result
		w.reason = spec.reason
		w.outcome = "all_failed"
		w.attempts[0].result = spec.result
		w.attempts[0].reason = spec.reason
		cases = append(cases, testCase{spec.name, func(h *recorderHarness) {
			h.body()
			a := h.send()
			h.response(a, spec.status, true)
			h.end(a, spec.result, "untrusted_reason", spec.ev, true)
		}, FinalizeInput{Status: spec.status}, w})
	}
	for _, spec := range []struct {
		name, result, reason string
		ev                   FailureEvidence
	}{
		{"connect_timeout", "timeout", "upstream_connect_timeout", FailureEvidence{Stage: "connect", TransportKind: "timeout"}},
		{"read_headers_timeout", "timeout", "upstream_read_timeout", FailureEvidence{Stage: "headers", TransportKind: "timeout"}},
		{"proxy_connection_refused", "upstream_error", "proxy_unreachable", FailureEvidence{Stage: "connect", TransportKind: "refused", ProxyUsed: true}},
		{"origin_connection_refused", "upstream_error", "unknown", FailureEvidence{Stage: "connect", TransportKind: "refused"}},
		{"502_proxy", "upstream_error", "proxy_unreachable", FailureEvidence{Stage: "connect", StatusCode: 502, ProxyUsed: true}},
	} {
		w := normalExpected()
		w.result = spec.result
		w.reason = spec.reason
		w.outcome = "all_failed"
		w.phases = [6]int64{10, 5, 5, 80, 0, 0}
		w.total = 100
		w.headers = -1
		w.bytes = [5]int64{10, 12, 20, 0, 0}
		w.coverage.Bytes = false
		w.attempts = []expectedAttempt{{"accounta", spec.result, spec.reason, -1}}
		cases = append(cases, testCase{spec.name, func(h *recorderHarness) { h.body(); a := h.send(); h.clock.at(100); a.Done(spec.result, "", spec.ev) }, FinalizeInput{Status: 502}, w})
	}
	w := normalExpected()
	w.phases = [6]int64{10, 5, 5, 30, 950, 10}
	w.total = 1010
	cases = append(cases, testCase{"200_SSE", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.clock.at(1000)
		a.BodyDone(nil)
		a.Done("success", "stale", FailureEvidence{})
		h.clock.at(1010)
		h.r.MarkFinish()
	}, FinalizeInput{Status: 200}, w})
	w = normalExpected()
	w.phases[5] = 0
	w.total = 100
	cases = append(cases, testCase{"no_usage", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, false)
	}, FinalizeInput{Status: 200}, w})
	w = normalExpected()
	w.phases[5] = 15
	w.total = 115
	cases = append(cases, testCase{"cross_goroutine_finish", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, false)
		h.clock.at(115)
		done := make(chan struct{})
		go func() { h.r.MarkFinish(); close(done) }()
		<-done
	}, FinalizeInput{Status: 200}, w})
	w = normalExpected()
	w.result = "gateway_error"
	w.reason = "gateway_panic"
	w.outcome = "all_failed"
	cases = append(cases, testCase{"panic_after_200_preserves_attempt", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, true)
	}, FinalizeInput{Status: 200, Panic: true}, w})
	w = normalExpected()
	w.result = "client_abort"
	w.reason = "client_abort_during_response"
	w.outcome = "all_failed"
	w.abort = "response"
	w.phases[5] = 0
	w.total = 100
	w.bytes[4] = 7
	w.coverage.Bytes = false
	w.attempts = []expectedAttempt{{"accounta", "client_abort", "client_abort_during_response", 50}}
	cases = append(cases, testCase{"client_response_abort", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, false)
		h.clock.at(70)
		a.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 30, "complete")
		h.r.AddBytes("resp_downstream", 7, "partial")
		h.clock.at(100)
		h.r.MarkClientAbort("response")
		h.r.MarkClientAbort("response")
		a.BodyDone(errors.New("broken pipe"))
		a.Done("client_abort", "", FailureEvidence{Stage: "response", ClientCanceled: true})
	}, FinalizeInput{Status: 200}, w})
	w = expectedRecorder{result: "client_abort", reason: "client_abort_during_body_read", outcome: "all_failed", phases: [6]int64{10, 0, 0, 0, 0, 0}, total: 10, headers: -1, bytes: [5]int64{3, 0, 0, 0, 0}, bodyN: 3, abort: "body_read", coverage: CoverageSample{Request: true, User: true}}
	cases = append(cases, testCase{"client_body_read_abort", func(h *recorderHarness) {
		h.clock.at(10)
		h.r.MarkBodyRead(3, 999, "partial")
		h.r.MarkClientAbort("body_read")
	}, FinalizeInput{Status: 400}, w})
	missingBody := w
	missingBody.bytes = [5]int64{}
	missingBody.bodyN = -1
	cases = append(cases, testCase{"body_abort_without_byte_observation", func(h *recorderHarness) {
		h.clock.at(10)
		h.r.MarkClientAbort("body_read")
	}, FinalizeInput{Status: 400}, missingBody})
	w.result = "gateway_error"
	w.reason = "unknown"
	w.abort = ""
	cases = append(cases, testCase{"unexpected_EOF_not_abort", func(h *recorderHarness) { h.clock.at(10); h.r.MarkBodyRead(3, 999, "partial") }, FinalizeInput{Status: 200}, w})
	w = expectedRecorder{result: "rejected", reason: "gateway_ratelimit", outcome: "all_failed", phases: [6]int64{0, 0, 120, 0, 0, 0}, total: 120, headers: -1, bodyN: -1, coverage: CoverageSample{Request: true, User: true}}
	cases = append(cases, testCase{"zero_attempt_rejection", func(h *recorderHarness) { h.r.AddWait(time.Second) }, FinalizeInput{Status: 429}, w})
	w.result = "gateway_error"
	w.reason = "gateway_panic"
	cases = append(cases, testCase{"panic_before_body", func(h *recorderHarness) {}, FinalizeInput{Status: 500, Panic: true}, w})
	w = expectedRecorder{result: "gateway_error", reason: "gateway_db", outcome: "all_failed", phases: [6]int64{10, 5, 35, 0, 0, 0}, total: 50, headers: -1, bodyN: 10, bytes: [5]int64{10, 12, 0, 0, 0}, coverage: CoverageSample{Request: true, User: true}}
	cases = append(cases, testCase{"body_complete_failure_before_send", func(h *recorderHarness) {
		h.body()
		h.clock.at(50)
		h.r.MarkFailure("gateway_error", "", FailureEvidence{DBError: true})
	}, FinalizeInput{Status: 500}, w})
	for _, name := range []string{"signature_correction", "grok_fallback"} {
		w = normalExpected()
		w.phases = [6]int64{10, 5, 5, 60, 20, 10}
		w.headers = 70
		w.bytes = [5]int64{10, 12, 40, 35, 25}
		cases = append(cases, testCase{name, func(h *recorderHarness) {
			h.body()
			a := h.send()
			h.clock.at(30)
			a.NoteHeaders(400)
			h.clock.at(35)
			a.NoteFirstByte()
			h.r.AddBytes("resp_upstream", 5, "complete")
			a.BodyDone(nil)
			h.clock.at(40)
			a.Sent()
			h.r.AddBytes("req_upstream", 20, "complete")
			require.Equal(t, time.Duration(20)*time.Millisecond, a.sentAt.Sub(h.r.entry))
			require.Equal(t, time.Duration(40)*time.Millisecond, a.lastSentAt.Sub(h.r.entry))
			h.clock.at(80)
			a.NoteHeaders(200)
			a.UseResponse()
			h.clock.at(90)
			a.NoteFirstByte()
			h.r.AddBytes("resp_upstream", 30, "complete")
			h.r.AddBytes("resp_downstream", 25, "complete")
			h.end(a, "success", "", FailureEvidence{}, true)
		}, FinalizeInput{Status: 200}, w})
		cases[len(cases)-1].want.attempts[0].ttftMS = 70
	}
	w = normalExpected()
	cases = append(cases, testCase{"duplicate_Done_Finalize_body", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, true)
		h.r.MarkBodyRead(999, 999, "partial")
		a.Done("timeout", "", FailureEvidence{Stage: "body", TransportKind: "timeout"})
		h.clock.at(120)
		h.r.Finalize(FinalizeInput{Status: 200})
		h.r.MarkSwitch()
		h.r.AddBytes("in_wire", 999, "complete")
		h.r.MarkClientAbort("response")
		h.r.MarkFinish()
		a.NoteFirstByte()
	}, FinalizeInput{Status: 500, Panic: true}, w})
	w = normalExpected()
	w.result = "upstream_error"
	w.reason = "unknown"
	w.outcome = "all_failed"
	w.phases[5] = 0
	w.total = 100
	w.attempts[0].result = "upstream_error"
	w.attempts[0].reason = "unknown"
	w.attempts[0].ttftMS = -1
	cases = append(cases, testCase{"unfinished_attempt_body_done", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.clock.at(100)
		a.BodyDone(nil)
	}, FinalizeInput{Status: 200}, w})
	w = normalExpected()
	w.result = "upstream_error"
	w.reason = "unknown"
	w.outcome = "all_failed"
	w.phases = [6]int64{10, 5, 5, 100, 0, 0}
	w.total = 120
	w.headers = -1
	w.bytes = [5]int64{10, 12, 20, 0, 0}
	w.coverage.Bytes = false
	w.attempts = []expectedAttempt{{"accounta", "upstream_error", "unknown", -1}}
	cases = append(cases, testCase{"unfinished_transport_attempt", func(h *recorderHarness) { h.body(); h.send() }, FinalizeInput{Status: 502}, w})
	w = normalExpected()
	w.headers = -1
	w.attempts[0].ttftMS = -1
	w.bytes = [5]int64{10, 12, 20, 0, 0}
	w.coverage.Bytes = false
	cases = append(cases, testCase{"empty_response_no_TTFT", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.response(a, 200, false)
		h.end(a, "success", "", FailureEvidence{}, true)
	}, FinalizeInput{Status: 200}, w})
	// A successful logical retry counts twice, unlike physical internal resends.
	w = normalExpected()
	w.phases = [6]int64{10, 5, 5, 60, 20, 10}
	w.headers = 30
	w.attempts = []expectedAttempt{{"accounta", "upstream_error", "upstream_server_error", 15}, {"accountb", "success", "", 30}}
	w.outcome = "retried_success"
	w.bytes = [5]int64{10, 12, 40, 35, 25}
	w.switches = 1
	cases = append(cases, testCase{"logical_retry_success", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.clock.at(30)
		a.NoteHeaders(500)
		a.UseResponse()
		h.clock.at(35)
		a.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 5, "complete")
		h.clock.at(40)
		a.BodyDone(nil)
		a.Done("upstream_error", "", FailureEvidence{StatusCode: 500})
		h.r.MarkSwitch()
		h.clock.at(60)
		b := h.r.StartAttempt("Account B")
		b.Sent()
		h.r.AddWait(time.Second)
		h.r.AddBytes("req_upstream", 20, "complete")
		h.clock.at(80)
		b.NoteHeaders(200)
		b.UseResponse()
		h.clock.at(90)
		b.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 30, "complete")
		h.r.AddBytes("resp_downstream", 25, "complete")
		h.end(b, "success", "", FailureEvidence{}, true)
	}, FinalizeInput{Status: 200}, w})
	w = normalExpected()
	w.result = "timeout"
	w.reason = "upstream_connect_timeout"
	w.outcome = "all_failed"
	w.phases = [6]int64{10, 5, 5, 80, 0, 0}
	w.total = 100
	w.headers = -1
	w.bytes = [5]int64{10, 12, 40, 5, 0}
	w.coverage.Bytes = false
	w.switches = 1
	w.attempts = []expectedAttempt{{"accounta", "upstream_error", "upstream_server_error", 15}, {"accountb", "timeout", "upstream_connect_timeout", -1}}
	cases = append(cases, testCase{"retry_transport_failure_drops_previous_headers", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.clock.at(30)
		a.NoteHeaders(500)
		a.UseResponse()
		h.clock.at(35)
		a.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 5, "complete")
		h.clock.at(40)
		a.BodyDone(nil)
		a.Done("upstream_error", "", FailureEvidence{StatusCode: 500})
		h.r.MarkSwitch()
		h.clock.at(60)
		b := h.r.StartAttempt("Account B")
		b.Sent()
		h.r.AddBytes("req_upstream", 20, "complete")
		h.clock.at(100)
		b.Done("timeout", "", FailureEvidence{Stage: "connect", TransportKind: "timeout"})
	}, FinalizeInput{Status: 502}, w})
	w = normalExpected()
	w.headers = -1
	w.attempts[0].ttftMS = -1
	cases = append(cases, testCase{"unlocked_first_byte_no_TTFT", func(h *recorderHarness) {
		h.body()
		a := h.send()
		h.clock.at(50)
		a.NoteHeaders(200)
		h.clock.at(70)
		a.NoteFirstByte()
		h.r.AddBytes("resp_upstream", 30, "complete")
		h.r.AddBytes("resp_downstream", 25, "complete")
		h.end(a, "success", "", FailureEvidence{}, true)
	}, FinalizeInput{Status: 200}, w})

	w = expectedRecorder{result: "success", outcome: "first_success", phases: [6]int64{0, 0, 120, 0, 0, 0}, total: 120, headers: -1, bodyN: -1, coverage: CoverageSample{Request: true, User: true}}
	cases = append(cases, testCase{"zero_attempt_local_success", func(h *recorderHarness) {}, FinalizeInput{Status: 200}, w})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecorderHarness(t)
			tc.run(h)
			if h.clock.millis.Load() < 120 {
				h.clock.at(120)
			}
			h.r.Finalize(tc.input)
			h.r.Finalize(tc.input)
			assertRecorderSettlement(t, h, tc.want)
			t.Logf("M1=%v total=%dms headers_end=%dms attempts=%d M6=%s M10=%s/%s PASS", tc.want.phases, tc.want.total, tc.want.headers, len(tc.want.attempts), tc.want.outcome, tc.want.result, tc.want.reason)
		})
	}
}

func TestOpsAttemptFrozenResponseEvents(t *testing.T) {
	for _, firstBeforeLock := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_before_lock_%t", firstBeforeLock), func(t *testing.T) {
			h := newRecorderHarness(t)
			h.body()
			a := h.send()
			h.clock.at(50)
			a.NoteHeaders(200)
			if firstBeforeLock {
				h.clock.at(60)
				a.NoteFirstByte()
			}
			a.UseResponse()
			if !firstBeforeLock {
				h.clock.at(60)
				a.NoteFirstByte()
			}
			h.clock.at(65)
			a.NoteFirstByte()
			a.NoteHeaders(500)
			a.UseResponse()
			h.clock.at(80)
			a.BodyDone(nil)
			// An unrelated physical response cannot replace any frozen timestamp.
			h.clock.at(90)
			a.Sent()
			a.NoteHeaders(400)
			a.NoteFirstByte()
			a.BodyDone(errors.New("foreign response"))
			a.UseResponse()
			h.clock.at(100)
			a.Done("success", "", FailureEvidence{})
			h.clock.at(110)
			h.r.MarkFinish()
			h.clock.at(120)
			h.r.Finalize(FinalizeInput{Status: 200})
			w := normalExpected()
			w.phases = [6]int64{10, 5, 5, 30, 30, 30}
			w.headers = 40
			w.attempts[0].ttftMS = 40
			w.bytes = [5]int64{10, 12, 20, 0, 0}
			w.coverage.Bytes = false
			assertRecorderSettlement(t, h, w)
		})
	}
	t.Run("foreign_first_byte_does_not_fill_locked_response", func(t *testing.T) {
		h := newRecorderHarness(t)
		h.body()
		a := h.send()
		h.clock.at(50)
		a.NoteHeaders(200)
		a.UseResponse()
		h.clock.at(60)
		a.Sent()
		a.NoteHeaders(500)
		a.NoteFirstByte()
		h.clock.at(100)
		a.Done("success", "", FailureEvidence{})
		h.clock.at(120)
		h.r.Finalize(FinalizeInput{Status: 200})
		w := normalExpected()
		w.phases[5] = 0
		w.total = 100
		w.headers = -1
		w.attempts[0].ttftMS = -1
		w.bytes = [5]int64{10, 12, 20, 0, 0}
		w.coverage.Bytes = false
		assertRecorderSettlement(t, h, w)
	})
}

func TestOpsRecorderWaitClampingAndAccumulation(t *testing.T) {
	h := newRecorderHarness(t)
	h.r.AddWait(time.Second) // Before body is measured: ignored.
	h.clock.at(10)
	h.r.MarkBodyRead(10, 999, "complete")
	h.clock.at(15)
	h.r.AddWait(3 * time.Millisecond)
	h.r.AddWait(3 * time.Millisecond) // Clamps to 5 ms total.
	h.clock.at(20)
	a := h.r.StartAttempt("unlisted")
	a.Sent()
	h.r.AddWait(time.Hour)
	h.r.AddWait(-time.Hour)
	h.r.AddBytes("req_upstream", 2, "complete")
	h.r.AddBytes("req_upstream", 3, "complete")
	h.r.MarkSwitch()
	h.r.MarkSwitch()
	h.clock.at(50)
	a.NoteHeaders(200)
	a.UseResponse()
	h.clock.at(70)
	a.NoteFirstByte()
	h.clock.at(100)
	a.BodyDone(nil)
	a.Done("success", "not_a_label", FailureEvidence{StatusCode: 500})
	h.clock.at(120)
	h.r.Finalize(FinalizeInput{Status: 200})
	w := normalExpected()
	w.phases[5] = 0
	w.total = 100
	w.attempts[0].account = "unknown"
	w.bytes = [5]int64{10, 0, 5, 0, 0}
	w.switches = 2
	w.coverage.Account = false
	w.coverage.Bytes = false
	assertRecorderSettlement(t, h, w)
}

func TestOpsRecorderReasonPriority(t *testing.T) {
	// Request-level failures compete in the same order as FailureEvidence;
	// Recovery's explicit panic marker remains the forced exception.
	for _, panicFlag := range []bool{false, true} {
		h := newRecorderHarness(t)
		h.body()
		a := h.send()
		h.response(a, 429, true)
		h.end(a, "upstream_error", "gateway_db", FailureEvidence{StatusCode: 429}, false)
		h.r.MarkFailure("gateway_error", "", FailureEvidence{DBError: true, RateLimited: true})
		h.r.MarkClientAbort("response")
		h.clock.at(120)
		h.r.Finalize(FinalizeInput{Status: 429, Panic: panicFlag})
		all := readRecorderMetrics(t, h.registry)
		result, reason := "upstream_error", "upstream_rate_limit"
		if panicFlag {
			result, reason = "gateway_error", "gateway_panic"
		}
		require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_gateway_requests_total", map[string]string{"result": result, "failure_reason": reason}).counter)
		require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_upstream_requests_total", map[string]string{"account": "accounta", "result": "upstream_error", "failure_reason": "upstream_rate_limit"}).counter)
	}
	for _, reason := range []string{"", "arbitrary_user_value"} {
		h := newRecorderHarness(t)
		h.body()
		a := h.send()
		h.clock.at(100)
		a.Done("not_a_result", reason, FailureEvidence{})
		h.clock.at(120)
		h.r.Finalize(FinalizeInput{Status: 500})
		all := readRecorderMetrics(t, h.registry)
		require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_upstream_requests_total", map[string]string{"account": "accounta", "result": "gateway_error", "failure_reason": "unknown"}).counter)
	}
}

func TestOpsRecorderCoverageAndLabelBoundaries(t *testing.T) {
	h := newRecorderHarness(t)
	h.r.AttachMeta(OpsRequestMeta{Model: "private-name", UserID: 1, APIKeyID: 0, Endpoint: "/v1/chat/completions", EndpointClass: "arbitrary"})
	h.clock.at(10)
	h.r.MarkBodyRead(0, 9999, "complete")
	for _, dir := range []string{"req_upstream", "resp_upstream", "resp_downstream"} {
		h.r.AddBytes(dir, 0, "complete")
	}
	h.r.AddBytes("resp_downstream", 3, "partial")
	h.r.AddBytes("resp_downstream", 2, "complete")
	h.r.AddBytes("malicious_direction", 1, "complete")
	h.r.AddBytes("in_wire", -1, "complete")
	h.r.MarkClientAbort("not_a_stage")
	h.clock.at(20)
	a := h.r.StartAttempt("arbitrary-account")
	a.Sent()
	h.clock.at(100)
	a.Done("upstream_error", "", FailureEvidence{})
	h.r.Finalize(FinalizeInput{Status: 502})
	all := readRecorderMetrics(t, h.registry)
	require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
	require.Equal(t, 0.0, all["sub2api_request_body_bytes"][0].sum)
	require.Equal(t, "chat", all["sub2api_request_body_bytes"][0].labels["endpoint_class"])
	require.Equal(t, "other", all["sub2api_model_requests_total"][0].labels["model_class"])
	require.Equal(t, "unknown", all["sub2api_upstream_requests_total"][0].labels["account"])
	require.Len(t, all["sub2api_transfer_bytes_total"], 4)
	require.Empty(t, all["sub2api_client_abort_total"])
	require.Equal(t, coverageCounts{entry: 1, request: 1}, h.coverage.counts)
	// Declaration never substitutes for an actual zero-byte read.
	require.Equal(t, int64(9999), h.r.declared)
	h.clock.at(60000)
	h.coverage.Flush()
	all = readRecorderMetrics(t, h.registry)
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": "request"}).gauge)
	for _, kind := range []string{"account", "bytes", "user"} {
		require.Equal(t, 0.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": kind}).gauge)
	}
}

func TestOpsRecorderNilAndDisabled(t *testing.T) {
	var r *OpsRecorder
	r.AttachMeta(OpsRequestMeta{})
	r.MarkBodyRead(1, 1, "complete")
	r.AddWait(time.Second)
	require.Nil(t, r.StartAttempt("any"))
	r.AddBytes("in_wire", 1, "complete")
	r.MarkClientAbort("body_read")
	r.MarkSwitch()
	r.MarkFinish()
	r.MarkFailure("gateway_error", "", FailureEvidence{})
	r.Finalize(FinalizeInput{})
	var a *OpsAttempt
	a.Sent()
	a.NoteHeaders(200)
	a.NoteFirstByte()
	a.UseResponse()
	a.BodyDone(nil)
	a.Done("success", "", FailureEvidence{})
	a = &OpsAttempt{}
	a.Sent()
	a.Done("success", "", FailureEvidence{})
	require.Nil(t, RecorderFromContext(nil))
	require.Nil(t, RecorderFromContext(context.Background()))
	ctx := context.WithValue(context.Background(), recorderDependenciesKey{}, "unrelated")
	same, disabled := BeginGatewayRequest(ctx, "/v1/messages", nil)
	require.Same(t, ctx, same)
	require.Nil(t, disabled)
	nonnil, disabled := BeginGatewayRequest(nil, "/v1/messages", nil)
	require.NotNil(t, nonnil)
	require.Nil(t, disabled)
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	ctx, r = BeginGatewayRequest(nil, "/v1/messages", m)
	require.Same(t, r, RecorderFromContext(ctx))
	a = r.StartAttempt("raw-account")
	require.Equal(t, "unknown", a.account)
	r.Finalize(FinalizeInput{Status: 401})
	// A partially supplied metrics bundle cannot panic due to missing collectors.
	_, r = BeginGatewayRequest(context.Background(), "/v1/messages", &OpsMetrics{})
	r.MarkBodyRead(1, 1, "complete")
	r.AddBytes("in_wire", 1, "complete")
	r.MarkSwitch()
	r.MarkClientAbort("body_read")
	a = r.StartAttempt("any")
	a.Done("success", "", FailureEvidence{})
	r.Finalize(FinalizeInput{Status: 200})
}

func TestOpsRecorderConcurrentSettlement(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	h.clock.at(100)
	a.BodyDone(nil)
	h.clock.at(110)
	var wg sync.WaitGroup
	// Every caller races the same settlement boundaries, using one stable clock.
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Done("success", "", FailureEvidence{})
			h.r.MarkFinish()
			h.r.Finalize(FinalizeInput{Status: 200})
			a.Done("timeout", "", FailureEvidence{Stage: "body", TransportKind: "timeout"})
			h.r.MarkBodyRead(999, 999, "partial")
			h.r.AddWait(time.Second)
			h.r.AddBytes("in_wire", 1, "complete")
			h.r.MarkSwitch()
			h.r.AttachMeta(OpsRequestMeta{Model: "claude"})
			a.NoteHeaders(500)
			a.NoteFirstByte()
			a.UseResponse()
			a.BodyDone(nil)
		}()
	}
	wg.Wait()
	w := normalExpected()
	assertRecorderSettlement(t, h, w)
	require.Nil(t, h.r.StartAttempt("Account.A"))
}

func TestOpsRecorderConcurrentFinishCutoff(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	h.end(a, "success", "", FailureEvidence{}, false)
	h.clock.at(110)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); h.r.MarkFinish() }()
		go func() { defer wg.Done(); h.r.Finalize(FinalizeInput{Status: 200}) }()
	}
	wg.Wait()
	all := readRecorderMetrics(t, h.registry)
	finish := metricWithLabels(t, all, "sub2api_request_phase_seconds", map[string]string{"phase": "finish"})
	require.EqualValues(t, 1, finish.count)
	require.Contains(t, []float64{0, .01}, finish.sum, "linearizable cutoff includes only finish events that won the mutex before Finalize")
	require.Equal(t, 1.0, counters(all["sub2api_gateway_requests_total"]))
	require.Equal(t, 1.0, counters(all["sub2api_upstream_requests_total"]))
}

func TestOpsRecorderObservationBoundaries(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	assertAbsent := func(names ...string) {
		t.Helper()
		all := readRecorderMetrics(t, h.registry)
		for _, name := range names {
			require.Empty(t, all[name], name)
		}
	}
	attemptNames := []string{"sub2api_upstream_requests_total", "sub2api_model_requests_total", "sub2api_upstream_ttft_seconds"}
	requestNames := []string{"sub2api_request_phase_seconds", "sub2api_gateway_requests_total", "sub2api_attempts_total"}
	assertAbsent(attemptNames...)
	assertAbsent(requestNames...)
	h.clock.at(100)
	a.BodyDone(nil)
	assertAbsent(attemptNames...)
	assertAbsent(requestNames...)
	a.Done("success", "", FailureEvidence{})
	all := readRecorderMetrics(t, h.registry)
	require.Equal(t, 1.0, counters(all[attemptNames[0]]))
	require.Equal(t, 1.0, counters(all[attemptNames[1]]))
	require.EqualValues(t, 1, all[attemptNames[2]][0].count)
	assertAbsent(requestNames...)
	h.clock.at(110)
	h.r.MarkFinish()
	h.clock.at(115)
	h.r.MarkFinish()
	h.clock.at(120)
	h.r.Finalize(FinalizeInput{Status: 200})
	assertRecorderSettlement(t, h, normalExpected())
}

func TestOpsRecorderEntryCoverageBeforeFinalize(t *testing.T) {
	h := newRecorderHarness(t)
	require.Equal(t, coverageCounts{entry: 1}, h.coverage.counts, "Begin increments entry immediately")
	h.clock.at(60000)
	h.coverage.Flush()
	all := readRecorderMetrics(t, h.registry)
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.Equal(t, 0.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": kind}).gauge, "unfinalized request is a real denominator")
	}
	h.clock.at(60010)
	h.r.Finalize(FinalizeInput{Status: 200})
	require.Equal(t, coverageCounts{}, h.coverage.counts, "late completion does not create a new entry or numerator in completion minute")
	require.Equal(t, coverageCounts{entry: 1, request: 1, user: 1}, h.coverage.previous)
	all = readRecorderMetrics(t, h.registry)
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": "request"}).gauge, "late completion revises published entry-minute ratio")
	h.clock.at(120000)
	h.coverage.Flush()
	all = readRecorderMetrics(t, h.registry)
	require.Equal(t, -1.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": "request"}).gauge, "completion minute itself has no entries")

	expired := newRecorderHarness(t)
	expired.clock.at(120000)
	expired.coverage.Flush()
	expired.r.Finalize(FinalizeInput{Status: 200})
	require.Equal(t, coverageCounts{}, expired.coverage.counts)
	require.Equal(t, coverageCounts{}, expired.coverage.previous)
	all = readRecorderMetrics(t, expired.registry)
	require.Equal(t, -1.0, metricWithLabels(t, all, "sub2api_metric_coverage_ratio", map[string]string{"kind": "request"}).gauge, "expired completion cannot rewrite an unrelated minute")
}

func TestOpsResponseHandleSurvivesLaterSent(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	h.clock.at(20)
	a := h.r.StartAttempt("Account.A")
	responseA := a.Sent()
	h.r.AddBytes("req_upstream", 20, "complete")
	require.NotNil(t, responseA)
	h.clock.at(50)
	a.NoteHeadersFor(responseA, 200)
	a.UseResponseFor(responseA)
	h.clock.at(60)
	responseB := a.Sent()
	h.r.AddBytes("req_upstream", 20, "complete")
	require.NotSame(t, responseA, responseB)
	h.clock.at(70)
	a.NoteHeadersFor(responseB, 500)
	a.UseResponseFor(responseB)
	a.NoteFirstByteFor(responseB)
	a.NoteHeadersFor(nil, 500)
	a.NoteFirstByteFor(nil)
	a.BodyDoneFor(nil, nil)
	a.UseResponseFor(nil)
	foreign := newRecorderHarness(t)
	foreign.body()
	other := foreign.send()
	foreignResponse := other.Sent()
	a.NoteHeadersFor(foreignResponse, 500)
	a.NoteFirstByteFor(foreignResponse)
	a.BodyDoneFor(foreignResponse, nil)
	a.UseResponseFor(foreignResponse)
	h.clock.at(75)
	a.BodyDoneFor(responseB, errors.New("foreign body"))
	h.clock.at(80)
	a.NoteFirstByteFor(responseA)
	h.r.AddBytes("resp_upstream", 30, "complete")
	h.r.AddBytes("resp_downstream", 25, "complete")
	h.clock.at(100)
	a.BodyDoneFor(responseA, nil)
	h.clock.at(105)
	a.Done("success", "", FailureEvidence{})
	h.clock.at(110)
	h.r.MarkFinish()
	h.clock.at(120)
	h.r.Finalize(FinalizeInput{Status: 200})
	w := normalExpected()
	w.bytes[2] = 40
	w.headers = 60
	w.attempts[0].ttftMS = 60
	assertRecorderSettlement(t, h, w)
	require.Same(t, responseA, a.locked)
	require.Equal(t, 200, responseA.status)
	require.True(t, responseB.headersAt.IsZero())
	require.True(t, responseB.firstAt.IsZero())
	require.True(t, responseB.bodyAt.IsZero())
	require.Equal(t, 80*time.Millisecond, responseA.firstAt.Sub(h.r.entry))
	require.Equal(t, 100*time.Millisecond, responseA.bodyAt.Sub(h.r.entry))
}

func TestOpsResponseHandleConcurrentEvents(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	h.clock.at(20)
	a := h.r.StartAttempt("Account.A")
	responseA := a.Sent()
	h.clock.at(50)
	a.NoteHeadersFor(responseA, 200)
	a.UseResponseFor(responseA)
	h.clock.at(60)
	responseB := a.Sent()
	h.clock.at(80)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.NoteHeadersFor(responseB, 500)
			a.NoteFirstByteFor(responseB)
			a.BodyDoneFor(responseB, errors.New("discarded"))
			a.UseResponseFor(responseB)
			a.NoteFirstByteFor(responseA)
			a.BodyDoneFor(responseA, nil)
		}()
	}
	wg.Wait()
	h.clock.at(100)
	a.Done("success", "", FailureEvidence{})
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	ttft := metricWithLabels(t, all, "sub2api_upstream_ttft_seconds", map[string]string{"account": "accounta"})
	require.EqualValues(t, 1, ttft.count)
	require.InDelta(t, .06, ttft.sum, 1e-9)
	require.Equal(t, 80*time.Millisecond, a.locked.bodyAt.Sub(h.r.entry))
	require.True(t, responseB.bodyAt.IsZero())
	var disabled *OpsAttempt
	require.Nil(t, disabled.Sent())
	disabled.NoteHeadersFor(responseA, 500)
	disabled.NoteFirstByteFor(responseA)
	disabled.BodyDoneFor(responseA, nil)
	disabled.UseResponseFor(responseA)
}

func TestOpsAttemptFallbackDoesNotObserveTTFT(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	h.clock.at(100)
	a.BodyDone(nil)
	h.clock.at(120)
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	require.Empty(t, all["sub2api_upstream_ttft_seconds"], "Finalize fallback never observes M-11 even with a locked first byte")
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_upstream_requests_total", map[string]string{"account": "accounta", "result": "upstream_error", "failure_reason": "unknown"}).counter)
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_model_requests_total", map[string]string{"model_class": "codex", "result": "upstream_error"}).counter)
	headers := metricWithLabels(t, all, "sub2api_request_phase_seconds", map[string]string{"phase": "headers_end"})
	require.EqualValues(t, 1, headers.count)
	require.InDelta(t, .05, headers.sum, 1e-9, "M-1 is distinct from explicit-Done-only M-11")
	a.Done("success", "", FailureEvidence{})
	all = readRecorderMetrics(t, h.registry)
	require.Empty(t, all["sub2api_upstream_ttft_seconds"], "late explicit Done cannot reopen a finalized attempt")
}
