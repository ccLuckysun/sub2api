package monitoring

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
)

// OpsBody observes only reads of the original incoming body. Keep this handle
// until the request ends, even when a prereader replaces Request.Body with an
// in-memory replay. Finish is a publication barrier, not a read or Close.
type OpsBody struct {
	body              io.ReadCloser
	ctx               context.Context
	declared          int64
	mu                sync.Mutex
	started, finished bool
	total             int64
	mark              func(int64, int64, string)
	abort             func(string)
}

// NewOpsBody does not read, buffer, or close body. Instrumentation callbacks are
// isolated; the underlying reader/closer's results and behavior remain intact.
func NewOpsBody(body io.ReadCloser, declared int64, ctx context.Context, rec *OpsRecorder) *OpsBody {
	if body == nil {
		body = http.NoBody
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &OpsBody{body: body, declared: declared, ctx: ctx, mark: rec.MarkBodyRead, abort: rec.MarkClientAbort}
}

func (b *OpsBody) Read(p []byte) (int, error) {
	if b == nil {
		return 0, io.EOF
	}
	b.mu.Lock()
	if !b.finished {
		b.started = true
	}
	b.mu.Unlock()
	// Do not hold the observation lock during I/O: Finish can stop publication,
	// and the original Close remains able to unblock an in-flight Read.
	n, err := b.body.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.finished {
		b.total += int64(n)
		if err == io.EOF {
			b.publish("complete", nil)
		} else if err != nil {
			b.publish("partial", err)
		}
	}
	return n, err
}

// Close delegates exactly once per caller invocation, with no implicit read,
// Finish or early close. No observation callback executes on the close path.
func (b *OpsBody) Close() error {
	if b == nil {
		return nil
	}
	return b.body.Close()
}

// Finish publishes an unfinished read as partial before Recorder.Finalize.
// If no Read occurred it only seals publication, leaving body_read at zero.
// Later Read/Close calls still reach the original body but cannot publish.
func (b *OpsBody) Finish() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return
	}
	if !b.started {
		b.finished = true
		return
	}
	b.publish("partial", b.ctx.Err())
}

// publish runs under mu so Finish cannot return before a terminal Read's
// callback has completed. Each callback has its own panic boundary.
func (b *OpsBody) publish(status string, err error) {
	b.finished = true
	if status == "partial" && err != nil {
		req := (&http.Request{}).WithContext(b.ctx)
		if pkghttputil.DescribeRequestBodyReadFailure(req, err).Kind == pkghttputil.RequestBodyReadErrorClientDisconnect {
			safeBodyObserve(func() { b.abort("body_read") })
		}
	}
	safeBodyObserve(func() { b.mark(b.total, b.declared, status) })
}

func safeBodyObserve(observe func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("ops body observation failed", "panic", recovered)
		}
	}()
	observe()
}
