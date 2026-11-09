package requesttiming

import (
	"context"
	"testing"
	"time"
)

func TestLifecycleCancelsAndAwaitsHijackedProducerBeforeDrain(t *testing.T) {
	l := &RequestLifecycle{}
	ctx, done, ok := l.Begin(context.Background())
	if !ok {
		t.Fatal("new lifecycle rejected request")
	}
	returned := make(chan error, 1)
	go func() {
		limit, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		returned <- l.Stop(limit)
	}()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("producer not canceled")
	}
	select {
	case <-returned:
		t.Fatal("shutdown returned before producer")
	default:
	}
	if _, _, ok := l.Begin(context.Background()); ok {
		t.Fatal("closing lifecycle accepted producer")
	}
	done()
	done()
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
}
