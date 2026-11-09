package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
)

// Request-timing alert states. "unknown" and "stale" are deliberately distinct from
// "ok": a bucket we could not observe must never resolve an active firing event.
const (
	TimingAlertStateOK           = "ok"
	TimingAlertStateBreached     = "breached"
	TimingAlertStateInsufficient = "insufficient_data"
	TimingAlertStateStale        = "stale"
	TimingAlertStateQueryError   = "query_error"
	TimingAlertStateUnknown      = "unknown"

	TimingAlertDefaultMinSamples      = 30
	TimingAlertDefaultMinCoverage     = 0.95
	TimingAlertDefaultRecoveryMinutes = 2

	// Aggregation publishes a minute only after it is closed and stable; alerts read
	// buckets that ended at least this long ago.
	TimingAlertAggregationLag = 2 * time.Minute

	// Guards the evaluator against pathological rule configuration.
	TimingAlertMaxSustainedMinutes = 60

	// A snapshot older than this is presented as stale instead of its last verdict.
	// The evaluator normally refreshes every minute, so two buckets of lag means the
	// loop is not running (crashed, disabled, or every query is failing) and the old
	// value must not be shown as a healthy state.
	TimingAlertStaleAfter = 2 * time.Minute

	// Reason attached to a snapshot that the evaluator has not refreshed in time.
	TimingAlertReasonEvaluationOverdue = "evaluation_overdue"
)

// RequestTimingAlertBucket is one aggregated minute of timing observations.
type RequestTimingAlertBucket struct {
	BucketStart time.Time

	CandidateCount int64
	UploadCount    int64
	RateCount      int64
	RealCount      int64
	KnownBodyCount int64
	LargeBodyCount int64

	Upload *stats.Histogram
	Rate   *stats.Histogram
	Body   *stats.Histogram
	Real   *stats.Histogram
}

type RequestTimingAlertFilter struct {
	StartTime time.Time
	EndTime   time.Time
	Platform  string
	GroupID   *int64
}

type RequestTimingAlertState struct {
	RuleID          int64
	EvaluatedBucket time.Time
	State           string
	SampleCount     int64
	Coverage        *float64
	MetricValue     *float64
	Reason          string
	UpdatedAt       time.Time
}

// RequestTimingAlertTx is the transactional handle used while holding the per-rule
// advisory lock, so event creation and resolution cannot interleave for one rule.
type RequestTimingAlertTx interface {
	GetActiveAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error)
	GetLatestAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error)
	CreateAlertEvent(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error)
	UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error
	LoadRequestTimingAlertState(ctx context.Context, ruleID int64) (*RequestTimingAlertState, error)
	SaveRequestTimingAlertState(ctx context.Context, state *RequestTimingAlertState) error
}

// Optional interface (like RequestTimingRepository) so existing OpsRepository mocks
// keep compiling.
type RequestTimingAlertRepository interface {
	GetRequestTimingAlertBuckets(ctx context.Context, filter *RequestTimingAlertFilter) ([]*RequestTimingAlertBucket, error)
	GetRequestTimingDirtyMinutes(ctx context.Context, from, to time.Time) (map[time.Time]struct{}, error)
	GetRequestTimingAlertExact(ctx context.Context, filter *RequestTimingAlertFilter, metric string, percentile float64) (*RequestTimingAlertSample, error)
	WithRequestTimingAlertRuleLock(ctx context.Context, ruleID int64, fn func(ctx context.Context, tx RequestTimingAlertTx) error) error
	// ListRequestTimingAlertStates returns the persisted state of every requested rule
	// in one round trip, so rule listing never fans out per rule.
	ListRequestTimingAlertStates(ctx context.Context, ruleIDs []int64) (map[int64]*RequestTimingAlertState, error)
}

// RequestTimingAlertSample is an exact (raw) recomputation for a single bucket.
type RequestTimingAlertSample struct {
	N          int64
	Candidate  int64
	Value      *float64
	Coverage   *float64
	OverBudget bool
}

