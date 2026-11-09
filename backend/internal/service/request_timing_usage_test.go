package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/stretchr/testify/require"
)

type timingUsageTestSink struct{ patches []requesttiming.TimingPatch }

func (s *timingUsageTestSink) Enqueue(p requesttiming.TimingPatch) bool {
	s.patches = append(s.patches, p)
	return true
}

type timingUsageTestRepo struct {
	UsageLogRepository
	last *UsageLog
}

func (r *timingUsageTestRepo) Create(_ context.Context, l *UsageLog) (bool, error) {
	l.ID = 42
	l.CreatedAt = time.Now().UTC()
	r.last = l
	return true, nil
}

func TestRequestTimingUsageCannotPublishFinalizedIneligibleOrPartialSpans(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "ineligible", true: "disconnect_after_eligibility"}[partial], func(t *testing.T) {
			sink := &timingUsageTestSink{}
			handle := requesttiming.New(time.Now().Add(-time.Second), sink)
			handle.RecordBodyBytes(128)
			if partial {
				handle.MarkEligible()
			}
			handle.Finalize(time.Now())
			repo := &timingUsageTestRepo{}
			billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(repo, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Timing: handle, Result: &OpenAIForwardResult{RequestID: "client:timing-unit", Model: "gpt-5.1", Stream: true, ClientDisconnect: partial, Duration: time.Millisecond * 50}, APIKey: &APIKey{ID: 1000, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 2000}, Account: &Account{ID: 3000, Type: AccountTypeAPIKey}})
			require.NoError(t, err)
			require.NotNil(t, repo.last)
			require.Nil(t, repo.last.GatewayLatencyMs, "known completion must not bypass eligibility")
			require.EqualValues(t, 128, *repo.last.BodyBytes)
			require.Empty(t, sink.patches)
		})
	}
}

func TestRequestTimingAuxiliaryRowsDoNotClaimPrimaryHandle(t *testing.T) {
	sink := &timingUsageTestSink{}
	handle := requesttiming.New(time.Now().Add(-time.Second), sink)
	handle.MarkEligible()
	handle.Finalize(time.Now())
	repo := &timingUsageTestRepo{}
	for _, id := range []string{"web_search:extra", "grok-video:task", "gcache:create:cache"} {
		writeUsageLogBestEffort(context.Background(), repo, &UsageLog{RequestID: id, APIKeyID: 1}, handle, "test")
	}
	require.Empty(t, sink.patches)
	writeUsageLogBestEffort(context.Background(), repo, &UsageLog{RequestID: "client:primary", APIKeyID: 1}, handle, "test")
	require.Len(t, sink.patches, 1)
	require.Equal(t, "client:primary", sink.patches[0].Row.RequestID)
}
