package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
)

func timingAlertTestRule(metric, operator string, threshold float64, sustained, recovery int) *OpsAlertRule {
	filters := map[string]any{}
	if recovery >= 0 {
		filters["recovery_minutes"] = recovery
	}
	return &OpsAlertRule{
		ID:               7,
		Name:             "timing rule",
		Enabled:          true,
		Severity:         "P2",
		MetricType:       metric,
		Operator:         operator,
		Threshold:        threshold,
		WindowMinutes:    1,
		SustainedMinutes: sustained,
		Filters:          filters,
	}
}

func uploadAlertBucket(at time.Time, values []float64, candidate, uploads int64) *RequestTimingAlertBucket {
	h := stats.New(stats.Upload)
	for _, v := range values {
		if err := h.Add(v); err != nil {
			panic(err)
		}
	}
	return &RequestTimingAlertBucket{
		BucketStart:    at,
		CandidateCount: candidate,
		UploadCount:    uploads,
		Upload:         h,
	}
}

func TestEvaluateRequestTimingAlertBucketNeverTreatsMissingOrDirtyAsHealthy(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	dirty := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at, repeat(9000, 40), 40, 40), true, at)
	if dirty.State != TimingAlertStateStale || dirty.Reason != "bucket_pending_aggregation" {
		t.Fatalf("dirty bucket must be stale, got %+v", dirty)
	}
	missing := EvaluateRequestTimingAlertBucket(rule, nil, false, at)
	if missing.State != TimingAlertStateStale || missing.Reason != "bucket_missing" {
		t.Fatalf("missing bucket must be stale, got %+v", missing)
	}
	mismatch := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at.Add(-time.Minute), repeat(9000, 40), 40, 40), false, at)
	if mismatch.State != TimingAlertStateStale || mismatch.Reason != "bucket_mismatch" {
		t.Fatalf("misaligned bucket must be stale, got %+v", mismatch)
	}
}

func TestEvaluateRequestTimingAlertBucketEnforcesSampleAndCoverageFloors(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	few := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at, repeat(9000, 29), 29, 29), false, at)
	if few.State != TimingAlertStateInsufficient || few.Reason != "insufficient_samples" {
		t.Fatalf("29 samples must be insufficient, got %+v", few)
	}

	// 95 uploads out of 100 candidates is exactly the 95% floor and must pass.
	atFloor := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at, repeat(9000, 95), 100, 95), false, at)
	if atFloor.State == TimingAlertStateInsufficient {
		t.Fatalf("exactly 95%% coverage must not be insufficient: %+v", atFloor)
	}
	if atFloor.Coverage == nil || math.Abs(*atFloor.Coverage-0.95) > 1e-9 {
		t.Fatalf("coverage must be 0.95, got %+v", atFloor.Coverage)
	}
	belowCoverage := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at, repeat(9000, 94), 100, 94), false, at)
	if belowCoverage.State != TimingAlertStateInsufficient || belowCoverage.Reason != "insufficient_coverage" {
		t.Fatalf("94%% coverage must be insufficient, got %+v", belowCoverage)
	}
}

