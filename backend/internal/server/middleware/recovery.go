package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// RecordRecoveredPanic records panic or broken-pipe telemetry onto the Gin
// context and active OpsRecorder. It returns true when the recovered panic is a
// client broken-pipe/connection-reset error so callers avoid writing a body.
func RecordRecoveredPanic(c *gin.Context, recovered any) bool {
	recoveredErr, _ := recovered.(error)
	if isBrokenPipe(recoveredErr) {
		if c != nil {
			if recoveredErr != nil {
				_ = c.Error(recoveredErr)
			}
			if rec := getOpsRecorder(c); rec != nil {
				safeObserve(func() {
					rec.MarkClientAbort("response")
					rec.RecordErrorDetails(recoveredErr.Error(), 0, "")
				})
			}
		}
		return true
	}
	if c != nil {
		c.Set(monitoring.OpsPanicKey, true)
		RecordOpsAbortError(c, "gateway_panic", fmt.Sprintf("panic: %v", recovered))
	}
	return false
}

// Recovery converts panics into the project's standard JSON error envelope.
//
// It preserves Gin's broken-pipe handling by not attempting to write a response
// when the client connection is already gone.
func Recovery() gin.HandlerFunc {
	recovery := gin.CustomRecoveryWithWriter(gin.DefaultErrorWriter, func(c *gin.Context, recovered any) {
		if RecordRecoveredPanic(c, recovered) {
			c.Abort()
			return
		}
		if c.Writer.Written() {
			c.Abort()
			return
		}

		response.ErrorWithDetails(
			c,
			http.StatusInternalServerError,
			infraerrors.UnknownMessage,
			infraerrors.UnknownReason,
			nil,
		)
		c.Abort()
	})
	return func(c *gin.Context) {
		recovery(c)
		// Gin handles unwrapped broken-pipe panics internally, bypassing the
		// custom callback. Both paths leave an error and preserve the response.
		if rec := getOpsRecorder(c); rec != nil {
			for _, recorded := range c.Errors {
				if isBrokenPipe(recorded.Err) {
					safeObserve(func() {
						rec.MarkClientAbort("response")
						rec.RecordErrorDetails(recorded.Err.Error(), 0, "")
					})
					break
				}
			}
		}
	}
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr.Err, &syscallErr) {
		return false
	}

	msg := strings.ToLower(syscallErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