type timingAlertMetricSpec struct {
	metric     stats.Metric
	percentile float64
	// samples counts observations that actually carry this metric.
	samples func(b *RequestTimingAlertBucket) int64
	// histogram exposes the distribution, nil for ratio metrics.
	histogram func(b *RequestTimingAlertBucket) *stats.Histogram
}

func timingAlertMetric(metricType string) (timingAlertMetricSpec, bool) {
	switch metricType {
	case "upload_ms_p90":
		return timingAlertMetricSpec{
			metric:     stats.Upload,
			percentile: 0.90,
			samples:    func(b *RequestTimingAlertBucket) int64 { return b.UploadCount },
			histogram:  func(b *RequestTimingAlertBucket) *stats.Histogram { return b.Upload },
		}, true
	case "upload_kbps_p50":
		return timingAlertMetricSpec{
			metric:     stats.Rate,
			percentile: 0.50,
			samples:    func(b *RequestTimingAlertBucket) int64 { return b.RateCount },
			histogram:  func(b *RequestTimingAlertBucket) *stats.Histogram { return b.Rate },
		}, true
	case "real_ttfb_ms_p95":
		return timingAlertMetricSpec{
			metric:     stats.Real,
			percentile: 0.95,
			samples:    func(b *RequestTimingAlertBucket) int64 { return b.RealCount },
			histogram:  func(b *RequestTimingAlertBucket) *stats.Histogram { return b.Real },
		}, true
	case "large_body_ratio":
		return timingAlertMetricSpec{
			metric:     stats.Body,
			percentile: 0,
			samples:    func(b *RequestTimingAlertBucket) int64 { return b.KnownBodyCount },
			histogram:  nil,
		}, true
	default:
		return timingAlertMetricSpec{}, false
	}
}

// RequestTimingAlertSettings carries the per-rule filters with documented defaults.
type RequestTimingAlertSettings struct {
	MinSamples      int64
	MinCoverage     float64
	RecoveryMinutes int
}

func ParseRequestTimingAlertSettings(rule *OpsAlertRule) RequestTimingAlertSettings {
	s := RequestTimingAlertSettings{
		MinSamples:      TimingAlertDefaultMinSamples,
		MinCoverage:     TimingAlertDefaultMinCoverage,
		RecoveryMinutes: TimingAlertDefaultRecoveryMinutes,
	}
	if rule == nil || rule.Filters == nil {
		return s
	}
	if v, ok := numberFilter(rule.Filters["min_samples"]); ok && v >= 1 {
		s.MinSamples = int64(v)
	}
	if v, ok := numberFilter(rule.Filters["min_coverage"]); ok && v > 0 && v <= 1 {
		s.MinCoverage = v
	}
	if v, ok := numberFilter(rule.Filters["recovery_minutes"]); ok && v >= 0 && v <= 60 {
		s.RecoveryMinutes = int(v)
	}
	return s
}

func numberFilter(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	default:
		return 0, false
	}
}

// TimingAlertBucketVerdict is the per-minute classification.
type TimingAlertBucketVerdict struct {
	BucketStart time.Time
	State       string
	Value       *float64
	Samples     int64
	Coverage    *float64
	Reason      string
	Ambiguous   bool
	Bounds      *stats.Bounds
}

