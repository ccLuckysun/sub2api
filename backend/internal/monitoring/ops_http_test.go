package monitoring

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsAttemptContext(t *testing.T) {
	h := newRecorderHarness(t)
	a := h.r.StartAttempt("Account.A")
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), recorderContextKey{}, h.r))
	defer cancel()
	ctx := WithAttempt(parent, a)
	require.Same(t, a, AttemptFromContext(ctx))
	require.Same(t, h.r, RecorderFromContext(ctx))
	request, err := http.NewRequestWithContext(ctx, "POST", "http://example.invalid", nil)
	require.NoError(t, err)
	require.Same(t, a, AttemptFromContext(request.Context()))
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Same(t, a, AttemptFromContext(context.WithoutCancel(ctx)))
	require.Nil(t, AttemptFromContext(WithAttempt(ctx, nil)), "nil masks an inherited attempt")
	require.Same(t, a, AttemptFromContext(WithAttempt(nil, a)))
	require.Nil(t, AttemptFromContext(nil))
	require.Nil(t, AttemptFromContext(context.Background()))
	require.Nil(t, AttemptFromContext(WithAttempt(nil, nil)))
}

func TestOpsHTTPResponseLifecycle(t *testing.T) {
	readError := errors.New("upstream body failed")
	for _, tc := range []struct {
		name       string
		results    []opsBodyResult
		times      []int64
		closeAt    int64
		lock       bool
		wantN      int64
		firstAt    int64
		terminalAt int64
		complete   bool
		closeErr   error
	}{
		{name: "chunked_zero_read_first_positive_only", results: []opsBodyResult{{"", nil}, {"ab", nil}, {"c", io.EOF}}, times: []int64{40, 50, 80}, closeAt: 85, lock: true, wantN: 3, firstAt: 50, terminalAt: 80, complete: true},
		{name: "positive_n_with_EOF", results: []opsBodyResult{{"abc", io.EOF}}, times: []int64{50}, closeAt: 85, lock: true, wantN: 3, firstAt: 50, terminalAt: 50, complete: true},
		{name: "empty_EOF_no_first_byte", results: []opsBodyResult{{"", io.EOF}}, times: []int64{50}, closeAt: 85, lock: true, terminalAt: 50, complete: true},
		{name: "error_keeps_n", results: []opsBodyResult{{"ab", readError}}, times: []int64{50}, closeAt: 85, lock: true, wantN: 2, firstAt: 50, terminalAt: 50},
		{name: "zero_n_error", results: []opsBodyResult{{"", readError}}, times: []int64{50}, closeAt: 85, lock: true, terminalAt: 50},
		{name: "early_close_after_positive_read", results: []opsBodyResult{{"ab", nil}}, times: []int64{50}, closeAt: 70, lock: true, wantN: 2, firstAt: 50, terminalAt: 70},
		{name: "early_close_unread", closeAt: 70, lock: true, terminalAt: 70},
		{name: "close_error_preserved", results: []opsBodyResult{{"ab", nil}}, times: []int64{50}, closeAt: 70, lock: true, wantN: 2, firstAt: 50, terminalAt: 70, closeErr: readError},
		{name: "unlocked_body_no_TTFT", results: []opsBodyResult{{"abc", io.EOF}}, times: []int64{50}, closeAt: 85, wantN: 3, firstAt: 50, terminalAt: 50, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecorderHarness(t)
			h.r.MarkBodyRead(0, 0, "complete")
			h.r.MarkBytesComplete("req_upstream")
			h.r.MarkBytesComplete("resp_downstream")
			h.clock.at(20)
			a := h.r.StartAttempt("Account.A")
			handle := a.Sent()
			h.clock.at(30)
			a.NoteHeadersFor(handle, 200)
			if tc.lock {
				a.UseResponseFor(handle)
			}
			raw := &opsScriptBody{results: append([]opsBodyResult(nil), tc.results...), closeErr: tc.closeErr}
			body := ObserveResponseBody(h.r, a, handle, raw)
			require.Zero(t, raw.reads)
			require.Zero(t, raw.closes, "wrapping has no I/O")
			require.Same(t, body, ObserveResponseBody(h.r, a, handle, body), "duplicate response wrapping is idempotent")
			require.Same(t, handle, ResponseHandle(&http.Response{Body: body}))
			for i, at := range tc.times {
				h.clock.at(at)
				buf := make([]byte, 20)
				n, err := body.Read(buf)
				require.Equal(t, tc.results[i].data, string(buf[:n]))
				require.True(t, tc.results[i].err == err, "Read returns the same error")
				if tc.firstAt == 0 || at < tc.firstAt {
					require.True(t, handle.firstAt.IsZero())
				} else {
					require.Equal(t, time.Duration(tc.firstAt)*time.Millisecond, handle.firstAt.Sub(h.r.entry))
				}
			}
			h.clock.at(tc.closeAt)
			err := body.Close()
			require.True(t, tc.closeErr == err, "Close returns the same error")
			require.Equal(t, 1, raw.closes)
			require.Equal(t, time.Duration(tc.terminalAt)*time.Millisecond, handle.bodyAt.Sub(h.r.entry))
			require.False(t, a.done, "EOF/Close do not settle a logical attempt")
			before := readRecorderMetrics(t, h.registry)
			require.Empty(t, before["sub2api_upstream_requests_total"])
			require.Empty(t, before["sub2api_upstream_ttft_seconds"])
			result, reason := "upstream_error", "unknown"
			if tc.complete {
				result, reason = "success", ""
			}
			h.clock.at(90)
			a.Done(result, reason, FailureEvidence{})
			h.clock.at(100)
			h.r.Finalize(FinalizeInput{Status: 200})
			all := readRecorderMetrics(t, h.registry)
			require.Equal(t, float64(tc.wantN), counters(all["sub2api_transfer_bytes_total"]), "M-2 counts only actual response reads")
			state := h.r.bytes["resp_upstream"]
			require.Equal(t, tc.wantN, state.n)
			expectedStatus := "partial"
			if tc.complete {
				expectedStatus = "complete"
			}
			require.Equal(t, expectedStatus, state.status)
			ttft := all["sub2api_upstream_ttft_seconds"]
			if tc.lock && tc.firstAt > 0 {
				require.Len(t, ttft, 1)
				require.EqualValues(t, 1, ttft[0].count)
				require.InDelta(t, float64(tc.firstAt-20)/1000, ttft[0].sum, 1e-9)
			} else {
				require.Empty(t, ttft)
			}
			require.Equal(t, 1.0, counters(all["sub2api_upstream_requests_total"]))
			require.Equal(t, 1.0, counters(all["sub2api_gateway_requests_total"]))
			wantCoverage := uint64(0)
			if tc.complete {
				wantCoverage = 1
			}
			require.Equal(t, wantCoverage, h.coverage.counts.bytes, "M-9 requires all four directions complete")
		})
	}
}