func TestBoundsCrossesThresholdClosedInterval(t *testing.T) {
	hi := func(v float64) *float64 { return &v }
	cases := []struct {
		name      string
		bounds    *stats.Bounds
		operator  string
		threshold float64
		ambiguous bool
	}{
		{"gt lower edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">", 100, true},
		{"gt upper edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">", 110, false},
		{"gt above the bucket", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">", 99, false},
		{"gt below the bucket", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">", 111, false},
		{"gt zero upper edge", &stats.Bounds{Lo: 0, Hi: hi(0)}, ">", 0, false},
		{"gte lower edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">=", 100, false},
		{"gte upper edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, ">=", 110, true},
		{"lt upper edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, "<", 110, true},
		{"lt lower edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, "<", 100, false},
		{"lte upper edge equals threshold", &stats.Bounds{Lo: 100, Hi: hi(110)}, "<=", 110, false},
		{"eq inside bucket", &stats.Bounds{Lo: 100, Hi: hi(110)}, "==", 105, true},
		{"eq outside bucket", &stats.Bounds{Lo: 100, Hi: hi(110)}, "==", 200, false},
		{"degenerate exact equals threshold", &stats.Bounds{Lo: 42, Hi: hi(42)}, ">", 42, false},
		{"degenerate zero", &stats.Bounds{Lo: 0, Hi: hi(0)}, ">=", 0, false},
		{"unbounded above", &stats.Bounds{Lo: 100}, ">", 1000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundsCrossesThreshold(tc.bounds, tc.operator, tc.threshold); got != tc.ambiguous {
				t.Fatalf("boundsCrossesThreshold(%+v, %q, %v) = %v, want %v", tc.bounds, tc.operator, tc.threshold, got, tc.ambiguous)
			}
		})
	}
}

func TestEvaluateRequestTimingAlertBucketClassifiesBreachAndAmbiguity(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	bucket := uploadAlertBucket(at, repeat(9000, 40), 40, 40)
	probe, _ := bucket.Upload.Quantile(0.90)
	if probe == nil {
		t.Fatal("expected a quantile estimate")
	}
	_, bounds := bucket.Upload.Quantile(0.90)
	if bounds == nil {
		t.Fatal("expected bounds")
	}

	// A threshold exactly on the bucket's lower edge is still undecided for a strict
	// ">": the bucket is a closed interval, so a value may equal the threshold.
	atEdge := timingAlertTestRule("upload_ms_p90", ">", bounds.Lo, 5, 2)
	edge := EvaluateRequestTimingAlertBucket(atEdge, bucket, false, at)
	if !edge.Ambiguous || edge.State != TimingAlertStateUnknown {
		t.Fatalf("threshold on the bucket lower edge must be ambiguous: %+v", edge)
	}

	// A threshold exactly on the upper edge is decided: every value is <= hi == T.
	if bounds.Hi != nil {
		atTop := timingAlertTestRule("upload_ms_p90", ">", *bounds.Hi, 5, 2)
		top := EvaluateRequestTimingAlertBucket(atTop, bucket, false, at)
		if top.Ambiguous || top.State != TimingAlertStateOK {
			t.Fatalf("threshold on the bucket upper edge must be decided as healthy: %+v", top)
		}

		// Strictly inside the bucket that owns p90 must be flagged for raw confirmation.
		straddle := timingAlertTestRule("upload_ms_p90", ">", bounds.Lo+(*bounds.Hi-bounds.Lo)/2, 5, 2)
		ambiguous := EvaluateRequestTimingAlertBucket(straddle, bucket, false, at)
		if !ambiguous.Ambiguous || ambiguous.Reason != "histogram_boundary" {
			t.Fatalf("threshold inside the p90 bucket must be ambiguous: %+v", ambiguous)
		}
	}

	// Clearly above the bucket: the breach is definite without a recheck.
	clear := timingAlertTestRule("upload_ms_p90", ">", bounds.Lo-1000, 5, 2)
	verdict := EvaluateRequestTimingAlertBucket(clear, bucket, false, at)
	if verdict.Ambiguous || verdict.State != TimingAlertStateBreached {
		t.Fatalf("threshold below the bucket must be a definite breach: %+v", verdict)
	}

	healthy := timingAlertTestRule("upload_ms_p90", ">", 60000, 5, 2)
	if got := EvaluateRequestTimingAlertBucket(healthy, bucket, false, at); got.State != TimingAlertStateOK {
		t.Fatalf("9000ms must be healthy against 60s, got %+v", got)
	}
}

func TestEvaluateRequestTimingAlertBucketRejectsUnknownMetric(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	rule := timingAlertTestRule("upload_p95", ">", 8000, 5, 2)
	got := EvaluateRequestTimingAlertBucket(rule, uploadAlertBucket(at, repeat(9000, 40), 40, 40), false, at)
	if got.State != TimingAlertStateUnknown || got.Reason != "unsupported_metric" {
		t.Fatalf("non-timing metric must be unknown, got %+v", got)
	}
}

func TestEvaluateRequestTimingAlertWindowFiresOnlyAfterConsecutiveBuckets(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	buckets := func(breachMinutes int) map[time.Time]*RequestTimingAlertBucket {
		out := map[time.Time]*RequestTimingAlertBucket{}
		for i := 0; i < breachMinutes; i++ {
			at := latest.Add(-time.Duration(i) * time.Minute)
			out[at] = uploadAlertBucket(at, repeat(9000, 40), 40, 40)
		}
		return out
	}

	decision, _ := EvaluateRequestTimingAlertWindow(rule, buckets(5), nil, latest, earliest)
	if !decision.ShouldFire || decision.BreachStreak != 5 {
		t.Fatalf("5 consecutive breached minutes must fire: %+v", decision)
	}

	decision, _ = EvaluateRequestTimingAlertWindow(rule, buckets(4), nil, latest, earliest)
	if decision.ShouldFire || decision.BreachStreak != 4 {
		t.Fatalf("4 breached minutes must not fire: %+v", decision)
	}
}

func TestEvaluateRequestTimingAlertWindowStreakBreaksOnUnknownOrStale(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	// 3 breached, one minute missing (stale), then 2 more breached minutes.
	buckets := map[time.Time]*RequestTimingAlertBucket{}
	for _, offset := range []int{0, 1, 3, 4} {
		at := latest.Add(-time.Duration(offset) * time.Minute)
		buckets[at] = uploadAlertBucket(at, repeat(9000, 40), 40, 40)
	}
	decision, verdicts := EvaluateRequestTimingAlertWindow(rule, buckets, nil, latest, earliest)
	if decision.State != TimingAlertStateBreached {
		t.Fatalf("newest bucket is breached, got %+v", decision)
	}
	if decision.ShouldFire || decision.BreachStreak != 2 {
		t.Fatalf("only the 2 trailing minutes count, got %+v", decision)
	}
	if len(verdicts) == 0 || verdicts[2].Reason != "bucket_missing" {
		t.Fatalf("expected a bucket_missing verdict at position 2, got %+v", verdicts)
	}

	// A dirty marker on the newest bucket blocks firing exactly like a missing one.
	dirty := map[time.Time]struct{}{latest: {}}
	decision, _ = EvaluateRequestTimingAlertWindow(rule, buckets, dirty, latest, earliest)
	if decision.State != TimingAlertStateStale || decision.ShouldFire {
		t.Fatalf("dirty bucket must block firing, got %+v", decision)
	}
}

func TestEvaluateRequestTimingAlertWindowResolvesAfterRecoveryBuckets(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	twoHealthy := map[time.Time]*RequestTimingAlertBucket{}
	for i := 0; i < 2; i++ {
		at := latest.Add(-time.Duration(i) * time.Minute)
		twoHealthy[at] = uploadAlertBucket(at, repeat(200, 40), 40, 40)
	}
	decision, _ := EvaluateRequestTimingAlertWindow(rule, twoHealthy, nil, latest, earliest)
	if !decision.ShouldResolve || decision.HealthStreak != 2 {
		t.Fatalf("2 healthy minutes must resolve: %+v", decision)
	}

	oneHealthy := map[time.Time]*RequestTimingAlertBucket{
		latest: uploadAlertBucket(latest, repeat(200, 40), 40, 40),
	}
	decision, _ = EvaluateRequestTimingAlertWindow(rule, oneHealthy, nil, latest, earliest)
	if decision.ShouldResolve {
		t.Fatalf("a single healthy minute must not resolve: %+v", decision)
	}
}

func TestEvaluateRequestTimingAlertWindowTreatsZeroRecoveryAsManual(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, -1)
	rule.Filters = map[string]any{"recovery_minutes": 0}

	buckets := map[time.Time]*RequestTimingAlertBucket{}
	for i := 0; i < 3; i++ {
		at := latest.Add(-time.Duration(i) * time.Minute)
		buckets[at] = uploadAlertBucket(at, repeat(200, 40), 40, 40)
	}
	decision, _ := EvaluateRequestTimingAlertWindow(rule, buckets, nil, latest, earliest)
	if decision.ShouldResolve {
		t.Fatalf("recovery_minutes=0 must never auto-resolve: %+v", decision)
	}
}

func TestEvaluateRequestTimingAlertWindowLargeBodyRatio(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	rule := timingAlertTestRule("large_body_ratio", ">", 0.5, 1, 2)

	bucket := &RequestTimingAlertBucket{
		BucketStart:    latest,
		CandidateCount: 100,
		KnownBodyCount: 100,
		LargeBodyCount: 60,
	}
	decision, _ := EvaluateRequestTimingAlertWindow(rule, map[time.Time]*RequestTimingAlertBucket{latest: bucket}, nil, latest, earliest)
	if !decision.ShouldFire || decision.MetricValue == nil || math.Abs(*decision.MetricValue-0.6) > 1e-9 {
		t.Fatalf("ratio 0.6 over 0.5 must fire with value 0.6: %+v", decision)
	}
	if decision.ExactMetric != "" {
		t.Fatalf("ratio metric never needs histogram confirmation: %+v", decision)
	}

	unknownBody := &RequestTimingAlertBucket{BucketStart: latest, CandidateCount: 100, KnownBodyCount: 0}
	if got := EvaluateRequestTimingAlertBucket(rule, unknownBody, false, latest); got.State != TimingAlertStateInsufficient {
		t.Fatalf("unknown body sample count must be insufficient, got %+v", got)
	}
}

func TestEvaluateRequestTimingAlertWindowRequestsExactConfirmation(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	bucket := uploadAlertBucket(latest, repeat(9000, 40), 40, 40)
	_, bounds := bucket.Upload.Quantile(0.90)
	if bounds == nil || bounds.Hi == nil {
		t.Skip("no bounded p90 bucket to straddle")
	}
	rule := timingAlertTestRule("upload_ms_p90", ">", bounds.Lo+(*bounds.Hi-bounds.Lo)/2, 1, 2)
	decision, _ := EvaluateRequestTimingAlertWindow(rule, map[time.Time]*RequestTimingAlertBucket{latest: bucket}, nil, latest, earliest)
	if decision.ExactMetric != "upload_ms_p90" || decision.ExactPercent != 0.90 {
		t.Fatalf("straddled threshold must request raw confirmation: %+v", decision)
	}
	if decision.BreachStreak != 0 {
		t.Fatalf("ambiguous bucket must not extend a breach streak until confirmed: %+v", decision)
	}
}

func TestRequestTimingAlertWindowBoundsUseClosedLag(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 42, 500_000_000, time.UTC)
	// floor(now)=10:10, exclusive safeEnd=10:08 → newest readable bucket start is 10:07.
	if got := RequestTimingAlertNow(now); !got.Equal(time.Date(2026, 5, 1, 10, 7, 0, 0, time.UTC)) {
		t.Fatalf("newest readable bucket must be floor(now)-3m, got %s", got)
	}
	if got := RequestTimingAlertNow(time.Date(2026, 5, 1, 10, 8, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("exactly on a minute boundary the watermark is exclusive, got %s", got)
	}
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)
	if got := RequestTimingAlertWindowStart(rule, time.Date(2026, 5, 1, 10, 7, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 5, 1, 10, 3, 0, 0, time.UTC)) {
		t.Fatalf("window start must cover the sustained streak, got %s", got)
	}
}

func TestParseRequestTimingAlertSettings(t *testing.T) {
	def := ParseRequestTimingAlertSettings(&OpsAlertRule{})
	if def.MinSamples != 30 || def.MinCoverage != 0.95 || def.RecoveryMinutes != 2 {
		t.Fatalf("unexpected defaults: %+v", def)
	}
	custom := ParseRequestTimingAlertSettings(&OpsAlertRule{Filters: map[string]any{
		"min_samples":      float64(100),
		"min_coverage":     0.99,
		"recovery_minutes": float64(5),
	}})
	if custom.MinSamples != 100 || custom.MinCoverage != 0.99 || custom.RecoveryMinutes != 5 {
		t.Fatalf("filters must override defaults: %+v", custom)
	}
	invalid := ParseRequestTimingAlertSettings(&OpsAlertRule{Filters: map[string]any{
		"min_samples":      float64(-1),
		"min_coverage":     1.5,
		"recovery_minutes": "nope",
	}})
	if invalid.MinSamples != 30 || invalid.MinCoverage != 0.95 || invalid.RecoveryMinutes != 2 {
		t.Fatalf("invalid filters must fall back to defaults: %+v", invalid)
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestEvaluateRequestTimingAlertWindowStreakMustBeConsecutive(t *testing.T) {
	latest := time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC)
	earliest := latest.Add(-9 * time.Minute)
	breached := func(at time.Time) *RequestTimingAlertBucket {
		return uploadAlertBucket(at, repeat(9000, 40), 40, 40)
	}
	healthy := func(at time.Time) *RequestTimingAlertBucket {
		return uploadAlertBucket(at, repeat(200, 40), 40, 40)
	}
	at := func(offset int) time.Time { return latest.Add(-time.Duration(offset) * time.Minute) }

	// breached / healthy / breached must not count as a 2-bucket breach streak.
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 2, 2)
	mixed := map[time.Time]*RequestTimingAlertBucket{
		at(0): breached(at(0)),
		at(1): healthy(at(1)),
		at(2): breached(at(2)),
		at(3): breached(at(3)),
	}
	decision, _ := EvaluateRequestTimingAlertWindow(rule, mixed, nil, latest, earliest)
	if decision.BreachStreak != 1 || decision.ShouldFire {
		t.Fatalf("breached/healthy/breached must not fire, got %+v", decision)
	}

	// healthy / breached / healthy must not count as a 2-bucket recovery streak.
	recovering := map[time.Time]*RequestTimingAlertBucket{
		at(0): healthy(at(0)),
		at(1): breached(at(1)),
		at(2): healthy(at(2)),
		at(3): healthy(at(3)),
	}
	decision, _ = EvaluateRequestTimingAlertWindow(rule, recovering, nil, latest, earliest)
	if decision.HealthStreak != 1 || decision.ShouldResolve {
		t.Fatalf("healthy/breached/healthy must not resolve, got %+v", decision)
	}

	// Sanity: two truly consecutive buckets still fire.
	consecutive := map[time.Time]*RequestTimingAlertBucket{
		at(0): breached(at(0)),
		at(1): breached(at(1)),
	}
	decision, _ = EvaluateRequestTimingAlertWindow(rule, consecutive, nil, latest, earliest)
	if !decision.ShouldFire || decision.BreachStreak != 2 {
		t.Fatalf("two consecutive breaches must fire: %+v", decision)
	}
}

func TestEvaluateRequestTimingAlertBucketRejectsHistogramSampleMismatch(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 5, 2)

	// UploadCount claims 40 samples while the histogram only carries 39.
	bucket := uploadAlertBucket(at, repeat(9000, 39), 40, 40)
	got := EvaluateRequestTimingAlertBucket(rule, bucket, false, at)
	if got.State != TimingAlertStateUnknown || got.Reason != "histogram_sample_mismatch" {
		t.Fatalf("histogram/sample disagreement must be unknown, got %+v", got)
	}
	if got.Value != nil || got.Bounds != nil {
		t.Fatalf("mismatched histogram must not report a value: %+v", got)
	}
	// The mismatch also breaks the streak walk instead of looking healthy.
	decision, _ := EvaluateRequestTimingAlertWindow(rule, map[time.Time]*RequestTimingAlertBucket{at: bucket}, nil, at, at)
	if decision.BreachStreak != 0 || decision.HealthStreak != 0 || decision.ShouldFire {
		t.Fatalf("unknown bucket must break both streaks: %+v", decision)
	}
}

// ---- evaluator-level tests with an in-memory repository ----

type fakeTimingAlertRepo struct {
	buckets map[time.Time]*RequestTimingAlertBucket
	dirty   map[time.Time]struct{}
	exact   map[time.Time]*RequestTimingAlertSample
	exactQ  []time.Time // minutes asked for raw confirmation, in call order
	state   *RequestTimingAlertState
	events  []*OpsAlertEvent
	active  *OpsAlertEvent
	// lockCalls counts rule-lock transactions, i.e. how many timing rules the evaluator
	// actually attempted to evaluate.
	lockCalls int
}

func (f *fakeTimingAlertRepo) GetRequestTimingAlertBuckets(_ context.Context, _ *RequestTimingAlertFilter) ([]*RequestTimingAlertBucket, error) {
	out := make([]*RequestTimingAlertBucket, 0, len(f.buckets))
	for _, b := range f.buckets {
		out = append(out, b)
	}
	return out, nil
}

func (f *fakeTimingAlertRepo) GetRequestTimingDirtyMinutes(_ context.Context, _, _ time.Time) (map[time.Time]struct{}, error) {
	return f.dirty, nil
}

func (f *fakeTimingAlertRepo) GetRequestTimingAlertExact(_ context.Context, filter *RequestTimingAlertFilter, _ string, _ float64) (*RequestTimingAlertSample, error) {
	if filter != nil && filter.EndTime.Sub(filter.StartTime) != time.Minute {
		panic("alert recheck must target exactly one minute")
	}
	if filter == nil {
		return &RequestTimingAlertSample{}, nil
	}
	at := filter.StartTime.UTC()
	f.exactQ = append(f.exactQ, at)
	if sample, ok := f.exact[at]; ok {
		return sample, nil
	}
	return &RequestTimingAlertSample{}, nil
}

func (f *fakeTimingAlertRepo) ListRequestTimingAlertStates(_ context.Context, ruleIDs []int64) (map[int64]*RequestTimingAlertState, error) {
	out := map[int64]*RequestTimingAlertState{}
	if f.state == nil {
		return out, nil
	}
	for _, id := range ruleIDs {
		if id == f.state.RuleID {
			out[id] = f.state
		}
	}
	return out, nil
}

func (f *fakeTimingAlertRepo) WithRequestTimingAlertRuleLock(ctx context.Context, _ int64, fn func(context.Context, RequestTimingAlertTx) error) error {
	f.lockCalls++
	return fn(ctx, &fakeTimingAlertTx{repo: f})
}

type fakeTimingAlertTx struct{ repo *fakeTimingAlertRepo }

func (t *fakeTimingAlertTx) GetActiveAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return t.repo.active, nil
}

func (t *fakeTimingAlertTx) GetLatestAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	if len(t.repo.events) == 0 {
		return nil, nil
	}
	return t.repo.events[len(t.repo.events)-1], nil
}

