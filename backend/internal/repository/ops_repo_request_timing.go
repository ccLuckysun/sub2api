package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"golang.org/x/sync/singleflight"
)

var timingRawSlots = make(chan struct{}, 2)
var timingCacheStore sync.Map

type timingCacheEntry struct {
	until time.Time
	value any
}
type timingRepoCache struct {
	mu     sync.Mutex
	values map[string]timingCacheEntry
	flight singleflight.Group
}

func (r *opsRepository) timingCached(ctx context.Context, kind string, f *service.RequestTimingFilter, fn func(context.Context) (any, error)) (any, error) {
	// Inserts/patches advance the revision sequence atomically with their dirty
	// markers. Recomputing a minute advances computed_at. A cached empty answer
	// must not become "finalized no requests" merely because fresh health was
	// attached after an arrival/recompute changed its population.
	var epoch string
	if err := r.db.QueryRowContext(ctx, `SELECT concat(s.finalized_through::text,'/',s.computed_at::text,'/',q.last_value::text) FROM ops_request_timing_state s CROSS JOIN ops_request_timing_revision_seq q WHERE s.singleton`).Scan(&epoch); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(struct {
		Kind   string
		Filter *service.RequestTimingFilter
		Epoch  string
	}{kind, f, epoch})
	key := string(encoded)
	candidate := &timingRepoCache{values: make(map[string]timingCacheEntry)}
	stored, _ := timingCacheStore.LoadOrStore(r, candidate)
	cache := stored.(*timingRepoCache)
	cache.mu.Lock()
	entry, ok := cache.values[key]
	cache.mu.Unlock()
	if ok && time.Now().Before(entry.until) {
		return entry.value, nil
	}
	result := cache.flight.DoChan(key, func() (any, error) {
		// Bounded, detached work may satisfy another waiting authorized reader.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		select {
		case timingRawSlots <- struct{}{}:
			defer func() { <-timingRawSlots }()
		case <-work.Done():
			return nil, timingBudgetError()
		}
		value, err := fn(work)
		if err != nil && work.Err() != nil {
			return nil, timingBudgetError()
		}
		if err == nil {
			cache.mu.Lock()
			if len(cache.values) >= 64 {
				clear(cache.values)
			}
			cache.values[key] = timingCacheEntry{time.Now().Add(30 * time.Second), value}
			cache.mu.Unlock()
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		return result.Val, result.Err
	}
}

func timingBudgetError() error {
	return infraerrors.New(422, "REQUEST_TIMING_BUDGET_EXCEEDED", "Request timing query exceeds its budget; shorten the time range or select fewer entities")
}

const timingLogColumns = `ul.id,ul.created_at,ul.user_id,ul.api_key_id,ul.account_id,ul.group_id,ul.model,ul.request_id,ul.upstream_request_id,
 ul.stream,ul.request_type,ul.openai_ws_mode,ul.gateway_latency_ms,ul.body_bytes,ul.duration_ms,ul.first_token_ms,
 ul.input_tokens,ul.cache_read_tokens,ul.output_tokens,ul.image_count,ul.image_output_tokens,ul.video_count,ul.billing_mode`
const timingJoins = ` LEFT JOIN groups g ON g.id=ul.group_id LEFT JOIN accounts a ON a.id=ul.account_id `
const timingPlatform = `lower(COALESCE(NULLIF(g.platform,''),NULLIF(a.platform,''),'unknown'))`
const timingCandidate = `ul.stream=TRUE AND ul.openai_ws_mode=FALSE AND (ul.request_type=2 OR (ul.request_type=0 AND ul.gateway_latency_ms IS NOT NULL))`

func timingWhere(f *service.RequestTimingFilter) (string, []any) {
	args := []any{f.StartTime, f.EndTime}
	parts := []string{"ul.created_at >= $1", "ul.created_at < $2", timingCandidate}
	add := func(expr string, v any) { args = append(args, v); parts = append(parts, fmt.Sprintf(expr, len(args))) }
	if f.Platform != "" {
		add(timingPlatform+"=$%d", strings.ToLower(f.Platform))
	}
	if f.GroupID != nil {
		if *f.GroupID == 0 {
			add("COALESCE(ul.group_id,0)=$%d", *f.GroupID)
		} else {
			add("ul.group_id=$%d", *f.GroupID)
		}
	}
	if f.AccountID != nil {
		add("ul.account_id=$%d", *f.AccountID)
	}
	if f.Model != "" {
		add("ul.model=$%d", f.Model)
	}
	if f.BodyBytesMin != nil {
		add("ul.body_bytes>=$%d", *f.BodyBytesMin)
	}
	if len(f.Entities) > 0 {
		if f.Dimension == "account" {
			ids := make([]int64, 0, len(f.Entities))
			for _, entity := range f.Entities {
				id, _ := strconv.ParseInt(entity, 10, 64)
				ids = append(ids, id)
			}
			add("ul.account_id=ANY($%d)", pq.Array(ids))
		} else if f.Dimension == "model" {
			add("ul.model=ANY($%d)", pq.Array(f.Entities))
		}
	}
	return strings.Join(parts, " AND "), args
}

type timingScanner interface{ Scan(...any) error }

func scanTimingLog(row timingScanner) (*service.UsageLog, error) {
	l := &service.UsageLog{}
	var group, gw, body, duration, first sql.NullInt64
	var upstream, billing sql.NullString
	var requestType int16
	err := row.Scan(&l.ID, &l.CreatedAt, &l.UserID, &l.APIKeyID, &l.AccountID, &group, &l.Model, &l.RequestID, &upstream,
		&l.Stream, &requestType, &l.OpenAIWSMode, &gw, &body, &duration, &first, &l.InputTokens, &l.CacheReadTokens, &l.OutputTokens, &l.ImageCount, &l.ImageOutputTokens, &l.VideoCount, &billing)
	if err != nil {
		return nil, err
	}
	l.RequestType = service.RequestTypeFromInt16(requestType)
	if group.Valid {
		l.GroupID = &group.Int64
	}
	if gw.Valid {
		v := int(gw.Int64)
		l.GatewayLatencyMs = &v
	}
	if body.Valid {
		l.BodyBytes = &body.Int64
	}
	if duration.Valid {
		v := int(duration.Int64)
		l.DurationMs = &v
	}
	if first.Valid {
		v := int(first.Int64)
		l.FirstTokenMs = &v
	}
	if upstream.Valid {
		l.UpstreamRequestID = &upstream.String
	}
	if billing.Valid {
		l.BillingMode = &billing.String
	}
	return l, nil
}

func (r *opsRepository) timingRaw(ctx context.Context, f *service.RequestTimingFilter) ([]*service.UsageLog, error) {
	if f.EndTime.Sub(f.StartTime) > 24*time.Hour {
		return nil, timingBudgetError()
	}
	var retained sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT source_retained_from FROM ops_request_timing_state WHERE singleton`).Scan(&retained); err != nil {
		return nil, err
	}
	if retained.Valid && f.StartTime.Before(retained.Time) {
		return nil, infraerrors.New(422, "REQUEST_TIMING_SOURCE_EXPIRED", "Raw request timing source has expired; use historical aggregates or shorten the range")
	}
	where, args := timingWhere(f)
	// The extra row is a budget sentinel, never a silently truncated population.
	rows, err := r.db.QueryContext(ctx, "SELECT "+timingLogColumns+" FROM usage_logs ul "+timingJoins+" WHERE "+where+" LIMIT 200001", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logs := make([]*service.UsageLog, 0, 1024)
	for rows.Next() {
		l, err := scanTimingLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
		if len(logs) > service.RequestTimingRowBudget {
			return nil, timingBudgetError()
		}
	}
	return logs, rows.Err()
}

type timingAccumulator struct {
	quality       service.TimingQuality
	totalObserved int64
	bodyObserved  int64
	hists         [4]*stats.Histogram
	values        [4][]float64
}

func newTimingAccumulator() *timingAccumulator {
	return &timingAccumulator{hists: [4]*stats.Histogram{stats.New(stats.Upload), stats.New(stats.Rate), stats.New(stats.Body), stats.New(stats.Real)}}
}
func (a *timingAccumulator) add(log *service.UsageLog, exact bool) {
	t := service.DeriveRequestTiming(log)
	a.quality.CandidateCount++
	if log.GatewayLatencyMs != nil {
		a.totalObserved++
	}
	if log.BodyBytes != nil {
		a.bodyObserved++
	}
	if t.Complete {
		a.quality.CompleteCount++
	}
	if t.Status == "invalid" {
		a.quality.InvalidCount++
	}
	if t.ValidUpload {
		a.quality.UploadCount++
		a.sample(0, float64(*t.UploadMs), exact)
	}
	if t.ValidTTFT {
		a.quality.TTFTCount++
	}
	if t.ValidReal {
		a.quality.RealCount++
		a.sample(3, float64(*t.RealTTFBMs), exact)
	}
	if t.ValidRate {
		a.quality.RateCount++
		a.sample(1, *t.UploadKBPS, exact)
	}
	if t.ValidBody {
		a.quality.KnownBodyCount++
		a.sample(2, float64(*t.BodyBytes), exact)
		if *t.BodyBytes > service.RequestTimingLargeBody {
			a.quality.LargeBodyCount++
		}
	}
	a.quality.Finalize()
}
func (a *timingAccumulator) sample(i int, v float64, exact bool) {
	_ = a.hists[i].Add(v)
	if exact {
		a.values[i] = append(a.values[i], v)
	}
}
func timingQuantiles(h *stats.Histogram, values []float64, exact bool) service.TimingQuantiles {
	q := service.TimingQuantiles{N: int64(h.N)}
	p := []float64{.1, .5, .9, .95}
	dest := []**float64{&q.P10, &q.P50, &q.P90, &q.P95}
	bd := []**stats.Bounds{&q.P10Bounds, &q.P50Bounds, &q.P90Bounds, &q.P95Bounds}
	for i, percentile := range p {
		if exact {
			v := stats.Exact(values, percentile)
			*dest[i] = v
			if v != nil {
				copy := *v
				*bd[i] = &stats.Bounds{Lo: *v, Hi: &copy}
			}
		} else {
			*dest[i], *bd[i] = h.Quantile(percentile)
		}
	}
	return q
}
func (a *timingAccumulator) point(at time.Time, exact bool) service.RequestTimingPoint {
	return service.RequestTimingPoint{BucketStart: at, Quality: a.quality, Upload: timingQuantiles(a.hists[0], a.values[0], exact), Rate: timingQuantiles(a.hists[1], a.values[1], exact), Body: timingQuantiles(a.hists[2], a.values[2], exact), Real: timingQuantiles(a.hists[3], a.values[3], exact)}
}
func timingBucket(at time.Time, seconds int) time.Time {
	return at.UTC().Truncate(time.Duration(seconds) * time.Second)
}

func timingTrendFromRaw(logs []*service.UsageLog, f *service.RequestTimingFilter) *service.RequestTimingTrend {
	points := make(map[time.Time]*timingAccumulator)
	for _, l := range logs {
		at := timingBucket(l.CreatedAt, f.BucketSeconds)
		a := points[at]
		if a == nil {
			a = newTimingAccumulator()
			points[at] = a
		}
		a.add(l, true)
	}
	result := newTimingTrend(f, "raw_exact")
	for at := timingBucket(f.StartTime, f.BucketSeconds); at.Before(f.EndTime); at = at.Add(time.Duration(f.BucketSeconds) * time.Second) {
		a := points[at]
		if a == nil {
			a = newTimingAccumulator()
		}
		p := a.point(at, true)
		result.Points = append(result.Points, p)
		result.Quality.Add(p.Quality)
	}
	now := time.Now().UTC()
	result.ComputedAt = &now
	return result
}
func newTimingTrend(f *service.RequestTimingFilter, mode string) *service.RequestTimingTrend {
	return &service.RequestTimingTrend{Mode: mode, HistVersion: int(stats.Version), BucketSeconds: f.BucketSeconds, StartTime: f.StartTime, EndTime: f.EndTime, Points: []service.RequestTimingPoint{}}
}

func (r *opsRepository) GetRequestTimingTrend(ctx context.Context, f *service.RequestTimingFilter) (*service.RequestTimingTrend, error) {
	copy := *f
	if err := copy.Normalize(time.Now()); err != nil {
		return nil, err
	}
	f = &copy
	filteredRawOnly := f.AccountID != nil || f.Model != "" || f.BodyBytesMin != nil || len(f.Entities) > 0
	if f.Mode == "raw" && f.EndTime.Sub(f.StartTime) > 24*time.Hour {
		return nil, timingBudgetError()
	}
	if filteredRawOnly && f.Mode == "preagg" {
		return nil, timingBudgetError()
	}
	value, err := r.timingCached(ctx, "trend", f, func(work context.Context) (any, error) {
		tryRaw := f.Mode != "preagg" && f.EndTime.Sub(f.StartTime) <= 24*time.Hour
		if tryRaw && f.Mode == "auto" && !filteredRawOnly {
			if estimate, err := r.timingCandidateEstimate(work, f); err == nil && estimate > service.RequestTimingRowBudget {
				tryRaw = false
			}
		}
		if tryRaw {
			rawCtx, rawCancel := context.WithTimeout(work, time.Second)
			logs, rawErr := r.timingRaw(rawCtx, f)
			rawCancel()
			if rawErr == nil {
				result := timingTrendFromRaw(logs, f)
				if err := r.timingMetadata(work, result); err != nil {
					return nil, err
				}
				return result, nil
			}
			if f.Mode == "raw" {
				return nil, rawErr
			}
			if work.Err() != nil {
				return nil, timingBudgetError()
			}
		}
		if filteredRawOnly {
			return nil, timingBudgetError()
		}
		result, err := r.timingHistogramTrend(work, f)
		if err == nil && f.Mode == "auto" {
			v := "raw range or row budget exceeded"
			if result.DegradationReason != nil {
				v += "; " + *result.DegradationReason
			}
			result.DegradationReason = &v
		}
		return result, err
	})
	if err != nil {
		return nil, err
	}
	cached := value.(*service.RequestTimingTrend)
	result := *cached
	result.Points = append([]service.RequestTimingPoint(nil), cached.Points...)
	if err := r.timingMetadata(ctx, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Only fully contained minutes are used, so partial edge minutes do not turn
// a small exact query into a false over-budget decision. Missing/dirty totals
// merely cause the usual bounded raw probe; this is an optimization, not truth.
func (r *opsRepository) timingCandidateEstimate(ctx context.Context, f *service.RequestTimingFilter) (int64, error) {
	start := f.StartTime.Truncate(time.Minute)
	if start.Before(f.StartTime) {
		start = start.Add(time.Minute)
	}
	copy := *f
	copy.StartTime = start
	copy.EndTime = f.EndTime.Truncate(time.Minute)
	where, args := timingHistogramWhere(&copy)
	var n sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT sum(candidate_count) FROM ops_request_timing_minute WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	return n.Int64, err
}

func timingHistogramWhere(f *service.RequestTimingFilter) ([]string, []any) {
	args := []any{f.StartTime, f.EndTime}
	where := []string{"bucket_start >= $1", "bucket_start < $2"}
	if f.GroupID == nil {
		where = append(where, "group_id=-1")
		args = append(args, strings.ToLower(f.Platform))
		where = append(where, fmt.Sprintf("platform=$%d", len(args)))
	} else {
		args = append(args, *f.GroupID)
		where = append(where, fmt.Sprintf("group_id=$%d", len(args)))
		if f.Platform != "" {
			args = append(args, strings.ToLower(f.Platform))
			where = append(where, fmt.Sprintf("platform=$%d", len(args)))
		}
	}
	return where, args
}

func (r *opsRepository) timingMetadata(ctx context.Context, result *service.RequestTimingTrend) error {
	var through, computed, lastRun, lastSuccess, lastErrorAt, progressAt sql.NullTime
	var lastError sql.NullString
	health := &service.TimingAggregationHealth{ServerTime: time.Now().UTC()}
	health.TargetThrough = health.ServerTime.Add(-2 * time.Minute).Truncate(time.Minute)
	if err := r.db.QueryRowContext(ctx, `SELECT finalized_through,computed_at,last_run_at,last_success_at,last_error_at,last_error,watermark_updated_at
 FROM ops_request_timing_state WHERE singleton`).Scan(&through, &computed, &lastRun, &lastSuccess, &lastErrorAt, &lastError, &progressAt); err != nil {
		return fmt.Errorf("timing aggregation health unavailable: %w", err)
	}
	if through.Valid {
		result.FinalizedThrough = &through.Time
		health.FinalizedThrough = &through.Time
		lag := int64(health.TargetThrough.Sub(through.Time).Seconds())
		if lag < 0 {
			lag = 0
		}
		health.LagSeconds = &lag
	}
	if result.Mode == "hist_approx" && computed.Valid {
		result.ComputedAt = &computed.Time
	}
	if lastRun.Valid {
		health.LastRunAt = &lastRun.Time
	}
	if lastSuccess.Valid {
		health.LastSuccessAt = &lastSuccess.Time
	}
	if lastErrorAt.Valid {
		health.LastErrorAt = &lastErrorAt.Time
	}
	if lastError.Valid && lastError.String != "" {
		health.LastError = &lastError.String
	}
	var oldest sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT count(*),min(minute_start) FROM ops_request_timing_dirty WHERE minute_start<$1 AND minute_start>=$2`, health.TargetThrough, health.TargetThrough.Add(-7*24*time.Hour)).Scan(&health.PendingMinutes, &oldest); err != nil {
		return err
	}
	if oldest.Valid {
		health.OldestPendingAt = &oldest.Time
	}
	switch {
	case health.LastError != nil:
		health.Status = "error"
	case !through.Valid:
		health.Status = "not_started"
	case health.LagSeconds != nil && *health.LagSeconds > 120 && progressAt.Valid && health.ServerTime.Sub(progressAt.Time) > 3*time.Minute:
		health.Status = "error"
		message := "Request timing watermark is stalled; scheduler attempts do not indicate aggregation progress"
		health.LastError = &message
	case !lastRun.Valid || health.ServerTime.Sub(lastRun.Time) > 3*time.Minute:
		health.Status = "stale"
	case health.LagSeconds != nil && *health.LagSeconds > 120:
		health.Status = "catching_up"
	case health.PendingMinutes > 0 && oldest.Valid && health.TargetThrough.Sub(oldest.Time) > 2*time.Minute:
		health.Status = "catching_up"
	default:
		health.Status = "healthy"
	}
	result.Aggregation = health
	result.Provisional = !through.Valid || result.EndTime.After(through.Time)
	rows, err := r.db.QueryContext(ctx, `SELECT minute_start FROM ops_request_timing_dirty WHERE minute_start>=$1 AND minute_start<$2`, result.StartTime.Truncate(time.Minute), result.EndTime)
	if err != nil {
		return err
	}
	defer rows.Close()
	dirty := make(map[time.Time]bool)
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			return err
		}
		dirty[timingBucket(at, result.BucketSeconds)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range result.Points {
		point := &result.Points[i]
		point.Pending = dirty[point.BucketStart]
		end := point.BucketStart.Add(time.Duration(result.BucketSeconds) * time.Second)
		point.Finalized = through.Valid && !end.After(through.Time) && !point.Pending
		if point.Pending {
			result.Provisional = true
		}
	}
	return nil
}