func TestOpsHTTPRequestBodyAndGetBody(t *testing.T) {
	h := newRecorderHarness(t)
	payload := []byte("request-原始-body")
	req, err := http.NewRequest("POST", "http://example.invalid", bytes.NewReader(payload))
	require.NoError(t, err)
	factory := req.GetBody
	factoryCalls := 0
	req.GetBody = func() (io.ReadCloser, error) { factoryCalls++; return factory() }
	declared := req.ContentLength
	ObserveHTTPRequest(h.r, req)
	ObserveHTTPRequest(h.r, req)
	require.Equal(t, declared, req.ContentLength)
	require.Zero(t, factoryCalls, "wrapping does not eagerly call GetBody")
	require.Same(t, req.Body, ObserveRequestBody(h.r, req.Body))
	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, payload, data)
	require.NoError(t, req.Body.Close())
	copyBody, err := req.GetBody()
	require.NoError(t, err)
	require.Equal(t, 1, factoryCalls, "nested installation calls the underlying factory once")
	require.Same(t, copyBody, ObserveRequestBody(h.r, copyBody))
	data, err = io.ReadAll(copyBody)
	require.NoError(t, err)
	require.Equal(t, payload, data)
	require.NoError(t, copyBody.Close())
	all := readRecorderMetrics(t, h.registry)
	require.Equal(t, 2*float64(len(payload)), counters(all["sub2api_transfer_bytes_total"]))
	require.Equal(t, byteObservation{2 * int64(len(payload)), "complete"}, h.r.bytes["req_upstream"])
	require.Empty(t, all["sub2api_upstream_requests_total"], "body wrapping creates no attempts")
}

