package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTimingScopeUnknownLegacyPlatformIsCountedOnce(t *testing.T) {
	legacy := &timingLeaf{platform: "", group: 0, acc: newTimingAccumulator()}
	legacy.acc.quality = service.TimingQuality{CandidateCount: 7}
	known := &timingLeaf{platform: "openai", group: 1, acc: newTimingAccumulator()}
	known.acc.quality = service.TimingQuality{CandidateCount: 3}
	leaves := map[string]*timingLeaf{"/0": legacy, "openai/1": known}
	require.NoError(t, addTimingScopeSummaries(leaves))
	require.EqualValues(t, 10, leaves["/-1"].acc.quality.CandidateCount)
	require.EqualValues(t, 3, leaves["openai/-1"].acc.quality.CandidateCount)
}