func (r *opsRepository) timingHistogramTrend(ctx context.Context, f *service.RequestTimingFilter) (*service.RequestTimingTrend, error) {
	copy := *f
	aligned := false
	if start := copy.StartTime.Truncate(time.Minute); !start.Equal(copy.StartTime) {
		copy.StartTime = start
		aligned = true
	}
	if end := copy.EndTime.Truncate(time.Minute); !end.Equal(copy.EndTime) {
		copy.EndTime = end.Add(time.Minute)
		aligned = true
	}
	if aligned && copy.BucketSeconds == 60 && int(copy.EndTime.Sub(copy.StartTime)/time.Minute) > 720 {
		copy.BucketSeconds = 300
	}
	f = &copy
	if f.GroupID == nil {
		var missing bool
		err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops_request_timing_minute l
 WHERE l.bucket_start >= $1 AND l.bucket_start < $2 AND l.group_id>=0 AND ($3='' OR l.platform=$3)
 AND NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=l.bucket_start AND p.group_id=-1 AND p.platform=$3) LIMIT 1)`, f.StartTime, f.EndTime, strings.ToLower(f.Platform)).Scan(&missing)
		if err != nil {
			return nil, err
		}
		if missing {
			return nil, infraerrors.New(409, "REQUEST_TIMING_AGGREGATION_PENDING", "Historical timing scope summaries are rebuilding; retry after aggregation catches up")
		}
	}
	where, args := timingHistogramWhere(f)
	rows, err := r.db.QueryContext(ctx, `SELECT bucket_start,hist_version,hist_schema,candidate_count,complete_count,invalid_count,upload_count,ttft_count,real_count,rate_count,known_body_count,large_body_count,upload_hist,rate_hist,body_hist,real_hist FROM ops_request_timing_minute WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := make(map[time.Time]*timingAccumulator)
	rowCount := 0
	for rows.Next() {
		rowCount++
		if rowCount > 50000 {
			return nil, timingBudgetError()
		}
		var at time.Time
		var version int
		var schema string
		var q service.TimingQuality
		var data [4][]byte
		if err := rows.Scan(&at, &version, &schema, &q.CandidateCount, &q.CompleteCount, &q.InvalidCount, &q.UploadCount, &q.TTFTCount, &q.RealCount, &q.RateCount, &q.KnownBodyCount, &q.LargeBodyCount, &data[0], &data[1], &data[2], &data[3]); err != nil {
			return nil, err
		}
		if version != int(stats.Version) || schema != stats.SchemaChecksum() {
			return nil, infraerrors.New(409, "REQUEST_TIMING_SCHEMA_MISMATCH", "Timing histogram versions require rebuilding")
		}
		bucket := timingBucket(at, f.BucketSeconds)
		a := acc[bucket]
		if a == nil {
			a = newTimingAccumulator()
			acc[bucket] = a
		}
		a.quality.Add(q)
		for i, metric := range []stats.Metric{stats.Upload, stats.Rate, stats.Body, stats.Real} {
			h, err := stats.Decode(data[i], metric)
			if err != nil {
				return nil, err
			}
			if err = a.hists[i].Merge(h); err != nil {
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := newTimingTrend(f, "hist_approx")
	if aligned {
		v := "historical histogram bounds aligned outward to complete UTC minutes; response times show effective range"
		result.DegradationReason = &v
	}
	if err := r.timingMetadata(ctx, result); err != nil {
		return nil, err
	}
	if result.FinalizedThrough == nil {
		return nil, infraerrors.New(409, "REQUEST_TIMING_AGGREGATION_PENDING", "Timing aggregation is not ready")
	}
	var dirty bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops_request_timing_dirty WHERE minute_start >= $1 AND minute_start < $2)`, f.StartTime, f.EndTime).Scan(&dirty); err != nil {
		return nil, err
	}
	if dirty {
		result.Provisional = true
		v := "late timing updates are pending aggregation"
		result.DegradationReason = &v
	}
	for at := timingBucket(f.StartTime, f.BucketSeconds); at.Before(f.EndTime); at = at.Add(time.Duration(f.BucketSeconds) * time.Second) {
		a := acc[at]
		if a == nil {
			a = newTimingAccumulator()
		}
		p := a.point(at, false)
		result.Points = append(result.Points, p)
		result.Quality.Add(p.Quality)
	}
	return result, nil
}

func (r *opsRepository) GetRequestTimingGroups(ctx context.Context, f *service.RequestTimingFilter) (*service.RequestTimingGroups, error) {
	copy := *f
	if err := copy.Normalize(time.Now()); err != nil {
		return nil, err
	}
	f = &copy
	if f.Dimension == "" {
		f.Dimension = "account"
	}
	value, err := r.timingCached(ctx, "groups", f, func(work context.Context) (any, error) {
		logs, err := r.timingRaw(work, f)
		if err != nil {
			return nil, err
		}
		type entity struct {
			key    string
			count  int64
			points map[time.Time][]float64
		}
		entities := make(map[string]*entity)
		selected := make(map[string]bool)
		for _, e := range f.Entities {
			selected[e] = true
		}
		for _, l := range logs {
			t := service.DeriveRequestTiming(l)
			if !t.ValidUpload {
				continue
			}
			key := l.Model
			if f.Dimension == "account" {
				key = strconv.FormatInt(l.AccountID, 10)
			}
			if len(selected) > 0 && !selected[key] {
				continue
			}
			e := entities[key]
			if e == nil {
				e = &entity{key: key, points: make(map[time.Time][]float64)}
				entities[key] = e
			}
			e.count++
			at := timingBucket(l.CreatedAt, f.BucketSeconds)
			e.points[at] = append(e.points[at], float64(*t.UploadMs))
		}
		ordered := make([]*entity, 0, len(entities))
		for _, e := range entities {
			ordered = append(ordered, e)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].count == ordered[j].count {
				return ordered[i].key < ordered[j].key
			}
			return ordered[i].count > ordered[j].count
		})
		limit := f.Top
		if len(selected) > 0 {
			limit = len(selected)
		}
		if len(ordered) > limit {
			ordered = ordered[:limit]
		}
		result := &service.RequestTimingGroups{Mode: "raw_exact", Dimension: f.Dimension, BucketSeconds: f.BucketSeconds, StartTime: f.StartTime, EndTime: f.EndTime, Series: []service.RequestTimingSeries{}}
		for _, e := range ordered {
			s := service.RequestTimingSeries{Key: e.key, Label: e.key, SampleCount: e.count, Points: []service.RequestTimingGroupPoint{}}
			for at := timingBucket(f.StartTime, f.BucketSeconds); at.Before(f.EndTime); at = at.Add(time.Duration(f.BucketSeconds) * time.Second) {
				values := e.points[at]
				s.Points = append(s.Points, service.RequestTimingGroupPoint{BucketStart: at, N: int64(len(values)), P95: stats.Exact(values, .95)})
			}
			result.Series = append(result.Series, s)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*service.RequestTimingGroups), nil
}

func (r *opsRepository) GetRequestTimingSlow(ctx context.Context, f *service.RequestTimingFilter) (*service.RequestTimingSlow, error) {
	copy := *f
	if err := copy.Normalize(time.Now()); err != nil {
		return nil, err
	}
	f = &copy
	if f.EndTime.Sub(f.StartTime) > 24*time.Hour {
		return nil, timingBudgetError()
	}
	value, err := r.timingCached(ctx, "slow", f, func(work context.Context) (any, error) {
		where, args := timingWhere(f)
		metric, valid := "upload_ms", "is_valid_upload"
		switch f.Sort {
		case "upstream_ttft_ms":
			metric, valid = "upstream_ttft_ms", "is_valid_ttft"
		case "real_ttfb_ms":
			metric, valid = "real_ttfb_ms", "is_valid_real"
		}
		q := `WITH picked AS (SELECT ul.id,ul.created_at,ul.` + metric + ` AS metric FROM v_request_timing ul ` + timingJoins + ` WHERE ` + where + ` AND ul.` + valid + ` ORDER BY ul.` + metric + ` DESC,ul.created_at DESC,ul.id DESC LIMIT 20)
 SELECT ` + timingLogColumns + ` FROM picked p JOIN usage_logs ul ON ul.id=p.id AND ul.created_at=p.created_at ORDER BY p.metric DESC,p.created_at DESC,p.id DESC`
		rows, err := r.db.QueryContext(work, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := &service.RequestTimingSlow{Items: []*service.RequestTimingDetail{}, Sort: f.Sort, StartTime: f.StartTime, EndTime: f.EndTime}
		for rows.Next() {
			l, err := scanTimingLog(rows)
			if err != nil {
				return nil, err
			}
			result.Items = append(result.Items, service.RequestTimingDetailFromUsage(l))
		}
		return result, rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return value.(*service.RequestTimingSlow), nil
}