func TestOpsHTTPRequestFactoryErrorsAndNil(t *testing.T) {
	h := newRecorderHarness(t)
	factoryErr := errors.New("original GetBody error")
	raw := &opsScriptBody{}
	req := &http.Request{GetBody: func() (io.ReadCloser, error) { return raw, factoryErr }}
	ObserveHTTPRequest(h.r, req)
	ObserveHTTPRequest(h.r, req)
	require.Nil(t, req.Body)
	got, err := req.GetBody()
	require.Same(t, raw, got)
	require.Same(t, factoryErr, err)
	require.Zero(t, raw.reads)
	require.Zero(t, raw.closes)
	noFactory := &http.Request{Body: raw}
	ObserveHTTPRequest(h.r, noFactory)
	require.Nil(t, noFactory.GetBody)
	nilFactoryBody := &http.Request{GetBody: func() (io.ReadCloser, error) { return nil, nil }}
	ObserveHTTPRequest(h.r, nilFactoryBody)
	got, err = nilFactoryBody.GetBody()
	require.Nil(t, got)
	require.NoError(t, err)
	require.NotPanics(t, func() { ObserveHTTPRequest(nil, nil); ObserveHTTPRequest(h.r, nil) })
	disabled := &http.Request{Body: raw}
	ObserveHTTPRequest(nil, disabled)
	require.Same(t, raw, disabled.Body)
	require.Nil(t, ObserveRequestBody(h.r, nil))
	require.Same(t, raw, ObserveRequestBody(nil, raw))
	require.Nil(t, ObserveResponseBody(h.r, nil, nil, nil))
	require.Same(t, raw, ObserveResponseBody(nil, nil, nil, raw))
	require.Nil(t, ResponseHandle(nil))
	require.Nil(t, ResponseHandle(&http.Response{}))
	require.Nil(t, ResponseHandle(&http.Response{Body: raw}))
	require.Nil(t, ResponseHandle(&http.Response{Body: noFactory.Body}))
	// Nil attempt and handle still allow a valid recorder to count body bytes.
	observed := ObserveResponseBody(h.r, nil, nil, io.NopCloser(strings.NewReader("abc")))
	_, err = io.ReadAll(observed)
	require.NoError(t, err)
	require.NoError(t, observed.Close())
	require.Equal(t, byteObservation{3, "complete"}, h.r.bytes["resp_upstream"])
}

func TestOpsHTTPRequestReadErrorsAndEarlyClose(t *testing.T) {
	readErr := errors.New("send failed")
	closeErr := errors.New("close failed")
	for _, tc := range []struct {
		name     string
		result   opsBodyResult
		read     bool
		closeErr error
		wantN    int64
		status   string
	}{
		{"EOF", opsBodyResult{"abc", io.EOF}, true, closeErr, 3, "complete"},
		{"error_with_n", opsBodyResult{"ab", readErr}, true, closeErr, 2, "partial"},
		{"error_without_n", opsBodyResult{"", readErr}, true, closeErr, 0, "partial"},
		{"early_close", opsBodyResult{"ab", nil}, true, closeErr, 2, "partial"},
		{"unread_close", opsBodyResult{}, false, nil, 0, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecorderHarness(t)
			raw := &opsScriptBody{results: []opsBodyResult{tc.result}, closeErr: tc.closeErr}
			body := ObserveRequestBody(h.r, raw)
			if tc.read {
				buf := make([]byte, 20)
				n, err := body.Read(buf)
				require.Equal(t, tc.result.data, string(buf[:n]))
				require.True(t, err == tc.result.err)
			}
			require.True(t, body.Close() == tc.closeErr)
			require.Equal(t, 1, raw.closes)
			require.Equal(t, byteObservation{tc.wantN, tc.status}, h.r.bytes["req_upstream"])
			require.Equal(t, float64(tc.wantN), counters(readRecorderMetrics(t, h.registry)["sub2api_transfer_bytes_total"]))
		})
	}
}

