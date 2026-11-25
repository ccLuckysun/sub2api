//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type keepaliveNotifyWriter struct {
	gin.ResponseWriter
	mu           sync.Mutex
	sawHeartbeat bool
	once         sync.Once
	flushed      chan struct{}
}

func (w *keepaliveNotifyWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(":\n\n")) {
		w.mu.Lock()
		w.sawHeartbeat = true
		w.mu.Unlock()
	}
	return w.ResponseWriter.Write(p)
}

func (w *keepaliveNotifyWriter) WriteString(s string) (int, error) {
	if strings.Contains(s, ":\n\n") {
		w.mu.Lock()
		w.sawHeartbeat = true
		w.mu.Unlock()
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *keepaliveNotifyWriter) Flush() {
	w.ResponseWriter.Flush()
	w.mu.Lock()
	ready := w.sawHeartbeat
	w.mu.Unlock()
	if ready {
		w.once.Do(func() { close(w.flushed) })
	}
}

type chatKeepaliveUpstreamCall struct {
	accountID int64
	path      string
}

type chatKeepaliveUpstream struct {
	service.HTTPUpstream
	mu      sync.Mutex
	hits    []chatKeepaliveUpstreamCall
	respond func(call int, req *http.Request, accountID int64) (*http.Response, error)
}

func (u *chatKeepaliveUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	u.mu.Lock()
	u.hits = append(u.hits, chatKeepaliveUpstreamCall{
		accountID: accountID,
		path:      req.URL.Path,
	})
	call := len(u.hits)
	u.mu.Unlock()
	return u.respond(call, req, accountID)
}

func (u *chatKeepaliveUpstream) calls() []chatKeepaliveUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]chatKeepaliveUpstreamCall, len(u.hits))
	copy(out, u.hits)
	return out
}

func serveChatKeepaliveFailoverScenario(
	t *testing.T,
	groupPlatform string,
	accounts []service.Account,
	requestBody string,
	userAgent string,
	respondFactory func(heartbeatFlushed <-chan struct{}) func(call int, req *http.Request, accountID int64) (*http.Response, error),
) (*httptest.ResponseRecorder, *chatKeepaliveUpstream) {
	t.Helper()
	if groupPlatform == "" {
		groupPlatform = service.PlatformOpenAI
	}
	gin.SetMode(gin.TestMode)
	groupID := int64(9301)
	heartbeatFlushed := make(chan struct{})
	repo := &grokCredentialHandlerRepo{accounts: accounts, missingOnGet: map[int64]bool{}}
	upstream := &chatKeepaliveUpstream{respond: respondFactory(heartbeatFlushed)}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxAccountSwitches = 3
	cfg.Gateway.StreamKeepaliveInterval = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billingCache, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	apiKey := &service.APIKey{
		ID: 9302, GroupID: &groupID,
		User:  &service.User{ID: 9303, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: groupPlatform, Status: service.StatusActive},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Writer = &keepaliveNotifyWriter{
			ResponseWriter: c.Writer,
			flushed:        heartbeatFlushed,
		}
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/v1/chat/completions", h.ChatCompletions)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	router.ServeHTTP(rec, req)
	return rec, upstream
}

func waitHeartbeatOrCloseError(pw *io.PipeWriter, heartbeatFlushed <-chan struct{}) bool {
	select {
	case <-heartbeatFlushed:
		return true
	case <-time.After(5 * time.Second):
		_ = pw.CloseWithError(errors.New("timed out waiting for keepalive heartbeat flush"))
		return false
	}
}

func TestOpenAIChatCompletions_HeartbeatOnlyAllowsPreOutputFailover(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("x", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"

	rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenAI, preContentAccounts(2), largeBody, "", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
		return func(call int, _ *http.Request, _ int64) (*http.Response, error) {
			if call == 1 {
				pr, pw := io.Pipe()
				go func() {
					// Zero-chunk reasoning event without response.created: no pending role chunk,
					// but refusalDetector releases keepalive so :\n\n is written before server_error.
					_, _ = io.WriteString(pw, reasoningAdded)
					if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
						return
					}
					_, _ = io.WriteString(pw, preContentHandlerFailed)
					_ = pw.Close()
				}()
				return preContentSSEResponse(pr), nil
			}
			return preContentSSEResponse(io.NopCloser(strings.NewReader(preContentHandlerSuccess))), nil
		}
	})

	require.Len(t, upstream.calls(), 2, "heartbeat-only bytes must not block pre-output failover to backup account")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.Contains(t, rec.Body.String(), "answer from second account")
}