// EvaluateRequestTimingAlertBucket classifies one closed minute. Missing or pending
// buckets return "stale", never "ok".
func EvaluateRequestTimingAlertBucket(rule *OpsAlertRule, bucket *RequestTimingAlertBucket, dirty bool, bucketStart time.Time) TimingAlertBucketVerdict {
	spec, ok := timingAlertMetric(rule.MetricType)
	if !ok {
		return TimingAlertBucketVerdict{State: TimingAlertStateUnknown, Reason: "unsupported_metric"}
	}
	if dirty {
		return TimingAlertBucketVerdict{State: TimingAlertStateStale, Reason: "bucket_pending_aggregation"}
	}
	if bucket == nil {
		return TimingAlertBucketVerdict{State: TimingAlertStateStale, Reason: "bucket_missing"}
	}
	if !bucket.BucketStart.Equal(bucketStart) {
		return TimingAlertBucketVerdict{State: TimingAlertStateStale, Reason: "bucket_mismatch"}
	}
	settings := ParseRequestTimingAlertSettings(rule)
	coverage := bucketCoverage(spec, bucket)
	samples := spec.samples(bucket)
	verdict := TimingAlertBucketVerdict{BucketStart: bucketStart.UTC(), Samples: samples, Coverage: coverage}
	if samples < settings.MinSamples {
		verdict.State = TimingAlertStateInsufficient
		verdict.Reason = "insufficient_samples"
		return verdict
	}
	if coverage != nil && *coverage < settings.MinCoverage {
		verdict.State = TimingAlertStateInsufficient
		verdict.Reason = "insufficient_coverage"
		return verdict
	}

	if rule.MetricType == "large_body_ratio" {
		if bucket.KnownBodyCount <= 0 {
			verdict.State = TimingAlertStateInsufficient
			verdict.Reason = "insufficient_samples"
			return verdict
		}
		value := float64(bucket.LargeBodyCount) / float64(bucket.KnownBodyCount)
		verdict.Value = &value
		verdict.State = breachState(value, rule)
		return verdict
	}

	hist := spec.histogram(bucket)
	if hist == nil || hist.N == 0 {
		verdict.State = TimingAlertStateInsufficient
		verdict.Reason = "insufficient_samples"
		return verdict
	}
	if samples != int64(hist.N) {
		// The histogram and its sample counter disagree: the encoded distribution is not
		// trustworthy, so the bucket is unknown until aggregation rewrites it.
		verdict.State = TimingAlertStateUnknown
		verdict.Reason = "histogram_sample_mismatch"
		verdict.Bounds = nil
		verdict.Value = nil
		return verdict
	}
	value, bounds := hist.Quantile(spec.percentile)
	verdict.Bounds = bounds
	verdict.Value = value
	if bounds != nil && boundsCrossesThreshold(bounds, rule.Operator, rule.Threshold) {
		// The histogram bucket straddles the threshold: the true percentile could sit on
		// either side, so the caller must confirm against raw rows.
		// The point estimate is informational only: an ambiguous minute is unknown until
		// raw rows confirm it, otherwise a straddled bucket could silently extend a
		// breach or recovery streak.
		verdict.Ambiguous = true
		verdict.Reason = "histogram_boundary"
		verdict.State = TimingAlertStateUnknown
		return verdict
	}
	if value == nil {
		verdict.State = TimingAlertStateUnknown
		verdict.Reason = "unbounded_histogram"
		return verdict
	}
	verdict.State = breachState(*value, rule)
	return verdict
}

// TimingAlertExactResolver confirms one ambiguous minute against raw rows. ok=false
// means the minute could not be decided (over budget, no samples, low coverage).
type TimingAlertExactResolver func(bucketStart time.Time) (state string, value *float64, samples int64, coverage *float64, ok bool)

func bucketCoverage(spec timingAlertMetricSpec, bucket *RequestTimingAlertBucket) *float64 {
	if bucket.CandidateCount <= 0 {
		return nil
	}
	v := float64(spec.samples(bucket)) / float64(bucket.CandidateCount)
	return &v
}

func breachState(value float64, rule *OpsAlertRule) string {
	if compareMetric(value, rule.Operator, rule.Threshold) {
		return TimingAlertStateBreached
	}
	return TimingAlertStateOK
}

