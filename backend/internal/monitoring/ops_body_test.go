package monitoring

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type opsBodyResult struct {
	data string
	err  error
}
type opsScriptBody struct {
	results       []opsBodyResult
	reads, closes int
	closeErr      error
}

func (b *opsScriptBody) Read(p []byte) (int, error) {
	b.reads++
	if len(b.results) == 0 {
		return 0, io.EOF
	}
	result := b.results[0]
	b.results = b.results[1:]
	return copy(p, result.data), result.err
}
func (b *opsScriptBody) Close() error { b.closes++; return b.closeErr }

func TestOpsBodySettlement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		results   []opsBodyResult
		readTimes []int64
		finishAt  int64
		wantN     int64
		bodyAt    int64
		status    string
		cancel    bool
		abort     bool
		read      bool
	}{
		{name: "normal_EOF_with_bytes", results: []opsBodyResult{{"abc", nil}, {"de", io.EOF}}, readTimes: []int64{10, 20}, finishAt: 30, wantN: 5, bodyAt: 20, status: "complete", read: true},
		{name: "empty_EOF", results: []opsBodyResult{{"", io.EOF}}, readTimes: []int64{10}, finishAt: 30, wantN: 0, bodyAt: 10, status: "complete", read: true},
		{name: "read_error_keeps_partial_n", results: []opsBodyResult{{"abc", io.ErrUnexpectedEOF}}, readTimes: []int64{10}, finishAt: 30, wantN: 3, bodyAt: 10, status: "partial", read: true},
		{name: "unfinished_Finish_partial", results: []opsBodyResult{{"abc", nil}}, readTimes: []int64{10}, finishAt: 25, wantN: 3, bodyAt: 25, status: "partial", read: true},
		{name: "unread_rejection", finishAt: 30, wantN: 0, status: "unread", read: false},
		{name: "client_canceled", results: []opsBodyResult{{"ab", context.Canceled}}, readTimes: []int64{10}, finishAt: 30, wantN: 2, bodyAt: 10, status: "partial", cancel: true, abort: true, read: true},
		{name: "deadline_not_abort_even_if_context_canceled", results: []opsBodyResult{{"ab", os.ErrDeadlineExceeded}}, readTimes: []int64{10}, finishAt: 30, wantN: 2, bodyAt: 10, status: "partial", cancel: true, read: true},
		{name: "normal_EOF_not_abort_even_if_context_canceled", results: []opsBodyResult{{"ab", io.EOF}}, readTimes: []int64{10}, finishAt: 30, wantN: 2, bodyAt: 10, status: "complete", cancel: true, read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecorderHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := &opsScriptBody{results: append([]opsBodyResult(nil), tc.results...)}
			body := NewOpsBody(original, 999, ctx, h.r)
			require.Zero(t, original.reads, "constructor does not pre-read")
			for i, at := range tc.readTimes {
				h.clock.at(at)
				if tc.cancel {
					cancel()
				}
				buf := make([]byte, 20)
				n, err := body.Read(buf)
				require.Equal(t, tc.results[i].data, string(buf[:n]))
				require.True(t, tc.results[i].err == err, "Read must return the original error")
			}
			h.clock.at(tc.finishAt)
			body.Finish()
			body.Finish()
			require.Zero(t, original.closes, "Finish never closes underlying body")
			require.Equal(t, tc.abort, h.r.ClientAborted())
			h.clock.at(40)
			status := 200
			if !tc.read {
				status = 401
			}
			h.r.Finalize(FinalizeInput{Status: status})
			w := expectedRecorder{result: "success", outcome: "first_success", phases: [6]int64{tc.bodyAt, 0, 40 - tc.bodyAt, 0, 0, 0}, total: 40, headers: -1, bytes: [5]int64{tc.wantN, 0, 0, 0, 0}, bodyN: tc.wantN, coverage: CoverageSample{Request: true, User: true}}
			if tc.status == "partial" {
				w.result = "gateway_error"
				w.reason = "unknown"
				w.outcome = "all_failed"
				w.phases = [6]int64{tc.bodyAt, 0, 0, 0, 0, 0}
				w.total = tc.bodyAt
			}
			if !tc.read {
				w.result = "rejected"
				w.reason = "unknown"
				w.outcome = "all_failed"
				w.bodyN = -1
			}
			if tc.abort {
				w.result = "client_abort"
				w.reason = "client_abort_during_body_read"
				w.abort = "body_read"
			}
			assertRecorderSettlement(t, h, w)
			if tc.read {
				require.Equal(t, int64(999), h.r.declared)
				require.Equal(t, tc.status, h.r.bodyStatus)
			} else {
				require.False(t, h.r.bodyMarked)
			}
		})
	}
}