func (t *fakeTimingAlertTx) CreateAlertEvent(_ context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	event.ID = int64(len(t.repo.events) + 1)
	t.repo.events = append(t.repo.events, event)
	t.repo.active = event
	return event, nil
}

func (t *fakeTimingAlertTx) UpdateAlertEventStatus(context.Context, int64, string, *time.Time) error {
	return nil
}

func (t *fakeTimingAlertTx) LoadRequestTimingAlertState(context.Context, int64) (*RequestTimingAlertState, error) {
	return t.repo.state, nil
}

func (t *fakeTimingAlertTx) SaveRequestTimingAlertState(_ context.Context, state *RequestTimingAlertState) error {
	t.repo.state = state
	return nil
}

func newTimingAlertEvaluator(repo RequestTimingAlertRepository) *OpsAlertEvaluatorService {
	svc := NewOpsAlertEvaluatorService(nil, nil, nil, nil, nil, nil)
	svc.timingAlertRepo = repo
	return svc
}

func TestEvaluateRequestTimingRuleConfirmsEveryAmbiguousMinute(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	latest := RequestTimingAlertNow(now) // 10:07
	ambiguousProbe := uploadAlertBucket(latest, repeat(100, 40), 40, 40)
	_, probeBounds := ambiguousProbe.Upload.Quantile(0.90)
	if probeBounds == nil || probeBounds.Hi == nil {
		t.Fatal("expected bounded p90 bucket")
	}
	threshold := probeBounds.Lo + (*probeBounds.Hi-probeBounds.Lo)/2
	rule := timingAlertTestRule("upload_ms_p90", ">", threshold, 2, 2)

	breachedBucket := func(at time.Time) *RequestTimingAlertBucket {
		return uploadAlertBucket(at, repeat(9000, 40), 40, 40)
	}
	ambiguousBucket := func(at time.Time) *RequestTimingAlertBucket {
		// 100ms sits inside the ~10%-wide bucket that owns p90, so the threshold drawn
		// from its bounds straddles that bucket.
		return uploadAlertBucket(at, repeat(100, 40), 40, 40)
	}

	buckets := map[time.Time]*RequestTimingAlertBucket{
		latest:                       breachedBucket(latest),
		latest.Add(-time.Minute):     ambiguousBucket(latest.Add(-time.Minute)),
		latest.Add(-2 * time.Minute): breachedBucket(latest.Add(-2 * time.Minute)),
	}
	repo := &fakeTimingAlertRepo{buckets: buckets, exact: map[time.Time]*RequestTimingAlertSample{}}
	svc := newTimingAlertEvaluator(repo)

	// The middle minute cannot be confirmed (no raw rows) → streak breaks, no fire.
	before := EvaluateRequestTimingAlertBucket(rule, buckets[latest.Add(-time.Minute)], false, latest.Add(-time.Minute))
	if !before.Ambiguous {
		t.Fatalf("expected the middle minute to be histogram-ambiguous: %+v", before)
	}
	result := svc.evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if result.Fired || result.State != TimingAlertStateUnknown {
		t.Fatalf("unconfirmed middle minute must block firing, got %+v (state=%s)", result, result.State)
	}
	if len(repo.exactQ) != 1 || !repo.exactQ[0].Equal(latest.Add(-time.Minute)) {
		t.Fatalf("only the ambiguous minute must be rechecked, got %v", repo.exactQ)
	}

	// Now the raw rows confirm the middle minute is above the threshold drawn from the
	// histogram bucket → both window minutes breach and the alert fires.
	value := 105.0
	n := int64(40)
	coverage := 1.0
	repo.exact[latest.Add(-time.Minute)] = &RequestTimingAlertSample{Candidate: 40, N: n, Value: &value, Coverage: &coverage}
	repo.exactQ = nil
	repo.events = nil
	repo.active = nil
	repo.state = nil
	result = svc.evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if !result.Fired {
		t.Fatalf("confirmed ambiguous minute must allow firing, got %+v", result)
	}
	if len(repo.exactQ) != 1 {
		t.Fatalf("exactly one minute should need confirmation, got %v", repo.exactQ)
	}
}

