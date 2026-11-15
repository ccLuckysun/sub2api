package monitoring

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsUsageLogFieldsSnapshot(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	h.end(a, "success", "", FailureEvidence{}, false)
	h.clock.at(120)
	before := readRecorderMetrics(t, h.registry)
	body, latency := h.r.UsageLogFields()
	require.EqualValues(t, 10, *body)
	require.EqualValues(t, 30, *latency, "gateway 5 + wait 5 + finish-at-snapshot 20")
	require.True(t, h.r.finishAt.IsZero(), "getter must not mark usage submission finished")
	require.Equal(t, before, readRecorderMetrics(t, h.registry), "read-only getter changes no metric")
	h.clock.at(150)
	h.r.MarkFinish()
	h.r.Finalize(FinalizeInput{Status: 200})
	require.EqualValues(t, 30, *latency, "frozen value excludes subsequent Submit/fallback time")
	require.EqualValues(t, 10, *body)
	phases := readRecorderMetrics(t, h.registry)["sub2api_request_phase_seconds"]
	finish := -1.0
	for _, record := range phases {
		if record.labels["phase"] == "finish" {
			finish = record.sum
		}
	}
	require.InDelta(t, .05, finish, .000001, "M1 retains true finish 50ms, rather than snapshot 20ms")
}
func TestOpsUsageLogFieldsNullVersusZero(t *testing.T) {
	var disabled *OpsRecorder
	body, latency := disabled.UsageLogFields()
	require.Nil(t, body)
	require.Nil(t, latency)
	h := newRecorderHarness(t)
	h.clock.at(10)
	body, latency = h.r.UsageLogFields()
	require.Nil(t, body)
	require.Nil(t, latency)
	h.r.Finalize(FinalizeInput{Status: 401})
	body, latency = h.r.UsageLogFields()
	require.Nil(t, body)
	require.Nil(t, latency, "unread zero-attempt rejection is not observed latency")
	zero := newRecorderHarness(t)
	zero.r.MarkBodyRead(0, 0, "complete")
	body, latency = zero.r.UsageLogFields()
	require.NotNil(t, body)
	require.Zero(t, *body)
	require.Nil(t, latency)
	a := zero.r.StartAttempt("Account.A")
	a.Sent()
	a.NoteHeaders(200)
	a.UseResponse()
	a.NoteFirstByte()
	body, latency = zero.r.UsageLogFields()
	require.NotNil(t, body)
	require.Nil(t, latency, "no fabricated t_done while response is in flight")
	a.BodyDone(nil)
	a.Done("success", "", FailureEvidence{})
	body, latency = zero.r.UsageLogFields()
	require.Zero(t, *body)
	require.NotNil(t, latency)
	require.Zero(t, *latency, "a measured 0ms is distinct from missing")
	partial := newRecorderHarness(t)
	partial.r.MarkBodyRead(3, -1, "partial")
	body, latency = partial.r.UsageLogFields()
	require.EqualValues(t, 3, *body)
	require.Nil(t, latency)
}
func TestOpsUsageLogFieldsSSEExcludesStreamDuration(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.response(a, 200, true)
	h.clock.at(60050)
	a.BodyDone(nil)
	a.Done("success", "", FailureEvidence{})
	h.clock.at(60070)
	body, latency := h.r.UsageLogFields()
	require.EqualValues(t, 10, *body)
	require.EqualValues(t, 30, *latency, "60s streaming response is excluded from local gateway latency")
	*body = 999
	again, _ := h.r.UsageLogFields()
	require.EqualValues(t, 10, *again, "returned scalar does not alias Recorder state")
}
func TestOpsUsageLogFieldsNormalRequestCoverage(t *testing.T) {
	populated := 0
	for i := 0; i < 100; i++ {
		h := newRecorderHarness(t)
		h.body()
		a := h.send()
		h.response(a, 200, true)
		h.end(a, "success", "", FailureEvidence{}, false)
		h.clock.at(100 + int64(i))
		body, latency := h.r.UsageLogFields()
		if body != nil && latency != nil {
			populated++
		}
	}
	require.GreaterOrEqual(t, populated, 95)
	t.Logf("normal 200/SSE-capable request fixtures: %d/100 non-NULL (target >=95%%)", populated)
}
func TestOpsUsageLogFieldsTransportTerminalObserved(t *testing.T) {
	h := newRecorderHarness(t)
	h.body()
	a := h.send()
	h.clock.at(50)
	a.Done("timeout", "upstream_connect_timeout", FailureEvidence{})
	h.clock.at(70)
	body, latency := h.r.UsageLogFields()
	require.EqualValues(t, 10, *body)
	require.EqualValues(t, 30, *latency)
	require.Equal(t, time.Time{}, h.r.finishAt)
}