type opsDecodedHTTPBody struct {
	io.Reader
	close func() error
}

func (b *opsDecodedHTTPBody) Close() error { return b.close() }

func TestOpsHTTPResponseCountsAfterDecompression(t *testing.T) {
	payload := []byte(strings.Repeat("decoded-中文-", 100))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(payload)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	require.NotEqual(t, len(payload), compressed.Len())
	decoder, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	require.NoError(t, err)
	closes := 0
	decoded := &opsDecodedHTTPBody{Reader: decoder, close: func() error { closes++; return decoder.Close() }}
	h := newRecorderHarness(t)
	a := h.r.StartAttempt("Account.A")
	handle := a.Sent()
	a.NoteHeadersFor(handle, 200)
	a.UseResponseFor(handle)
	// The same ordering required of the future transport composition: decode,
	// then observe. The observer itself neither decompresses nor buffers bytes.
	body := ObserveResponseBody(h.r, a, handle, decoded)
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, payload, data)
	require.NoError(t, body.Close())
	require.Equal(t, 1, closes)
	require.Equal(t, int64(len(payload)), h.r.bytes["resp_upstream"].n)
	require.Equal(t, float64(len(payload)), counters(readRecorderMetrics(t, h.registry)["sub2api_transfer_bytes_total"]))
}

func TestOpsMarkBytesCompleteStateOnly(t *testing.T) {
	h := newRecorderHarness(t)
	dirs := []string{"in_wire", "req_decoded", "req_upstream", "resp_upstream", "resp_downstream"}
	for _, dir := range dirs {
		h.r.MarkBytesComplete(dir)
		require.Equal(t, byteObservation{0, "complete"}, h.r.bytes[dir])
	}
	require.Empty(t, readRecorderMetrics(t, h.registry)["sub2api_transfer_bytes_total"], "state upgrades do not add to M-2")
	for _, dir := range dirs {
		h.r.AddBytes(dir, 7, "partial")
		require.Equal(t, "partial", h.r.bytes[dir].status, "existing AddBytes semantics remain intact")
		h.r.MarkBytesComplete(dir)
		h.r.MarkBytesComplete(dir)
		require.Equal(t, byteObservation{7, "complete"}, h.r.bytes[dir])
	}
	require.Equal(t, 35.0, counters(readRecorderMetrics(t, h.registry)["sub2api_transfer_bytes_total"]))
	h.r.MarkBytesComplete("attacker_label")
	require.Len(t, h.r.bytes, 5)
	h.r.AddBytes("req_upstream", 0, "partial")
	h.r.Finalize(FinalizeInput{Status: 200})
	h.r.MarkBytesComplete("req_upstream")
	require.Equal(t, "partial", h.r.bytes["req_upstream"].status, "Finalize is the publication cutoff")
	require.NotPanics(t, func() { (*OpsRecorder)(nil).MarkBytesComplete("req_upstream") })
}