func TestEvaluateRequestTimingRuleRejectsLowSampleExactRecheck(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	latest := RequestTimingAlertNow(now)
	// Newest minute is ambiguous too, so firing depends entirely on the raw recheck.
	bucket := uploadAlertBucket(latest, repeat(100, 40), 40, 40)
	_, bounds := bucket.Upload.Quantile(0.90)
	if bounds == nil || bounds.Hi == nil {
		t.Fatal("expected bounded p90 bucket")
	}
	rule := timingAlertTestRule("upload_ms_p90", ">", bounds.Lo+(*bounds.Hi-bounds.Lo)/2, 2, 2)
	value := 500.0
	small := int64(10)
	coverage := 1.0
	repo := &fakeTimingAlertRepo{
		buckets: map[time.Time]*RequestTimingAlertBucket{latest: bucket},
		exact:   map[time.Time]*RequestTimingAlertSample{latest: {Candidate: 10, N: small, Value: &value, Coverage: &coverage}},
	}
	svc := newTimingAlertEvaluator(repo)

	result := svc.evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if result.Fired {
		t.Fatalf("raw recheck below min_samples must not fire: %+v", result)
	}
	if result.State != TimingAlertStateUnknown {
		t.Fatalf("unconfirmed bucket must be unknown, got %s", result.State)
	}
}