func TestOpsBodyCallbacksCannotChangeIO(t *testing.T) {
	originalErr := errors.New("original read error")
	closeErr := errors.New("original close error")
	original := &opsScriptBody{results: []opsBodyResult{{"abc", originalErr}}, closeErr: closeErr}
	body := NewOpsBody(original, 100, context.Background(), nil)
	body.mark = func(int64, int64, string) { panic("injected observation panic") }
	buf := make([]byte, 20)
	var n int
	var err error
	require.NotPanics(t, func() { n, err = body.Read(buf) })
	require.Equal(t, 3, n)
	require.Equal(t, "abc", string(buf[:n]))
	require.Same(t, originalErr, err)
	require.NotPanics(t, func() { err = body.Close() })
	require.Same(t, closeErr, err)
	require.Equal(t, 1, original.closes)
	require.NotPanics(t, body.Finish)
	canceled := NewOpsBody(&opsScriptBody{results: []opsBodyResult{{"ab", context.Canceled}}}, 10, context.Background(), nil)
	canceled.abort = func(string) { panic("injected abort observation panic") }
	marked := false
	canceled.mark = func(n, declared int64, status string) { marked = true }
	require.NotPanics(t, func() { _, err = canceled.Read(buf) })
	require.Same(t, context.Canceled, err)
	require.True(t, marked, "abort callback panic cannot skip body accounting")
	var disabled *OpsBody
	require.NotPanics(t, disabled.Finish)
	require.NoError(t, disabled.Close())
	n, err = disabled.Read(buf)
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
}

type blockingOpsBody struct {
	started, release chan struct{}
	closes           atomic.Int32
}

func (b *blockingOpsBody) Read(p []byte) (int, error) {
	close(b.started)
	<-b.release
	return copy(p, "late"), io.EOF
}
func (b *blockingOpsBody) Close() error { b.closes.Add(1); close(b.release); return nil }

func TestOpsBodyFinishBarrierBeforeLateRead(t *testing.T) {
	h := newRecorderHarness(t)
	raw := &blockingOpsBody{started: make(chan struct{}), release: make(chan struct{})}
	body := NewOpsBody(raw, -1, context.Background(), h.r)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8)
		n, err := body.Read(buf)
		if n != 4 || err != io.EOF {
			t.Errorf("underlying late result changed: %d %v", n, err)
		}
	}()
	<-raw.started
	h.clock.at(10)
	body.Finish()
	require.True(t, h.r.bodyMarked)
	require.Equal(t, "partial", h.r.bodyStatus)
	h.clock.at(20)
	h.r.Finalize(FinalizeInput{Status: 400})
	require.NoError(t, body.Close())
	<-done
	require.EqualValues(t, 1, raw.closes.Load())
	all := readRecorderMetrics(t, h.registry)
	require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
	require.Zero(t, all["sub2api_request_body_bytes"][0].sum, "late read cannot reopen the Finish barrier")
	require.Equal(t, 0.0, counters(all["sub2api_transfer_bytes_total"]))
	phase := metricWithLabels(t, all, "sub2api_request_phase_seconds", map[string]string{"phase": "body_read"})
	require.InDelta(t, .01, phase.sum, 1e-9)
}

func TestOpsBodyConcurrentFinishAndClientAbortQuery(t *testing.T) {
	h := newRecorderHarness(t)
	body := NewOpsBody(io.NopCloser(strings.NewReader("abc")), 3, context.Background(), h.r)
	_, err := io.ReadAll(body)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body.Finish()
			h.r.ClientAborted()
			h.r.MarkClientAbort("response")
			h.r.ClientAborted()
		}()
	}
	wg.Wait()
	require.True(t, h.r.ClientAborted())
	require.False(t, (*OpsRecorder)(nil).ClientAborted())
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	require.EqualValues(t, 1, all["sub2api_request_body_bytes"][0].count)
	require.Equal(t, 3.0, all["sub2api_request_body_bytes"][0].sum)
	require.Equal(t, 1.0, counters(all["sub2api_client_abort_total"]))
}