func TestOpsHTTPObservationPanicsPreserveIO(t *testing.T) {
	readErr := errors.New("original body error")
	closeErr := errors.New("original Close error")
	h := newRecorderHarness(t)
	raw := &opsScriptBody{results: []opsBodyResult{{"ab", readErr}}, closeErr: closeErr}
	body := ObserveResponseBody(h.r, nil, nil, raw).(*opsHTTPBody)
	first, adds, done := 0, 0, 0
	body.firstByte = func() { first++; panic("first-byte observation failed") }
	body.add = func(int64, string) { adds++; panic("byte observation failed") }
	body.done = func(error) { done++; panic("body-end observation failed") }
	var n int
	var err error
	buf := make([]byte, 8)
	require.NotPanics(t, func() { n, err = body.Read(buf) })
	require.Equal(t, 2, n)
	require.Equal(t, "ab", string(buf[:n]))
	require.Same(t, readErr, err)
	require.Equal(t, 1, first)
	require.Equal(t, 1, adds)
	require.Equal(t, 1, done, "one callback panic cannot skip the others")
	require.NotPanics(t, func() { err = body.Close() })
	require.Same(t, closeErr, err)
	require.Equal(t, 1, raw.closes)
	earlyRaw := &opsScriptBody{closeErr: closeErr}
	early := ObserveResponseBody(h.r, nil, nil, earlyRaw).(*opsHTTPBody)
	early.add = func(int64, string) { panic("partial observation failed") }
	ended := false
	early.done = func(error) { ended = true; panic("close-end observation failed") }
	require.NotPanics(t, func() { err = early.Close() })
	require.Same(t, closeErr, err)
	require.True(t, ended)
	require.Equal(t, 1, earlyRaw.closes)
	eof := ObserveResponseBody(h.r, nil, nil, &opsScriptBody{results: []opsBodyResult{{"", io.EOF}}}).(*opsHTTPBody)
	eof.complete = func() { panic("complete upgrade failed") }
	ended = false
	eof.done = func(err error) { require.NoError(t, err); ended = true }
	require.NotPanics(t, func() { _, err = eof.Read(buf) })
	require.ErrorIs(t, err, io.EOF)
	require.True(t, ended)
}

type opsHandleDecorator struct {
	io.ReadCloser
}

func (b *opsHandleDecorator) OpsObservationDirection() string {
	return b.ReadCloser.(OpsObservation).OpsObservationDirection()
}
func (b *opsHandleDecorator) OpsResponseHandle() *OpsResponse {
	return b.ReadCloser.(OpsObservation).OpsResponseHandle()
}

func TestOpsHTTPResponseIdentityAcrossSends(t *testing.T) {
	h := newRecorderHarness(t)
	h.clock.at(20)
	a := h.r.StartAttempt("Account.A")
	handleA := a.Sent()
	h.clock.at(30)
	a.NoteHeadersFor(handleA, 200)
	a.UseResponseFor(handleA)
	bodyA := ObserveResponseBody(h.r, a, handleA, io.NopCloser(strings.NewReader("A!")))
	h.clock.at(40)
	handleB := a.Sent()
	a.NoteHeadersFor(handleB, 500)
	bodyB := ObserveResponseBody(h.r, a, handleB, io.NopCloser(strings.NewReader("other")))
	require.Same(t, bodyB, ObserveResponseBody(h.r, a, handleA, bodyB), "duplicate wrapper cannot silently rebind a physical response")
	require.Same(t, handleB, ResponseHandle(&http.Response{Body: bodyB}))
	require.Same(t, handleA, ResponseHandle(&http.Response{Body: &opsHandleDecorator{ReadCloser: bodyA}}))
	h.clock.at(50)
	data, err := io.ReadAll(bodyB)
	require.NoError(t, err)
	require.Equal(t, "other", string(data))
	require.NoError(t, bodyB.Close())
	require.True(t, handleB.firstAt.IsZero())
	require.True(t, handleB.bodyAt.IsZero(), "unselected handle events cannot alter attempt timing")
	h.clock.at(70)
	data, err = io.ReadAll(bodyA)
	require.NoError(t, err)
	require.Equal(t, "A!", string(data))
	require.NoError(t, bodyA.Close())
	require.Equal(t, 70*time.Millisecond, handleA.firstAt.Sub(h.r.entry))
	require.Equal(t, 70*time.Millisecond, handleA.bodyAt.Sub(h.r.entry))
	h.clock.at(80)
	a.Done("success", "", FailureEvidence{})
	a.Done("success", "", FailureEvidence{})
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	require.Equal(t, 7.0, counters(all["sub2api_transfer_bytes_total"]), "all physically consumed response bytes count, including unselected responses")
	ttft := metricWithLabels(t, all, "sub2api_upstream_ttft_seconds", map[string]string{"account": "accounta"})
	require.EqualValues(t, 1, ttft.count)
	require.InDelta(t, .05, ttft.sum, 1e-9)
}