func TestEvaluateRequestTimingRuleRechecksDirtySettledBucket(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	latest := RequestTimingAlertNow(now)
	bucket := uploadAlertBucket(latest, repeat(9000, 40), 40, 40)
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 1, 2)
	settled := &RequestTimingAlertState{
		RuleID:          rule.ID,
		EvaluatedBucket: latest,
		State:           TimingAlertStateOK,
		SampleCount:     40,
	}
	buckets := map[time.Time]*RequestTimingAlertBucket{latest: bucket}

	// Without a dirty marker the settled verdict is reused as-is: the bucket now
	// breaches, so a re-evaluation would have fired.
	clean := &fakeTimingAlertRepo{buckets: buckets, state: settled}
	result := newTimingAlertEvaluator(clean).evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if result.Fired || result.State != TimingAlertStateOK {
		t.Fatalf("settled minute must be reused while clean, got %+v", result)
	}

	// A late timing patch marks the minute dirty → the stored verdict is stale and must
	// be recomputed instead of being skipped.
	dirty := &fakeTimingAlertRepo{buckets: buckets, state: settled, dirty: map[time.Time]struct{}{latest: {}}}
	result = newTimingAlertEvaluator(dirty).evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if result.Fired {
		t.Fatalf("a dirty minute must not fire from the stale verdict: %+v", result)
	}
	if result.State != TimingAlertStateStale {
		t.Fatalf("dirty minute must re-evaluate to stale, got %s", result.State)
	}
}

