package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsUsageFieldsOpenAIRecordUsageMapping(t *testing.T) {
	zero, body, latency := int64(0), int64(12345), int64(17)
	for _, tc := range []struct {
		name          string
		body, latency *int64
	}{
		{"null", nil, nil}, {"explicit zero", &zero, &zero}, {"snapshot values", &body, &latency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			users := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(repo, users, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "usage-fields", Model: "gpt-5", Duration: time.Second, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 6}},
				APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3}, BodyBytes: tc.body, GatewayLatencyMs: tc.latency,
			})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.Equal(t, tc.body, repo.lastLog.BodyBytes)
			require.Equal(t, tc.latency, repo.lastLog.GatewayLatencyMs)
			require.Equal(t, 10, repo.lastLog.InputTokens)
			require.Equal(t, 6, repo.lastLog.OutputTokens)
		})
	}
}
