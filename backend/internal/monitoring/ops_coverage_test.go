package monitoring

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestCoverageMinuteSwap(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 20, 0, time.UTC)
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	c := NewOpsCoverage(m.Coverage, func() time.Time { return now })
	assert := func(want map[string]float64) {
		for kind, v := range want {
			require.Equal(t, v, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)), kind)
		}
	}
	assert(map[string]float64{"request": -1, "account": -1, "bytes": -1, "user": -1})
	c.Add(CoverageSample{true, true, true, true})
	c.Add(CoverageSample{Request: true})
	c.Flush() // Incomplete minute must not be published or cleared.
	assert(map[string]float64{"request": -1})
	now = now.Add(time.Minute)
	c.Flush()
	assert(map[string]float64{"request": 1, "account": 0.5, "bytes": 0.5, "user": 0.5})
	c.Add(CoverageSample{}) // Uninstrumented entry remains in denominator.
	c.Add(CoverageSample{Account: true, User: true})
	now = now.Add(time.Minute)
	c.Add(CoverageSample{Bytes: true}) // Add rolls the previous minute atomically.
	assert(map[string]float64{"request": 0, "account": 0.5, "bytes": 0, "user": 0.5})
	c.Flush() // Same-minute flush is idempotent.
	assert(map[string]float64{"request": 0})
	now = now.Add(time.Minute)
	c.Flush()
	assert(map[string]float64{"request": 0, "account": 0, "bytes": 1, "user": 0})
	now = now.Add(time.Minute)
	c.Flush()
	assert(map[string]float64{"request": -1, "account": -1, "bytes": -1, "user": -1})
	c.Add(CoverageSample{true, true, true, true})
	now = now.Add(2 * time.Minute)
	c.Flush() // Last completed minute was idle, even though an older bucket wasn't.
	assert(map[string]float64{"request": -1})
}

func TestCoverageConcurrentWholeBucket(t *testing.T) {
	// Real clock dependency remains injectable without a timer/goroutine lifecycle.
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	c := NewOpsCoverage(m.Coverage, func() time.Time { return now })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Add(CoverageSample{true, true, true, true})
				c.Flush()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, coverageCounts{20000, 20000, 20000, 20000, 20000}, c.counts)
	now = now.Add(time.Minute)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Flush(); c.Add(CoverageSample{true, true, true, true}) }()
	}
	wg.Wait()
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)))
	}
	require.Equal(t, coverageCounts{20, 20, 20, 20, 20}, c.counts)
	require.NotNil(t, NewOpsCoverage(nil, nil))
	var nilCoverage *OpsCoverage
	nilCoverage.Add(CoverageSample{})
	nilCoverage.Flush()
}

func TestCoverageEntryAndLateCompletion(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	c := NewOpsCoverage(m.Coverage, func() time.Time { return now })
	all := CoverageSample{true, true, true, true}
	first := c.Entry()
	late := c.Entry()
	never := c.Entry()
	require.NotNil(t, never)
	require.Equal(t, coverageCounts{entry: 3}, c.counts, "entry denominator exists before any completion")
	c.Complete(first, all)
	now = now.Add(time.Minute)
	c.Flush()
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.InDelta(t, 1.0/3, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)), 1e-9)
	}
	current := c.Entry()
	c.Complete(current, CoverageSample{User: true})
	c.Complete(late, all) // Revises the published ENTRY minute, not current minute.
	c.Complete(late, all) // Idempotent even for an already published bucket.
	require.Equal(t, coverageCounts{entry: 1, user: 1}, c.counts)
	require.Equal(t, coverageCounts{3, 2, 2, 2, 2}, c.previous)
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.InDelta(t, 2.0/3, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)), 1e-9)
	}
	now = now.Add(time.Minute)
	c.Flush()
	require.Equal(t, 0.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("user")))
	c.Complete(never, all) // Two-minute-old completion is dropped, with debug log.
	require.Equal(t, coverageCounts{}, c.counts)
	require.Equal(t, coverageCounts{entry: 1, user: 1}, c.previous)
	require.Equal(t, 0.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")))
	for i := 0; i < 3; i++ {
		now = now.Add(time.Minute)
		c.Flush()
		for _, kind := range []string{"request", "account", "bytes", "user"} {
			require.Equal(t, -1.0, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)))
		}
	}
	// A completion can itself cause rollover, without waiting for scheduled Flush.
	late = c.Entry()
	now = now.Add(time.Minute)
	c.Complete(late, all)
	require.Equal(t, coverageCounts{}, c.counts)
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)))
	}
	now = now.Add(3 * time.Minute)
	c.Flush()
	c.Complete(c.Entry(), CoverageSample{})
	require.Equal(t, -1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")), "skipped idle minutes do not publish stale numerators")
}

func TestCoverageTokenValidationAndRatioBounds(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	c := NewOpsCoverage(m.Coverage, func() time.Time { return now })
	other := NewOpsCoverage(nil, func() time.Time { return now })
	token := c.Entry()
	c.Complete(nil, CoverageSample{true, true, true, true})
	other.Complete(token, CoverageSample{true, true, true, true})
	require.Equal(t, coverageCounts{}, other.counts)
	c.Complete(token, CoverageSample{true, true, true, true})
	c.Complete(token, CoverageSample{true, true, true, true})
	require.Equal(t, coverageCounts{1, 1, 1, 1, 1}, c.counts)
	now = now.Add(time.Minute)
	c.Flush()
	c.mu.Lock()
	c.publish(coverageCounts{entry: 1, request: 2, account: 100, bytes: 1})
	c.mu.Unlock()
	for _, kind := range []string{"request", "account", "bytes"} {
		require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)), kind)
	}
	require.Equal(t, 0.0, testutil.ToFloat64(m.Coverage.WithLabelValues("user")))
	c.mu.Lock()
	c.publish(coverageCounts{request: 1})
	c.mu.Unlock()
	require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")), "anomalous positive numerator without entry warns and publishes 1")
	require.Equal(t, -1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("user")))
	var disabled *OpsCoverage
	require.Nil(t, disabled.Entry())
	disabled.Complete(token, CoverageSample{})
}

func TestCoverageConcurrentEntryCompleteFlush(t *testing.T) {
	clock := &recorderClock{}
	m, err := NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	c := NewOpsCoverage(m.Coverage, clock.now)
	tokens := make([]*CoverageToken, 1000)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) { defer wg.Done(); tokens[i] = c.Entry(); c.Flush() }(i)
	}
	wg.Wait()
	c.Entry() // Unfinished entry must keep the denominator greater than numerator.
	require.EqualValues(t, 1001, c.counts.entry)
	clock.at(60000)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Complete(tokens[i], CoverageSample{true, true, true, true})
			c.Complete(tokens[i], CoverageSample{true, true, true, true})
			c.Flush()
			current := c.Entry()
			c.Complete(current, CoverageSample{User: true})
		}(i)
	}
	wg.Wait()
	require.Equal(t, coverageCounts{1001, 1000, 1000, 1000, 1000}, c.previous)
	require.Equal(t, coverageCounts{entry: 1000, user: 1000}, c.counts)
	for _, kind := range []string{"request", "account", "bytes", "user"} {
		require.InDelta(t, 1000.0/1001, testutil.ToFloat64(m.Coverage.WithLabelValues(kind)), 1e-9)
	}
	clock.at(120000)
	c.Flush()
	require.Equal(t, 0.0, testutil.ToFloat64(m.Coverage.WithLabelValues("request")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.Coverage.WithLabelValues("user")))
	require.Equal(t, coverageCounts{}, c.counts)
}
