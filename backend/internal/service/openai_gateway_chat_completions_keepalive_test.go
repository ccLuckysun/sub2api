package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newChatKeepaliveTestAccount() *Account {
	return &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
}

type syncChatResponseWriter struct {
	gin.ResponseWriter
	mu      sync.Mutex
	buf     bytes.Buffer
	flushes int
}

func (w *syncChatResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	return w.ResponseWriter.Write(p)
}

func (w *syncChatResponseWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func (w *syncChatResponseWriter) Flush() {
	w.mu.Lock()
	w.flushes++
	w.ResponseWriter.Flush()
	w.mu.Unlock()
}

func (w *syncChatResponseWriter) snapshot() (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String(), w.flushes
}

func TestForwardAsChatCompletions_KeepaliveNotStarvedByIgnoredUpstreamEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)

	synctest.Test(t, func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		sw := &syncChatResponseWriter{ResponseWriter: c.Writer}
		c.Writer = sw
		body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		pr, pw := io.Pipe()
		cfg := &config.Config{
			Gateway: config.GatewayConfig{
				StreamKeepaliveInterval: 1,
			},
		}
		svc := &OpenAIGatewayService{
			cfg: cfg,
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_ka_1"}},
				Body:       pr,
			}},
		}

		type callResult struct {
			res *OpenAIForwardResult
			err error
		}
		done := make(chan callResult, 1)
		go func() {
			res, err := svc.ForwardAsChatCompletions(context.Background(), c, newChatKeepaliveTestAccount(), body, "", "gpt-5.4")
			done <- callResult{res: res, err: err}
		}()

		_, err := io.WriteString(pw, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ka_1\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		require.NoError(t, err)
		synctest.Wait()

		out, flushes := sw.snapshot()
		require.Contains(t, out, `"role":"assistant"`)
		require.NotContains(t, out, ":\n\n")
		require.GreaterOrEqual(t, flushes, 1)

		// Emit ignored upstream SSE events every 250ms across >1s.
		// Previously, each line refreshed lastDataAt and starved downstream keepalive completely.
		for i := 0; i < 5; i++ {
			time.Sleep(250 * time.Millisecond)
			_, err = io.WriteString(pw, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_ka_1\",\"status\":\"in_progress\"}}\n\n")
			require.NoError(t, err)
			synctest.Wait()
		}

		out, _ = sw.snapshot()
		require.Contains(t, out, ":\n\n", "downstream keepalive comment must be sent even while upstream emits non-emitted SSE events")

		_, err = io.WriteString(pw, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(pw, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ka_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n")
		require.NoError(t, err)
		require.NoError(t, pw.Close())

		got := <-done
		require.NoError(t, got.err)
		require.NotNil(t, got.res)
		out, _ = sw.snapshot()
		require.Contains(t, out, `"content":"done"`)
	})
}

func TestForwardAsChatCompletions_LargeBodyReasoningReleasesPendingAndSendsKeepalive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	synctest.Test(t, func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		sw := &syncChatResponseWriter{ResponseWriter: c.Writer}
		c.Writer = sw
		// Exceeds 64KB silent-refusal threshold so refusalDetector is enabled.
		body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("a", 70*1024) + `"}],"stream":true}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		pr, pw := io.Pipe()
		cfg := &config.Config{
			Gateway: config.GatewayConfig{
				StreamKeepaliveInterval: 1,
			},
		}
		svc := &OpenAIGatewayService{
			cfg: cfg,
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_ka_2"}},
				Body:       pr,
			}},
		}

		type callResult struct {
			res *OpenAIForwardResult
			err error
		}
		done := make(chan callResult, 1)
		go func() {
			res, err := svc.ForwardAsChatCompletions(context.Background(), c, newChatKeepaliveTestAccount(), body, "", "gpt-5.4")
			done <- callResult{res: res, err: err}
		}()

		// Stage 1: after response.created alone, refusalDetector must hold both pendingSSE and keepalive
		// even across a full keepalive interval.
		_, err := io.WriteString(pw, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ka_2\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		require.NoError(t, err)
		synctest.Wait()
		time.Sleep(1100 * time.Millisecond)
		synctest.Wait()

		out, flushes := sw.snapshot()
		require.Empty(t, out, "pending role chunk and keepalive must stay held before refusalDetector releases output")
		require.Equal(t, 0, flushes)

		// Stage 2: injecting a zero-chunk reasoning item must flush the held pending role chunk immediately,
		// without advancing virtual time or waiting for text/keepalive.
		_, err = io.WriteString(pw, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n")
		require.NoError(t, err)
		synctest.Wait()

		out, flushes = sw.snapshot()
		require.Contains(t, out, `"role":"assistant"`, "zero-chunk reasoning event must immediately release held pending role chunk")
		require.NotContains(t, out, ":\n\n", "keepalive must not have fired yet right after pending flush")
		require.Equal(t, 1, flushes)

		// Stage 3: advancing past one keepalive interval while upstream sends non-emitted reasoning events
		// must emit a keepalive comment.
		for i := 0; i < 5; i++ {
			time.Sleep(250 * time.Millisecond)
			_, err = io.WriteString(pw, "data: {\"type\":\"response.reasoning_summary_part.added\",\"output_index\":0,\"summary_index\":0,\"part\":{\"type\":\"summary_text\",\"text\":\"\"}}\n\n")
			require.NoError(t, err)
			synctest.Wait()
		}

		out, _ = sw.snapshot()
		require.Contains(t, out, ":\n\n", "keepalive must be sent during long reasoning after pending release")
		require.Less(t, strings.Index(out, `"role":"assistant"`), strings.Index(out, ":\n\n"))

		// Stage 4: final answer and usage complete cleanly.
		_, err = io.WriteString(pw, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"final answer\"}\n\n")
		require.NoError(t, err)
		_, err = io.WriteString(pw, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ka_2\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"final answer\"}]}],\"usage\":{\"input_tokens\":20,\"output_tokens\":4,\"total_tokens\":24}}}\n\n")
		require.NoError(t, err)
		require.NoError(t, pw.Close())

		got := <-done
		require.NoError(t, got.err)
		require.NotNil(t, got.res)
		require.Equal(t, 20, got.res.Usage.InputTokens)
		require.Equal(t, 4, got.res.Usage.OutputTokens)
		out, _ = sw.snapshot()
		require.Contains(t, out, `"content":"final answer"`)
	})
}

func TestForwardAsChatCompletions_PendingWriteFailThenFailedEventPreservesUsageWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	// Exceeds 64KB silent-refusal threshold so response.created is buffered in pendingSSE.
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("b", 70*1024) + `"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_pf_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_pf_1","object":"response","model":"gpt-5.4","status":"failed","output":[],"error":{"code":"server_error","message":"upstream failed after disconnect"},"usage":{"input_tokens":33,"output_tokens":9,"total_tokens":42}}}`,
		"",
	}, "\n")

	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_pf_1"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, newChatKeepaliveTestAccount(), body, "", "gpt-5.4")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "already-disconnected client must not trigger pre-output failover")
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 33, result.Usage.InputTokens)
	require.Equal(t, 9, result.Usage.OutputTokens)
}