func TestOpenAIChatCompletions_ReleasedRoleChunkBlocksFailover(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("y", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"

	rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenAI, preContentAccounts(2), largeBody, "", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
		return func(call int, _ *http.Request, _ int64) (*http.Response, error) {
			if call == 1 {
				pr, pw := io.Pipe()
				go func() {
					// response.created queues role chunk; reasoningAdded releases and flushes it,
					// so semantic output has started and subsequent server_error must not fail over.
					_, _ = io.WriteString(pw, preContentHandlerCreated)
					_, _ = io.WriteString(pw, reasoningAdded)
					if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
						return
					}
					_, _ = io.WriteString(pw, preContentHandlerFailed)
					_ = pw.Close()
				}()
				return preContentSSEResponse(pr), nil
			}
			return preContentSSEResponse(io.NopCloser(strings.NewReader(preContentHandlerSuccess))), nil
		}
	})

	require.Len(t, upstream.calls(), 1, "once role chunk has been flushed, handler must not fail over to another account")
	require.Contains(t, rec.Body.String(), `"role":"assistant"`)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.NotContains(t, rec.Body.String(), "answer from second account")
}

func TestOpenAIChatCompletions_HeartbeatOnlyFailoverExhaustedWritesSSEError(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("z", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"

	rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenAI, preContentAccounts(1), largeBody, "", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
		return func(_ int, _ *http.Request, _ int64) (*http.Response, error) {
			pr, pw := io.Pipe()
			go func() {
				_, _ = io.WriteString(pw, reasoningAdded)
				if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
					return
				}
				_, _ = io.WriteString(pw, preContentHandlerFailed)
				_ = pw.Close()
			}()
			return preContentSSEResponse(pr), nil
		}
	})

	require.Len(t, upstream.calls(), 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.Contains(t, rec.Body.String(), "event: error")
}

func assertSingleSSEErrorFrameAfterHeartbeats(t *testing.T, body string) string {
	t.Helper()
	require.True(t, strings.HasPrefix(body, ":\n\n"), "body must begin with heartbeat comment: %q", body)
	rest := body
	for strings.HasPrefix(rest, ":\n\n") {
		rest = strings.TrimPrefix(rest, ":\n\n")
	}
	require.NotContains(t, rest, "event: error", "must not append duplicate fallback event: error")
	require.True(t, strings.HasPrefix(rest, "data: "), "expected single SSE data frame after heartbeats, got %q", rest)
	require.True(t, strings.HasSuffix(rest, "\n\n"), "expected SSE double newline terminator, got %q", rest)

	payloadText := strings.TrimSuffix(strings.TrimPrefix(rest, "data: "), "\n\n")
	require.NotContains(t, payloadText, "\n", "must contain strictly one SSE frame, got %q", rest)
	require.True(t, json.Valid([]byte(payloadText)), "SSE frame payload must be valid JSON: %q", payloadText)
	return payloadText
}

func TestOpenAIChatCompletions_HeartbeatOnlyBackupLocalRejectionWritesSingleSSEError(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("k", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"

	accounts := []service.Account{
		{
			ID: 9201, Name: "openai-apikey-1", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		},
		{
			ID: 9202, Name: "openai-oauth-2", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2,
			Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
			Extra:       map[string]any{"codex_cli_only": true},
		},
	}

	rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenAI, accounts, largeBody, "curl/8.0", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
		return func(_ int, _ *http.Request, _ int64) (*http.Response, error) {
			pr, pw := io.Pipe()
			go func() {
				_, _ = io.WriteString(pw, reasoningAdded)
				if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
					return
				}
				_, _ = io.WriteString(pw, preContentHandlerFailed)
				_ = pw.Close()
			}()
			return preContentSSEResponse(pr), nil
		}
	})

	hits := upstream.calls()
	require.Len(t, hits, 1)
	require.Equal(t, int64(9201), hits[0].accountID)
	require.Equal(t, http.StatusOK, rec.Code)

	payloadText := assertSingleSSEErrorFrameAfterHeartbeats(t, rec.Body.String())
	require.Equal(t, "forbidden_error", gjson.Get(payloadText, "error.type").String())
	require.Equal(t, "This account only allows Codex official clients", gjson.Get(payloadText, "error.message").String())
}