// boundsCrossesThreshold reports whether the bucket interval leaves the comparison
// undecided. Buckets are read as closed intervals [lo, hi]: a bucket is only
// classified when every value inside it falls on the same side of the threshold.
// A degenerate interval (lo == hi) is an exact observation and needs no recheck.
func boundsCrossesThreshold(bounds *stats.Bounds, operator string, threshold float64) bool {
	if bounds == nil {
		return true
	}
	if bounds.Hi == nil || bounds.Overflow {
		return true
	}
	lo, hi := bounds.Lo, *bounds.Hi
	if lo == hi {
		return false
	}
	allAbove := lo > threshold      // every value is strictly above the threshold
	allAtOrAbove := lo >= threshold // every value satisfies ">= threshold"
	allBelow := hi < threshold      // every value is strictly below the threshold
	allAtOrBelow := hi <= threshold // every value satisfies "<= threshold"
	switch operator {
	case ">":
		return !(allAbove || allAtOrBelow)
	case ">=":
		return !(allAtOrAbove || allBelow)
	case "<":
		return !(allBelow || allAtOrAbove)
	case "<=":
		return !(allAtOrBelow || allAbove)
	case "==", "!=":
		// Equality cannot be decided while the threshold sits inside the bucket.
		return !(allBelow || allAbove)
	}
	return true
}

// TimingAlertDecision is what the evaluator needs to persist.
type TimingAlertDecision struct {
	State        string
	Reason       string
	MetricValue  *float64
	Samples      int64
	Coverage     *float64
	Breached     bool
	BreachStreak int
	HealthStreak int
	// Inconclusive records that some ambiguous minute in the window could not be
	// confirmed against raw rows. Such a window may not settle on its bucket, so the
	// next evaluation retries instead of freezing an unproven streak.
	Inconclusive  bool
	ShouldFire    bool
	ShouldResolve bool
	ExactMetric   string
	ExactPercent  float64
	LatestBucket  time.Time
	Earliest      time.Time
}

// EvaluateRequestTimingAlertWindow classifies the trailing bucket window. Streaks are
// counted in buckets (minutes), not evaluator polls: a slow poll cadence cannot
// shorten or lengthen the configured sustained/recovery requirement.
func EvaluateRequestTimingAlertWindow(rule *OpsAlertRule, buckets map[time.Time]*RequestTimingAlertBucket, dirty map[time.Time]struct{}, latest, earliest time.Time) (*TimingAlertDecision, []TimingAlertBucketVerdict) {
	return EvaluateRequestTimingAlertWindowWithResolver(rule, buckets, dirty, latest, earliest, nil)
}

