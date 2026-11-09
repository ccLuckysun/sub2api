package requesttiming

import (
	"io"
	"sync"
)

// ObserveIdentityBody covers readers outside the common JSON helper. It is only
// installed for an originally unencoded body: its measured EOF length is also
// the decoded length. A Content-Length hint alone never proves a full read.
// The request owns this wrapper; the Handle never retains its reader/body.
func ObserveIdentityBody(body io.ReadCloser, h *Handle) io.ReadCloser {
	if body == nil || h == nil {
		return body
	}
	return &identityBodyObserver{ReadCloser: body, handle: h}
}

type identityBodyObserver struct {
	io.ReadCloser
	handle *Handle
	mu     sync.Mutex
	read   int64
}

func (r *identityBodyObserver) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.mu.Lock()
	r.read += int64(n)
	total := r.read
	r.mu.Unlock()
	if err == io.EOF {
		r.handle.RecordBodyBytes(total)
	}
	return n, err
}