func TestOpsHTTPResponseFallbackDoesNotSettleTTFT(t *testing.T) {
	h := newRecorderHarness(t)
	a := h.r.StartAttempt("Account.A")
	handle := a.Sent()
	a.NoteHeadersFor(handle, 200)
	a.UseResponseFor(handle)
	body := ObserveResponseBody(h.r, a, handle, io.NopCloser(strings.NewReader("abc")))
	_, err := io.ReadAll(body)
	require.NoError(t, err)
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	require.Empty(t, all["sub2api_upstream_ttft_seconds"])
	require.Equal(t, 1.0, metricWithLabels(t, all, "sub2api_upstream_requests_total", map[string]string{"account": "accounta", "result": "upstream_error", "failure_reason": "unknown"}).counter)
}

type opsHTTPUnblockBody struct {
	started, release chan struct{}
	start, closeOnce sync.Once
	closes           atomic.Int32
	closeErr         error
}

func (b *opsHTTPUnblockBody) Read([]byte) (int, error) {
	b.start.Do(func() { close(b.started) })
	<-b.release
	return 0, io.ErrClosedPipe
}
func (b *opsHTTPUnblockBody) Close() error {
	b.closes.Add(1)
	b.closeOnce.Do(func() { close(b.release) })
	return b.closeErr
}

func TestOpsHTTPCloseUnblocksRead(t *testing.T) {
	for _, response := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "response"}[response], func(t *testing.T) {
			h := newRecorderHarness(t)
			a := h.r.StartAttempt("Account.A")
			handle := a.Sent()
			a.NoteHeadersFor(handle, 200)
			a.UseResponseFor(handle)
			originalErr := errors.New("original close error")
			raw := &opsHTTPUnblockBody{started: make(chan struct{}), release: make(chan struct{}), closeErr: originalErr}
			var body io.ReadCloser = ObserveRequestBody(h.r, raw)
			direction := "req_upstream"
			if response {
				body = ObserveResponseBody(h.r, a, handle, raw)
				direction = "resp_upstream"
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				n, err := body.Read(make([]byte, 4))
				if n != 0 || err != io.ErrClosedPipe {
					t.Errorf("Read changed original I/O result: %d %v", n, err)
				}
			}()
			<-raw.started
			closed := make(chan struct{})
			go func() {
				defer close(closed)
				if err := body.Close(); err != originalErr {
					t.Errorf("Close changed original error: %v", err)
				}
			}()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close blocked behind in-flight Read")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("underlying Close did not unblock Read")
			}
			require.Equal(t, byteObservation{0, "partial"}, h.r.bytes[direction])
			require.EqualValues(t, 1, raw.closes.Load())
			require.True(t, body.Close() == originalErr)
			require.EqualValues(t, 2, raw.closes.Load(), "each explicit Close reaches the original body")
		})
	}
}

type opsHTTPAtomicBody struct{ reads, closes atomic.Int64 }

func (b *opsHTTPAtomicBody) Read(p []byte) (int, error) { b.reads.Add(1); return copy(p, "x"), nil }
func (b *opsHTTPAtomicBody) Close() error               { b.closes.Add(1); return nil }

