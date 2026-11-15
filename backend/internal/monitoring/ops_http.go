package monitoring

import (
	"io"
	"net/http"
	"sync"
)

// OpsObservation identifies an existing HTTP body observation layer. Outer
// decorators must forward both methods to keep reinstallation idempotent while
// preserving the outer reader/closer's behavior. Request observations carry a
// nil response handle; response observations retain their original identity.
type OpsObservation interface {
	OpsObservationDirection() string
	OpsResponseHandle() *OpsResponse
}

// ObserveRequestBody observes actual reads of a single physical request body.
// Every physical copy (including a GetBody result) must pass through this
// function. ObserveHTTPRequest installs both entry points together. Repeating
// this wrapper on the same request body is a no-op, never a second counter.
func ObserveRequestBody(rec *OpsRecorder, body io.ReadCloser) io.ReadCloser {
	if body == nil || rec == nil {
		return body
	}
	if observed, ok := body.(OpsObservation); ok && observed.OpsObservationDirection() == "req_upstream" {
		return body
	}
	return &opsHTTPBody{body: body, direction: "req_upstream",
		add:      func(n int64, status string) { rec.AddBytes("req_upstream", n, status) },
		complete: func() { rec.MarkBytesComplete("req_upstream") },
	}
}

// ObserveHTTPRequest wraps Body and every successful GetBody result, preserving
// nil fields, errors, ContentLength and the underlying factory's call count.
// Call before sending the request; mutating an already in-flight http.Request
// is not supported. Repeated installation cannot double-count body reads.
func ObserveHTTPRequest(rec *OpsRecorder, request *http.Request) {
	if rec == nil || request == nil {
		return
	}
	request.Body = ObserveRequestBody(rec, request.Body)
	if request.GetBody != nil {
		getBody := request.GetBody
		request.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil {
				return body, err
			}
			return ObserveRequestBody(rec, body), nil
		}
	}
}

// ObserveResponseBody must be installed AFTER the transport's decompression
// layer. It counts bytes returned by that decoded body, without decoding,
// pre-reading, buffering or choosing which physical response is consumed.
// Retain handle from Sent and lock it with UseResponseFor in the consumer.
// A nil recorder disables observation; nil attempt/handle leave byte counting
// enabled while the nil-safe attempt methods simply ignore timing events.
func ObserveResponseBody(rec *OpsRecorder, attempt *OpsAttempt, handle *OpsResponse, body io.ReadCloser) io.ReadCloser {
	if body == nil || rec == nil {
		return body
	}
	if observed, ok := body.(OpsObservation); ok && observed.OpsObservationDirection() == "resp_upstream" {
		return body
	}
	return &opsHTTPBody{body: body, direction: "resp_upstream", handle: handle,
		add:       func(n int64, status string) { rec.AddBytes("resp_upstream", n, status) },
		complete:  func() { rec.MarkBytesComplete("resp_upstream") },
		firstByte: func() { attempt.NoteFirstByteFor(handle) },
		done:      func(err error) { attempt.BodyDoneFor(handle, err) },
	}
}

// ResponseHandle returns the physical response identity carried by the observed
// body. Outer decorators must forward OpsObservation to preserve this identity
// and prevent a second observation layer from being installed.
func ResponseHandle(response *http.Response) *OpsResponse {
	if response == nil || response.Body == nil {
		return nil
	}
	if body, ok := response.Body.(OpsObservation); ok && body.OpsObservationDirection() == "resp_upstream" {
		return body.OpsResponseHandle()
	}
	return nil
}

type opsHTTPBody struct {
	body                 io.ReadCloser
	direction            string
	handle               *OpsResponse
	mu                   sync.Mutex // serializes observation events, never held during I/O
	terminal, first, eof bool
	add                  func(int64, string)
	complete             func()
	firstByte            func()
	done                 func(error)
}

func (b *opsHTTPBody) OpsObservationDirection() string { return b.direction }
func (b *opsHTTPBody) OpsResponseHandle() *OpsResponse { return b.handle }

func (b *opsHTTPBody) Read(p []byte) (int, error) {
	// Close must remain able to interrupt a blocked underlying Read. The body
	// retains its original I/O concurrency contract; only observer state is locked.
	n, err := b.body.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > 0 {
		if !b.first {
			b.first = true
			if b.firstByte != nil {
				safeBodyObserve(b.firstByte)
			}
		}
		status := "partial"
		if b.eof {
			status = "complete"
		}
		safeBodyObserve(func() { b.add(int64(n), status) })
	}
	if !b.terminal {
		switch {
		case err == io.EOF:
			b.terminal = true
			b.eof = true
			safeBodyObserve(b.complete)
			if b.done != nil {
				safeBodyObserve(func() { b.done(nil) })
			}
		case err != nil:
			b.terminal = true
			if n == 0 {
				safeBodyObserve(func() { b.add(0, "partial") })
			}
			if b.done != nil {
				safeBodyObserve(func() { b.done(err) })
			}
		}
	}
	return n, err
}

func (b *opsHTTPBody) Close() error {
	err := b.body.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.terminal {
		b.terminal = true
		safeBodyObserve(func() { b.add(0, "partial") })
		if b.done != nil {
			terminalErr := err
			if terminalErr == nil {
				terminalErr = io.ErrUnexpectedEOF
			}
			safeBodyObserve(func() { b.done(terminalErr) })
		}
	}
	return err
}
