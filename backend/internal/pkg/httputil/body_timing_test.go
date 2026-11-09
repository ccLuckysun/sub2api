package httputil

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
)

func TestReadRequestBodyWithPrealloc_RecordsIdentityBodyBytes(t *testing.T) {
	req := newRequestWithBody(t, []byte(samplePayload), "")
	h := requesttiming.New(time.Now(), nil)
	req = req.WithContext(requesttiming.WithContext(req.Context(), h))

	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatalf("read: %v", err)
	}
	got := h.BodyBytes()
	if got == nil || *got != int64(len(samplePayload)) {
		t.Fatalf("BodyBytes() = %v, want %d", got, len(samplePayload))
	}
}

func TestReadRequestBodyWithPrealloc_RecordsDecodedBytesBeforeNormalization(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(samplePayload)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req := newRequestWithBody(t, compressed.Bytes(), "gzip")
	h := requesttiming.New(time.Now(), nil)
	req = req.WithContext(requesttiming.WithContext(req.Context(), h))

	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("decoded body mismatch: %q", got)
	}
	bodyBytes := h.BodyBytes()
	if bodyBytes == nil || *bodyBytes != int64(len(samplePayload)) {
		t.Fatalf("BodyBytes() = %v, want decoded length %d", bodyBytes, len(samplePayload))
	}
	if *bodyBytes == int64(compressed.Len()) {
		t.Fatal("BodyBytes recorded the compressed size, not the decoded size")
	}
}

func TestReadRequestBodyWithPrealloc_RecordsPrereadBodyBytes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	preread := NewPrereadBody([]byte(samplePayload))
	// Simulate a streaming parser having consumed the buffer: the snapshot must
	// still use the complete wrapped bytes.
	scratch := make([]byte, 4)
	if _, err := preread.Read(scratch); err != nil {
		t.Fatal(err)
	}
	req.Body = preread
	h := requesttiming.New(time.Now(), nil)
	req = req.WithContext(requesttiming.WithContext(req.Context(), h))

	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("preread body mismatch: %q", got)
	}
	bodyBytes := h.BodyBytes()
	if bodyBytes == nil || *bodyBytes != int64(len(samplePayload)) {
		t.Fatalf("BodyBytes() = %v, want %d", bodyBytes, len(samplePayload))
	}
}

func TestReadRequestBodyWithPrealloc_KeepsFirstBodySnapshot(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Body = NewPrereadBody([]byte(samplePayload))
	h := requesttiming.New(time.Now(), nil)
	req = req.WithContext(requesttiming.WithContext(req.Context(), h))

	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatal(err)
	}
	req.Body = NewPrereadBody([]byte("a much longer replacement body"))
	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatal(err)
	}

	bodyBytes := h.BodyBytes()
	if bodyBytes == nil || *bodyBytes != int64(len(samplePayload)) {
		t.Fatalf("BodyBytes() = %v, want first observation %d", bodyBytes, len(samplePayload))
	}
}

func TestReadRequestBodyWithPrealloc_RecordsEmptyBodyAsZero(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Body = NewPrereadBody(nil)
	h := requesttiming.New(time.Now(), nil)
	req = req.WithContext(requesttiming.WithContext(req.Context(), h))

	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatal(err)
	}
	bodyBytes := h.BodyBytes()
	if bodyBytes == nil || *bodyBytes != 0 {
		t.Fatalf("BodyBytes() = %v, want 0", bodyBytes)
	}
}

func TestReadRequestBodyWithPrealloc_WithoutTimingHandleDoesNotPanic(t *testing.T) {
	req := newRequestWithBody(t, []byte(samplePayload), "")
	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatalf("read: %v", err)
	}
}
