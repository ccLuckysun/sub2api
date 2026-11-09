package requesttiming

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIdentityEOFObserverCoversUnknownLengthAndDoesNotCountPartialReads(t *testing.T) {
	h := New(time.Now(), nil)
	reader := ObserveIdentityBody(io.NopCloser(strings.NewReader("decoded body")), h)
	buffer := make([]byte, 3)
	_, _ = reader.Read(buffer)
	if h.BodyBytes() != nil {
		t.Fatal("partial read reported as complete")
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if got := h.BodyBytes(); got == nil || *got != 12 {
		t.Fatal(got)
	}
	h.RecordBodyBytes(1000)
	if *h.BodyBytes() != 12 {
		t.Fatal("later reread overwrote canonical size")
	}
	failed := New(time.Now(), nil)
	_, _ = io.ReadAll(ObserveIdentityBody(io.NopCloser(failingBodyReader{}), failed))
	if failed.BodyBytes() != nil {
		t.Fatal("failed read reported a complete body")
	}
}

type failingBodyReader struct{}

func (failingBodyReader) Read(p []byte) (int, error) {
	copy(p, "bad")
	return 3, errors.New("read interrupted")
}