// stubTimingAlertOpsRepo serves alert rules to evaluateOnce without pulling in the rest of
// the ops repository surface.
type stubTimingAlertOpsRepo struct {
	OpsRepository
	rules      []*OpsAlertRule
	heartbeats int
}

func (s *stubTimingAlertOpsRepo) ListAlertRules(context.Context) ([]*OpsAlertRule, error) {
	return s.rules, nil
}

func (s *stubTimingAlertOpsRepo) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	return &OpsSystemMetricsSnapshot{}, nil
}

func (s *stubTimingAlertOpsRepo) UpsertJobHeartbeat(context.Context, *OpsUpsertJobHeartbeatInput) error {
	s.heartbeats++
	return nil
}

// request_timing.alert_enabled 必须真正短路新指标族：关闭时连规则事务都不开启，
// 打开时才会走到评估；旧指标规则的行为不受该开关影响（它们不经过这个分支）。
func TestEvaluateOnceGatesRequestTimingRulesByConfig(t *testing.T) {
	rule := &OpsAlertRule{
		ID:               41,
		Name:             "upload p90",
		Enabled:          true,
		Severity:         "P1",
		MetricType:       "upload_ms_p90",
		Operator:         ">",
		Threshold:        8000,
		WindowMinutes:    1,
		SustainedMinutes: 5,
	}

	cases := []struct {
		name    string
		enabled bool
		want    int
	}{
		{name: "disabled by config", enabled: false, want: 0},
		{name: "enabled by config", enabled: true, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubTimingAlertOpsRepo{rules: []*OpsAlertRule{rule}}
			timingRepo := &fakeTimingAlertRepo{}
			evaluator := NewOpsAlertEvaluatorService(nil, repo, nil, nil, &config.Config{
				Ops:           config.OpsConfig{Enabled: true},
				RequestTiming: config.RequestTimingConfig{AlertEnabled: tc.enabled},
			}, nil)
			evaluator.timingAlertRepo = timingRepo

			evaluator.evaluateOnce(time.Minute)

			require.Equal(t, tc.want, timingRepo.lockCalls)
			require.Equal(t, 1, repo.heartbeats)
		})
	}
}