func TestOpenAIChatCompletions_HeartbeatOnlyPreservesBackupStructuredInputFallback(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("m", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"
	invalidTypeBody := `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
	chatFallbackSSE := strings.Join([]string{
		`data: {"id":"chatcmpl_fb","object":"chat.completion.chunk","created":1,"model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"fallback ok"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_fb","object":"chat.completion.chunk","created":1,"model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	for _, withHeartbeat := range []bool{false, true} {
		name := "without_heartbeat"
		if withHeartbeat {
			name = "with_heartbeat"
		}
		t.Run(name, func(t *testing.T) {
			accounts := []service.Account{
				{
					ID: 9201, Name: "openai-apikey-1", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
				},
				{
					ID: 9202, Name: "openai-apikey-2", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
					Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
				},
			}

			rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenAI, accounts, largeBody, "", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
				return func(_ int, req *http.Request, accountID int64) (*http.Response, error) {
					if accountID == 9201 {
						if !withHeartbeat {
							return preContentSSEResponse(io.NopCloser(strings.NewReader(preContentHandlerFailed))), nil
						}
						pr, pw := io.Pipe()
						go func() {
							_, _ = io.WriteString(pw, reasoningAdded)
							if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
								return
							}
							_, _ = io.WriteString(pw, preContentHandlerFailed)
							_ = pw.Close()
						}()
						return preContentSSEResponse(pr), nil
					}
					if strings.HasSuffix(req.URL.Path, "/responses") {
						return &http.Response{
							StatusCode: http.StatusBadRequest,
							Header:     http.Header{"Content-Type": []string{"application/json"}},
							Body:       io.NopCloser(strings.NewReader(invalidTypeBody)),
						}, nil
					}
					return preContentSSEResponse(io.NopCloser(strings.NewReader(chatFallbackSSE))), nil
				}
			})

			hits := upstream.calls()
			require.Len(t, hits, 3)
			require.Equal(t, []int64{9201, 9202, 9202}, []int64{hits[0].accountID, hits[1].accountID, hits[2].accountID})
			require.True(t, strings.HasSuffix(hits[0].path, "/responses"))
			require.True(t, strings.HasSuffix(hits[1].path, "/responses"))
			require.True(t, strings.HasSuffix(hits[2].path, "/chat/completions"))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "fallback ok")
			require.NotContains(t, rec.Body.String(), "invalid_type")
			if withHeartbeat {
				require.Contains(t, rec.Body.String(), ":\n\n")
			}
		})
	}
}

func TestOpenAIChatCompletions_OpenCodeUnsupportedModelErrorFormat(t *testing.T) {
	largeBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("u", 70*1024) + `"}],"stream":true}`
	reasoningAdded := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"
	openCodeAccount := func(id int64, priority int, mappedModel string) service.Account {
		return service.Account{
			ID: id, Name: "opencode-go", Platform: service.PlatformOpenCodeGo, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: priority,
			Credentials: map[string]any{
				"api_key":  "sk-opencode",
				"base_url": "http://upstream.example/v1",
				"model_mapping": map[string]any{
					"gpt-5.4": mappedModel,
				},
			},
		}
	}

	t.Run("first_write_json", func(t *testing.T) {
		accounts := []service.Account{openCodeAccount(9202, 1, "gemini-2.5-pro")}
		rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenCodeGo, accounts, largeBody, "", func(<-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
			return func(_ int, _ *http.Request, _ int64) (*http.Response, error) {
				t.Fatal("unexpected upstream call when OpenCode rejects unsupported model locally")
				return nil, nil
			}
		})

		require.Empty(t, upstream.calls())
		require.Equal(t, http.StatusBadRequest, rec.Code)
		rawBody := rec.Body.Bytes()
		require.True(t, json.Valid(rawBody), "expected valid JSON body, got %q", string(rawBody))
		require.Equal(t, "invalid_request_error", gjson.GetBytes(rawBody, "error.type").String())
		require.Equal(t, "model_not_supported", gjson.GetBytes(rawBody, "error.code").String())
		msg := gjson.GetBytes(rawBody, "error.message").String()
		require.Contains(t, msg, "gemini-2.5-pro")
		require.Contains(t, msg, "not supported on OpenCode standard gateway")
	})

	t.Run("after_heartbeat_single_sse_frame", func(t *testing.T) {
		accounts := []service.Account{
			openCodeAccount(9201, 1, "gpt-5.4"),
			openCodeAccount(9202, 2, "gemini-2.5-pro"),
		}
		rec, upstream := serveChatKeepaliveFailoverScenario(t, service.PlatformOpenCodeGo, accounts, largeBody, "", func(heartbeatFlushed <-chan struct{}) func(int, *http.Request, int64) (*http.Response, error) {
			return func(_ int, _ *http.Request, accountID int64) (*http.Response, error) {
				require.Equal(t, int64(9201), accountID)
				pr, pw := io.Pipe()
				go func() {
					_, _ = io.WriteString(pw, reasoningAdded)
					if !waitHeartbeatOrCloseError(pw, heartbeatFlushed) {
						return
					}
					_, _ = io.WriteString(pw, preContentHandlerFailed)
					_ = pw.Close()
				}()
				return preContentSSEResponse(pr), nil
			}
		})

		hits := upstream.calls()
		require.Len(t, hits, 1)
		require.Equal(t, int64(9201), hits[0].accountID)
		require.Equal(t, http.StatusOK, rec.Code)

		payloadText := assertSingleSSEErrorFrameAfterHeartbeats(t, rec.Body.String())
		require.Equal(t, "invalid_request_error", gjson.Get(payloadText, "error.type").String())
		require.Equal(t, "model_not_supported", gjson.Get(payloadText, "error.code").String())
		msg := gjson.Get(payloadText, "error.message").String()
		require.Contains(t, msg, "gemini-2.5-pro")
		require.Contains(t, msg, "not supported on OpenCode standard gateway")
	})
}