// EvaluateRequestTimingAlertWindowWithResolver evaluates the trailing window and
// resolves every histogram-ambiguous minute through resolve before streaks are
// counted. A minute that cannot be resolved breaks the streak.
func EvaluateRequestTimingAlertWindowWithResolver(rule *OpsAlertRule, buckets map[time.Time]*RequestTimingAlertBucket, dirty map[time.Time]struct{}, latest, earliest time.Time, resolve TimingAlertExactResolver) (*TimingAlertDecision, []TimingAlertBucketVerdict) {
	settings := ParseRequestTimingAlertSettings(rule)
	required := rule.SustainedMinutes
	if required < 1 {
		required = 1
	}
	if required > TimingAlertMaxSustainedMinutes {
		required = TimingAlertMaxSustainedMinutes
	}
	// recovery_minutes=0 means "resolve manually": the alert then never auto-resolves.
	recovery := settings.RecoveryMinutes
	if recovery < 0 {
		recovery = 0
	}

	decision := &TimingAlertDecision{LatestBucket: latest, Earliest: earliest}
	verdicts := make([]TimingAlertBucketVerdict, 0, required+recovery)
	var firstVer *TimingAlertBucketVerdict

streakWalk:
	for at := latest; !at.Before(earliest); at = at.Add(-time.Minute) {
		_, isDirty := dirty[at]
		verdict := EvaluateRequestTimingAlertBucket(rule, buckets[at], isDirty, at)
		if verdict.Ambiguous {
			var confirmed bool
			verdict, confirmed = resolveAmbiguousVerdict(rule, verdict, resolve)
			if !confirmed {
				decision.Inconclusive = true
			}
		}
		verdicts = append(verdicts, verdict)
		if firstVer == nil {
			copied := verdict
			firstVer = &copied
			decision.State = verdict.State
			decision.Reason = verdict.Reason
			decision.MetricValue = verdict.Value
			decision.Samples = verdict.Samples
			decision.Coverage = verdict.Coverage
			if verdict.Ambiguous && rule.MetricType != "large_body_ratio" {
				spec, _ := timingAlertMetric(rule.MetricType)
				decision.ExactMetric = rule.MetricType
				decision.ExactPercent = spec.percentile
			}
		}
		// Streaks are anchored on the newest bucket and only extend while the
		// classification stays identical. The opposite state (or any unknown/stale/
		// insufficient bucket) breaks the streak immediately, so breached/healthy/
		// breached never counts as a two-bucket breach streak.
		if verdict.State != firstVer.State {
			break
		}
		switch verdict.State {
		case TimingAlertStateBreached:
			decision.BreachStreak++
		case TimingAlertStateOK:
			decision.HealthStreak++
		default:
			break streakWalk
		}
	}

	if firstVer == nil {
		return decision, verdicts
	}
	switch firstVer.State {
	case TimingAlertStateBreached:
		decision.Breached = true
		decision.ShouldFire = decision.BreachStreak >= required
	case TimingAlertStateOK:
		decision.ShouldResolve = recovery > 0 && decision.HealthStreak >= recovery
	}
	return decision, verdicts
}

func resolveAmbiguousVerdict(rule *OpsAlertRule, verdict TimingAlertBucketVerdict, resolve TimingAlertExactResolver) (TimingAlertBucketVerdict, bool) {
	if resolve == nil {
		verdict.State = TimingAlertStateUnknown
		verdict.Reason = "histogram_boundary_unconfirmed"
		return verdict, false
	}
	state, value, samples, coverage, ok := resolve(verdict.BucketStart)
	if !ok || state == "" {
		verdict.State = TimingAlertStateUnknown
		verdict.Reason = "histogram_boundary_unconfirmed"
		verdict.Value = nil
		return verdict, false
	}
	verdict.State = state
	verdict.Reason = "histogram_boundary_confirmed_by_raw"
	if value != nil {
		verdict.Value = value
	}
	if samples > 0 {
		verdict.Samples = samples
	}
	if coverage != nil {
		verdict.Coverage = coverage
	}
	return verdict, true
}

// timingAlertRepository returns the optional timing alert repository, preferring the
// test/override field over the shared ops repository assertion.
func (s *OpsAlertEvaluatorService) timingAlertRepository() (RequestTimingAlertRepository, bool) {
	if s == nil {
		return nil, false
	}
	if s.timingAlertRepo != nil {
		return s.timingAlertRepo, true
	}
	repo, ok := s.opsRepo.(RequestTimingAlertRepository)
	return repo, ok
}

// RequestTimingAlertEvaluations returns the persisted evaluation snapshots for the
// given rules, keyed by rule id. Rules without a snapshot are absent from the map.
func (s *OpsService) RequestTimingAlertEvaluations(ctx context.Context, ruleIDs []int64) (map[int64]*RequestTimingAlertState, error) {
	if s == nil || len(ruleIDs) == 0 {
		return nil, nil
	}
	repo, ok := s.opsRepo.(RequestTimingAlertRepository)
	if !ok {
		return nil, nil
	}
	return repo.ListRequestTimingAlertStates(ctx, ruleIDs)
}