func TestOpsHTTPConcurrentReadCloseAndUpgrade(t *testing.T) {
	h := newRecorderHarness(t)
	a := h.r.StartAttempt("Account.A")
	handle := a.Sent()
	a.NoteHeadersFor(handle, 200)
	a.UseResponseFor(handle)
	raw := &opsHTTPAtomicBody{}
	body := ObserveResponseBody(h.r, a, handle, raw)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := body.Read(make([]byte, 1))
			if n != 1 || err != nil {
				t.Errorf("Read result changed: %d %v", n, err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := body.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 64, raw.reads.Load())
	require.EqualValues(t, 8, raw.closes.Load())
	require.Equal(t, byteObservation{64, "partial"}, h.r.bytes["resp_upstream"])
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.r.MarkBytesComplete("resp_upstream") }()
	}
	wg.Wait()
	require.Equal(t, byteObservation{64, "complete"}, h.r.bytes["resp_upstream"])
	a.Done("success", "", FailureEvidence{})
	h.r.Finalize(FinalizeInput{Status: 200})
	all := readRecorderMetrics(t, h.registry)
	require.Equal(t, 64.0, counters(all["sub2api_transfer_bytes_total"]))
	require.EqualValues(t, 1, all["sub2api_upstream_ttft_seconds"][0].count)
}

func TestOpsHTTPDecoratedObservationReinstallation(t *testing.T) {
	originalReadErr := errors.New("original read failure")
	originalCloseErr := errors.New("original close failure")
	for _, direction := range []string{"req_upstream", "resp_upstream"} {
		for _, tc := range []struct {
			name    string
			readErr error
			status  string
		}{
			{"EOF", io.EOF, "complete"},
			{"read_error", originalReadErr, "partial"},
			{"early_close", nil, "partial"},
		} {
			t.Run(direction+"/"+tc.name, func(t *testing.T) {
				h := newRecorderHarness(t)
				a := h.r.StartAttempt("Account.A")
				handle := a.Sent()
				a.NoteHeadersFor(handle, 200)
				a.UseResponseFor(handle)
				raw := &opsScriptBody{results: []opsBodyResult{{"actual-字节", tc.readErr}}, closeErr: originalCloseErr}
				var observed io.ReadCloser
				if direction == "req_upstream" {
					observed = ObserveRequestBody(h.r, raw)
				} else {
					observed = ObserveResponseBody(h.r, a, handle, raw)
				}
				// Two external decorators forward the observation identity without exposing
				// the concrete opsHTTPBody type to the reinstallation entry points.
				outer := &opsHandleDecorator{ReadCloser: &opsHandleDecorator{ReadCloser: observed}}
				var reinstalled io.ReadCloser
				if direction == "req_upstream" {
					reinstalled = ObserveRequestBody(h.r, outer)
				} else {
					reinstalled = ObserveResponseBody(h.r, a, handle, outer)
				}
				require.Same(t, outer, reinstalled, "keep the entire outer decorator chain")
				require.Equal(t, direction, reinstalled.(OpsObservation).OpsObservationDirection())
				expectedHandle := handle
				if direction == "req_upstream" {
					expectedHandle = nil
				}
				require.Equal(t, expectedHandle, reinstalled.(OpsObservation).OpsResponseHandle())
				require.Equal(t, expectedHandle, ResponseHandle(&http.Response{Body: reinstalled}))
				require.Zero(t, raw.reads)
				require.Zero(t, raw.closes, "installation performs no I/O")
				buf := make([]byte, 64)
				n, err := reinstalled.Read(buf)
				require.Equal(t, "actual-字节", string(buf[:n]))
				require.True(t, err == tc.readErr, "preserve original Read error")
				require.True(t, reinstalled.Close() == originalCloseErr, "preserve original Close error")
				require.Equal(t, 1, raw.reads)
				require.Equal(t, 1, raw.closes, "Close reaches the bottom body exactly once")
				require.Equal(t, byteObservation{int64(n), tc.status}, h.r.bytes[direction])
				all := readRecorderMetrics(t, h.registry)
				require.Equal(t, float64(n), counters(all["sub2api_transfer_bytes_total"]), "external decorators must not cause a second byte increment")
				require.Equal(t, expectedHandle, ResponseHandle(&http.Response{Body: reinstalled}), "Close cannot replace the response identity")
			})
		}
	}
}
