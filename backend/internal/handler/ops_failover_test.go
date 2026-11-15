package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestOpsFailoverSwitchOnlyAcrossAccounts(t *testing.T) {
	ctx, _, m := opsRequestFixture(t)
	fs := NewFailoverState(2, false)
	unscheduler := &mockTempUnscheduler{}
	retry := &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, SameAccountRetryDelay: time.Nanosecond}
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, unscheduler, 101, service.PlatformAnthropic, 1, retry))
	require.Zero(t, fs.SwitchCount)
	require.Equal(t, 0.0, testutil.ToFloat64(m.AccountSwitches), "same-account retry is not a switch")
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, unscheduler, 101, service.PlatformAnthropic, 1, retry))
	require.Equal(t, 1, fs.SwitchCount)
	require.Equal(t, 1.0, testutil.ToFloat64(m.AccountSwitches))
	require.Len(t, unscheduler.calls, 1)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, unscheduler, 102, service.PlatformAnthropic, 1, &service.UpstreamFailoverError{StatusCode: 502}))
	require.Equal(t, 2, fs.SwitchCount)
	require.Equal(t, 2.0, testutil.ToFloat64(m.AccountSwitches))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, unscheduler, 103, service.PlatformAnthropic, 1, &service.UpstreamFailoverError{StatusCode: 502}))
	require.Equal(t, 2.0, testutil.ToFloat64(m.AccountSwitches), "exhaustion is not an additional switch")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Equal(t, FailoverCanceled, fs.HandleFailoverError(canceled, unscheduler, 104, service.PlatformAnthropic, 1, retry))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, unscheduler, 104, service.PlatformAnthropic, 1, nil))
	fs.RecordProfitVeto(105)
	require.Equal(t, 2.0, testutil.ToFloat64(m.AccountSwitches), "cancel/terminal error/profit veto do not add switches")
}