// DefaultRequestTimingSustainedMinutes is the contract default streak length for each
// request-timing metric, used when a rule does not set sustained_minutes explicitly.
func DefaultRequestTimingSustainedMinutes(metricType string) int {
	switch metricType {
	case "upload_kbps_p50":
		return 10
	case "upload_ms_p90", "large_body_ratio", "real_ttfb_ms_p95":
		return 5
	}
	return 5
}

func timingAlertPercentile(metricType string) float64 {
	if spec, ok := timingAlertMetric(metricType); ok {
		return spec.percentile
	}
	return 0
}

// RequestTimingAlertNow returns the start of the newest bucket an alert may read.
//
// The aggregator publishes with an exclusive safeEnd of "floor(now) - 2m", i.e. only
// minutes strictly before it are closed. The newest readable bucket therefore starts
// at floor(now) - 3m; using the watermark itself as a bucket start would always look
// like a missing (stale) bucket.
func RequestTimingAlertNow(now time.Time) time.Time {
	return now.UTC().Truncate(time.Minute).Add(-TimingAlertAggregationLag).Add(-time.Minute)
}

// RequestTimingAlertWindowStart returns the oldest bucket needed for the streak walk.
func RequestTimingAlertWindowStart(rule *OpsAlertRule, latest time.Time) time.Time {
	settings := ParseRequestTimingAlertSettings(rule)
	required := rule.SustainedMinutes
	if required < 1 {
		required = 1
	}
	if required > TimingAlertMaxSustainedMinutes {
		required = TimingAlertMaxSustainedMinutes
	}
	recovery := settings.RecoveryMinutes
	if recovery < 1 {
		recovery = 1
	}
	window := required
	if recovery > window {
		window = recovery
	}
	return latest.Add(-time.Duration(window-1) * time.Minute)
}

type timingAlertRuleResult struct {
	Fired    bool
	Resolved bool
	Emailed  bool
	State    string
}