func TestEvaluateRequestTimingRuleRecomputesWhenRuleConfigChanged(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	latest := RequestTimingAlertNow(now)
	bucket := uploadAlertBucket(latest, repeat(9000, 40), 40, 40)
	settledAt := now.Add(-30 * time.Second)
	settled := &RequestTimingAlertState{
		RuleID:          7,
		EvaluatedBucket: latest,
		State:           TimingAlertStateOK,
		SampleCount:     40,
		UpdatedAt:       settledAt,
	}
	buckets := map[time.Time]*RequestTimingAlertBucket{latest: bucket}

	// Unchanged rule → the settled verdict of this minute is reused.
	rule := timingAlertTestRule("upload_ms_p90", ">", 8000, 1, 1)
	rule.UpdatedAt = settledAt.Add(-time.Minute)
	repo := &fakeTimingAlertRepo{buckets: buckets, state: settled}
	result := newTimingAlertEvaluator(repo).evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if result.Fired || result.State != TimingAlertStateOK {
		t.Fatalf("unchanged rule must reuse the settled minute, got %+v", result)
	}

	// The rule was edited after the snapshot was written (threshold/sustained/filters):
	// the old verdict came from the previous configuration and must not be reused.
	rule.UpdatedAt = settledAt.Add(10 * time.Second)
	repo = &fakeTimingAlertRepo{buckets: buckets, state: settled}
	result = newTimingAlertEvaluator(repo).evaluateRequestTimingRule(context.Background(), rule, nil, now)
	if !result.Fired || result.State != TimingAlertStateBreached {
		t.Fatalf("edited rule must re-evaluate the minute, got %+v", result)
	}
	if repo.state == nil || repo.state.State != TimingAlertStateBreached {
		t.Fatalf("re-evaluated state must be persisted, got %+v", repo.state)
	}
}

