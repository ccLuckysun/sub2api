//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestOpsNativeOpenAISwitch(t *testing.T) {
	for _, endpoint := range []string{"Responses", "Messages", "Chat"} {
		for _, scenario := range []string{"switch", "exhausted", "budget_zero", "canceled", "same_account_retry"} {
			t.Run(endpoint+"/"+scenario, func(t *testing.T) {
				ctx, rec, m := opsRequestFixture(t)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				upstream := &openAIResponsesFailoverCancelUpstream{}
				if scenario == "canceled" {
					upstream.onFirstDo = cancel
				}
				h := newOpenAIResponsesFailoverTestHandler(t, upstream, func(accounts *[]service.Account) {
					if scenario == "exhausted" {
						*accounts = (*accounts)[:1]
					}
					if scenario == "same_account_retry" {
						for i := range *accounts {
							(*accounts)[i].Type = service.AccountTypeAPIKey
							(*accounts)[i].Credentials = map[string]any{"api_key": "local-key", "pool_mode": true, "pool_mode_retry_count": 1, "pool_mode_retry_status_codes": []any{float64(520)}}
						}
					}
				})
				h.maxAccountSwitches = 1
				if scenario == "budget_zero" || scenario == "same_account_retry" {
					h.maxAccountSwitches = 0
				}
				c, _ := newOpenAIResponsesFailoverTestContext(t, ctx)
				apiKey, _ := middleware2.GetAPIKeyFromContext(c)
				apiKey.Group.AllowMessagesDispatch = true
				if endpoint != "Responses" {
					body := `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`
					c.Request.Body = io.NopCloser(strings.NewReader(body))
					c.Request.ContentLength = int64(len(body))
				}
				switch endpoint {
				case "Responses":
					h.Responses(c)
				case "Messages":
					h.Messages(c)
				case "Chat":
					h.ChatCompletions(c)
				}
				want := 0.0
				if scenario == "switch" {
					want = 1
					require.Equal(t, []int64{1, 2}, upstream.calls())
				} else if scenario == "same_account_retry" {
					require.Equal(t, []int64{1, 1}, upstream.calls(), "same account retry never increments M7")
				} else {
					require.Equal(t, []int64{1}, upstream.calls())
				}
				rec.Finalize(monitoring.FinalizeInput{Status: http.StatusBadGateway})
				require.Equal(t, want, testutil.ToFloat64(m.AccountSwitches), "budget exhaustion/cancel are not switches")
			})
		}
	}
}

// First selection, same-account retry and canceled contexts must not add M7.
func TestOpsNativeSwitchContextBoundary(t *testing.T) {
	ctx, rec, m := opsRequestFixture(t)
	var previousAccountID int64
	markForwardedAccountSwitch(ctx, &previousAccountID, 1)
	markForwardedAccountSwitch(ctx, &previousAccountID, 1)
	markForwardedAccountSwitch(ctx, &previousAccountID, 2)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	markForwardedAccountSwitch(canceled, &previousAccountID, 3)
	require.Equal(t, int64(2), previousAccountID)
	markForwardedAccountSwitch(context.Background(), &previousAccountID, 4)
	markForwardedAccountSwitch(nil, &previousAccountID, 5)
	rec.Finalize(monitoring.FinalizeInput{Status: 200})
	require.Equal(t, 1.0, testutil.ToFloat64(m.AccountSwitches))
}