// evaluateRequestTimingRule evaluates one request-timing rule against closed buckets.
func (s *OpsAlertEvaluatorService) evaluateRequestTimingRule(ctx context.Context, rule *OpsAlertRule, runtimeCfg *OpsAlertRuntimeSettings, now time.Time) timingAlertRuleResult {
	result := timingAlertRuleResult{}
	repo, ok := s.timingAlertRepository()
	if !ok {
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] request timing alert repository unavailable (rule=%d)", rule.ID)
		result.State = TimingAlertStateQueryError
		return result
	}

	latest := RequestTimingAlertNow(now)
	if latest.IsZero() {
		result.State = TimingAlertStateStale
		return result
	}
	platform, groupID, region := parseOpsAlertRuleScope(rule.Filters)
	filter := &RequestTimingAlertFilter{StartTime: RequestTimingAlertWindowStart(rule, latest), EndTime: latest.Add(time.Minute), Platform: platform, GroupID: groupID}

	err := repo.WithRequestTimingAlertRuleLock(ctx, rule.ID, func(ctx context.Context, tx RequestTimingAlertTx) error {
		state, err := tx.LoadRequestTimingAlertState(ctx, rule.ID)
		if err != nil {
			result.State = TimingAlertStateQueryError
			return err
		}
		if state != nil && state.EvaluatedBucket.Equal(latest) && timingAlertStateIsSettled(state.State) && !timingAlertRuleChangedSince(rule, state) {
			// A settled minute can still be rewritten by late timing patches. If the
			// aggregator has marked it dirty, the previous verdict is no longer
			// trustworthy, so fall through and re-evaluate instead of skipping.
			pending, err := repo.GetRequestTimingDirtyMinutes(ctx, latest, latest.Add(time.Minute))
			if err != nil {
				result.State = TimingAlertStateQueryError
				return err
			}
			if _, dirty := pending[latest.UTC()]; !dirty {
				result.State = state.State
				return nil
			}
		}

		buckets, err := repo.GetRequestTimingAlertBuckets(ctx, filter)
		if err != nil {
			result.State = TimingAlertStateQueryError
			return err
		}
		dirty, err := repo.GetRequestTimingDirtyMinutes(ctx, filter.StartTime, filter.EndTime)
		if err != nil {
			result.State = TimingAlertStateQueryError
			return err
		}
		indexed := make(map[time.Time]*RequestTimingAlertBucket, len(buckets))
		for _, b := range buckets {
			if b == nil {
				continue
			}
			indexed[b.BucketStart.UTC()] = b
		}

		settings := ParseRequestTimingAlertSettings(rule)
		resolve := func(bucketStart time.Time) (string, *float64, int64, *float64, bool) {
			sample, err := repo.GetRequestTimingAlertExact(ctx, &RequestTimingAlertFilter{
				StartTime: bucketStart,
				EndTime:   bucketStart.Add(time.Minute),
				Platform:  platform,
				GroupID:   groupID,
			}, rule.MetricType, timingAlertPercentile(rule.MetricType))
			if err != nil || sample == nil || sample.OverBudget || sample.Value == nil || sample.N == 0 {
				return "", nil, 0, nil, false
			}
			if sample.N < settings.MinSamples {
				return "", nil, 0, nil, false
			}
			if sample.Coverage != nil && *sample.Coverage < settings.MinCoverage {
				return "", nil, 0, nil, false
			}
			return breachState(*sample.Value, rule), sample.Value, sample.N, sample.Coverage, true
		}
		decision, _ := EvaluateRequestTimingAlertWindowWithResolver(rule, indexed, dirty, latest, filter.StartTime, resolve)

		persistedState := decision.State
		if decision.Inconclusive {
			persistedState = TimingAlertStateUnknown
		}
		result.State = persistedState

		activeEvent, err := tx.GetActiveAlertEvent(ctx, rule.ID)
		if err != nil {
			return err
		}

		if decision.ShouldFire {
			if activeEvent == nil {
				silenced := false
				if s.opsService != nil && strings.TrimSpace(platform) != "" {
					if ok, err := s.opsService.IsAlertSilenced(ctx, rule.ID, platform, groupID, region, now); err == nil && ok {
						silenced = true
					}
				}
				if !silenced {
					latestEvent, err := tx.GetLatestAlertEvent(ctx, rule.ID)
					if err != nil {
						return err
					}
					cooling := false
					if latestEvent != nil && rule.CooldownMinutes > 0 && now.Sub(latestEvent.FiredAt) < time.Duration(rule.CooldownMinutes)*time.Minute {
						cooling = true
					}
					if !cooling {
						event := &OpsAlertEvent{
							RuleID:         rule.ID,
							Severity:       strings.TrimSpace(rule.Severity),
							Status:         OpsAlertStatusFiring,
							Title:          fmt.Sprintf("%s: %s", strings.TrimSpace(rule.Severity), strings.TrimSpace(rule.Name)),
							Description:    buildRequestTimingAlertDescription(rule, decision, buckets),
							MetricValue:    decision.MetricValue,
							ThresholdValue: float64Ptr(rule.Threshold),
							Dimensions:     buildOpsAlertDimensions(platform, groupID),
							FiredAt:        now,
							CreatedAt:      now,
						}
						created, err := tx.CreateAlertEvent(ctx, event)
						if err != nil {
							return err
						}
						result.Fired = true
						if created != nil && created.ID > 0 {
							result.Emailed = s.maybeSendAlertEmail(ctx, runtimeCfg, rule, created)
						}
					}
				}
			}
		}

		if decision.ShouldResolve && activeEvent != nil {
			resolvedAt := now
			if err := tx.UpdateAlertEventStatus(ctx, activeEvent.ID, OpsAlertStatusResolved, &resolvedAt); err != nil {
				return err
			}
			result.Resolved = true
		}

		return tx.SaveRequestTimingAlertState(ctx, &RequestTimingAlertState{
			RuleID:          rule.ID,
			EvaluatedBucket: latest,
			State:           persistedState,
			SampleCount:     decision.Samples,
			Coverage:        decision.Coverage,
			MetricValue:     decision.MetricValue,
			Reason:          decision.Reason,
			UpdatedAt:       now,
		})
	})
	if err != nil {
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] request timing rule failed (rule=%d): %v", rule.ID, err)
		if result.State == "" {
			result.State = TimingAlertStateQueryError
		}
	}
	return result
}