func TestTimingAlertEvaluationSnapshotDowngradesOverdueVerdict(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	value := 1234.5
	coverage := 0.97
	evaluatedAt := RequestTimingAlertNow(now).Add(-5 * time.Minute)
	state := &RequestTimingAlertState{
		RuleID:          7,
		EvaluatedBucket: evaluatedAt,
		State:           TimingAlertStateOK,
		SampleCount:     40,
		Coverage:        &coverage,
		MetricValue:     &value,
	}

	got := TimingAlertEvaluationSnapshot(state, now)
	if got == nil {
		t.Fatal("snapshot must not be nil")
	}
	if got.State != TimingAlertStateStale {
		t.Fatalf("overdue snapshot must be shown as stale, got %q", got.State)
	}
	if got.Reason != TimingAlertReasonEvaluationOverdue {
		t.Fatalf("stale snapshot must explain why, got %q", got.Reason)
	}
	if got.LastEvaluatedAt == nil || !got.LastEvaluatedAt.Equal(evaluatedAt) {
		t.Fatalf("stale snapshot must keep the original bucket, got %v", got.LastEvaluatedAt)
	}
	if got.MetricValue == nil || *got.MetricValue != value {
		t.Fatalf("stale snapshot must keep the original value, got %v", got.MetricValue)
	}
	if got.SampleCount != 40 || got.Coverage == nil || *got.Coverage != coverage {
		t.Fatalf("stale snapshot must keep samples/coverage, got %d/%v", got.SampleCount, got.Coverage)
	}
}

func TestTimingAlertEvaluationSnapshotKeepsFreshVerdict(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	state := &RequestTimingAlertState{
		EvaluatedBucket: RequestTimingAlertNow(now),
		State:           TimingAlertStateBreached,
		SampleCount:     40,
	}

	got := TimingAlertEvaluationSnapshot(state, now)
	if got == nil || got.State != TimingAlertStateBreached {
		t.Fatalf("fresh snapshot must keep its verdict, got %+v", got)
	}
	if got.Reason != "" {
		t.Fatalf("fresh snapshot must not invent a reason, got %q", got.Reason)
	}

	if TimingAlertEvaluationSnapshot(nil, now) != nil {
		t.Fatal("missing snapshot must stay nil so the UI shows 'not evaluated yet'")
	}
	if TimingAlertEvaluationSnapshot(&RequestTimingAlertState{}, now) != nil {
		t.Fatal("state without verdict must stay nil")
	}
}

func TestTimingAlertEvaluationSnapshotKeepsOriginalReasonWhenOverdue(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 10, 30, 0, time.UTC)
	state := &RequestTimingAlertState{
		EvaluatedBucket: RequestTimingAlertNow(now).Add(-10 * time.Minute),
		State:           TimingAlertStateInsufficient,
		Reason:          "insufficient_coverage",
	}

	got := TimingAlertEvaluationSnapshot(state, now)
	if got == nil || got.State != TimingAlertStateStale {
		t.Fatalf("overdue snapshot must be stale, got %+v", got)
	}
	if got.Reason != "insufficient_coverage" {
		t.Fatalf("overdue snapshot must keep the original reason, got %q", got.Reason)
	}
}