// timingAlertRuleChangedSince reports whether the rule was edited after the stored
// snapshot was written. A settled minute must be recomputed in that case: a changed
// threshold, operator, sustained window or filter would otherwise keep reusing a
// verdict produced by the previous configuration for the rest of the minute.
func timingAlertRuleChangedSince(rule *OpsAlertRule, state *RequestTimingAlertState) bool {
	if rule == nil || state == nil {
		return false
	}
	if rule.UpdatedAt.IsZero() || state.UpdatedAt.IsZero() {
		return false
	}
	return state.UpdatedAt.Before(rule.UpdatedAt)
}

// TimingAlertEvaluationSnapshot turns a persisted state row into the API DTO shown on
// the rules page. A snapshot the evaluator has not refreshed within TimingAlertStaleAfter
// is downgraded to stale (keeping its original bucket, value, samples, coverage and
// reason) so an old green verdict cannot outlive a broken evaluator loop.
func TimingAlertEvaluationSnapshot(state *RequestTimingAlertState, now time.Time) *OpsAlertRuleEvaluation {
	if state == nil || strings.TrimSpace(state.State) == "" {
		return nil
	}
	evaluatedAt := state.EvaluatedBucket
	snapshot := &OpsAlertRuleEvaluation{
		State:           state.State,
		Reason:          state.Reason,
		LastEvaluatedAt: &evaluatedAt,
		MetricValue:     state.MetricValue,
		SampleCount:     state.SampleCount,
		Coverage:        state.Coverage,
	}
	if !timingAlertSnapshotIsOverdue(state, now) {
		return snapshot
	}
	snapshot.State = TimingAlertStateStale
	if strings.TrimSpace(snapshot.Reason) == "" {
		snapshot.Reason = TimingAlertReasonEvaluationOverdue
	}
	return snapshot
}

// timingAlertSnapshotIsOverdue compares the stored bucket with the newest bucket the
// evaluator could have evaluated by now.
func timingAlertSnapshotIsOverdue(state *RequestTimingAlertState, now time.Time) bool {
	if state == nil || state.EvaluatedBucket.IsZero() {
		return true
	}
	expected := RequestTimingAlertNow(now)
	return expected.Sub(state.EvaluatedBucket.UTC()) > TimingAlertStaleAfter
}

func timingAlertStateIsSettled(state string) bool {
	return state == TimingAlertStateOK || state == TimingAlertStateBreached
}

func buildRequestTimingAlertDescription(rule *OpsAlertRule, decision *TimingAlertDecision, buckets []*RequestTimingAlertBucket) string {
	if rule == nil || decision == nil {
		return ""
	}
	sample := ""
	if decision.MetricValue != nil {
		sample = fmt.Sprintf("value=%.4g threshold=%.4g", *decision.MetricValue, rule.Threshold)
	}
	coverage := ""
	if decision.Coverage != nil {
		coverage = fmt.Sprintf(" coverage=%.1f%%", *decision.Coverage*100)
	}
	reason := ""
	if decision.Reason != "" {
		reason = " reason=" + decision.Reason
	}
	return truncateString(fmt.Sprintf("%s%s%s n=%d streak=%d/%d at %s", sample, coverage, reason, decision.Samples, decision.BreachStreak, max(rule.SustainedMinutes, 1), decision.LatestBucket.Format(time.RFC3339)), 1024)
}
