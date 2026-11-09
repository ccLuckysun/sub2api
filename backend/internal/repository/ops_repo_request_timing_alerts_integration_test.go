//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type timingAlertFixture struct {
	userID    int64
	keyID     int64
	accountID int64
	groupID   int64
}

func insertTimingAlertFixture(t *testing.T, tx *sql.Tx, platform string) timingAlertFixture {
	t.Helper()
	ctx := context.Background()
	f := timingAlertFixture{}

	suffix := time.Now().UnixNano()
	require.NoError(t, tx.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		"timing-alert-"+platform+"-"+time.Now().Format("150405.000000000")+"@example.test",
	).Scan(&f.userID))
	require.NoError(t, tx.QueryRowContext(ctx,
		`INSERT INTO api_keys (user_id, key, name) VALUES ($1, $2, $3) RETURNING id`,
		f.userID, "sk-timing-"+platform+"-"+time.Now().Format("150405.000000000"), "timing",
	).Scan(&f.keyID))
	require.NoError(t, tx.QueryRowContext(ctx,
		`INSERT INTO groups (name, platform) VALUES ($1, $2) RETURNING id`,
		"timing-"+platform+"-"+time.Now().Format("150405.000000000"), platform,
	).Scan(&f.groupID))
	require.NoError(t, tx.QueryRowContext(ctx,
		`INSERT INTO accounts (name, platform, type) VALUES ($1, $2, 'api_key') RETURNING id`,
		"timing-"+platform+"-"+time.Now().Format("150405.000000000"), platform,
	).Scan(&f.accountID))
	_ = suffix
	return f
}

func insertTimingAlertUsageLog(t *testing.T, tx *sql.Tx, f timingAlertFixture, at time.Time, durationMs, firstTokenMs, gatewayLatencyMs int, bodyBytes int64) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), `
		INSERT INTO usage_logs (user_id, api_key_id, account_id, group_id, model, created_at,
		                        stream, request_type, duration_ms, first_token_ms, gateway_latency_ms, body_bytes,
		                        input_tokens, cache_read_tokens, output_tokens, image_count, image_output_tokens, video_count)
		VALUES ($1,$2,$3,$4,'gpt-5',$5,TRUE,2,$6,$7,$8,$9,10,0,20,0,0,0)`,
		f.userID, f.keyID, f.accountID, f.groupID, at.UTC(), durationMs, firstTokenMs, gatewayLatencyMs, bodyBytes)
	require.NoError(t, err)
}

func insertTimingAlertMinuteBucket(t *testing.T, tx *sql.Tx, bucketStart time.Time, platform string, groupID int64, uploads []float64) {
	t.Helper()
	upload := stats.New(stats.Upload)
	for _, v := range uploads {
		require.NoError(t, upload.Add(v))
	}
	encode := func(h *stats.Histogram) []byte {
		data, err := h.Encode()
		require.NoError(t, err)
		return data
	}
	empty := func(metric stats.Metric) []byte { return encode(stats.New(metric)) }
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ops_request_timing_minute (
			bucket_start, platform, group_id,
			candidate_count, complete_count, invalid_count, upload_count, ttft_count, rate_count,
			real_count, known_body_count, large_body_count, total_observed_count, body_observed_count,
			revision, hist_version, hist_schema, upload_hist, rate_hist, body_hist, real_hist)
		VALUES ($1,$2,$3,$4,$4,0,$4,$4,$4,$4,$4,0,$4,$4,1,$5,$6,$7,$8,$9,$10)`,
		bucketStart.UTC(), platform, groupID, len(uploads),
		int(stats.Version), stats.SchemaChecksum(),
		encode(upload), empty(stats.Rate), empty(stats.Body), empty(stats.Real))
	require.NoError(t, err)
}

func newTimingAlertRepo(t *testing.T) *opsRepository {
	t.Helper()
	return &opsRepository{db: integrationDB}
}

// The exact recheck must join the active view aliases (v/g/a). A regression here is a
// hard SQL error, so one query with a platform filter exercises the whole projection.
func TestRequestTimingAlertExactFiltersByPlatform(t *testing.T) {
	tx := testTx(t)
	repo := newTimingAlertRepo(t)
	// The repository reads through the transaction carried in the context, so rows
	// written inside the test transaction stay visible without committing anything.
	ctx := context.WithValue(context.Background(), timingAlertTxKey, tx)
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	openai := insertTimingAlertFixture(t, tx, "openai")
	anthropic := insertTimingAlertFixture(t, tx, "anthropic")
	for i := 0; i < 5; i++ {
		insertTimingAlertUsageLog(t, tx, openai, start.Add(time.Duration(i)*time.Second), 9000, 500, 13000, 4096)
	}
	for i := 0; i < 7; i++ {
		insertTimingAlertUsageLog(t, tx, anthropic, start.Add(time.Duration(i)*time.Second), 3000, 500, 4000, 1024)
	}

	filter := &service.RequestTimingAlertFilter{StartTime: start, EndTime: start.Add(time.Minute)}

	sample, err := repo.GetRequestTimingAlertExact(ctx, filter, "upload_ms_p90", 0.90)
	require.NoError(t, err, "unfiltered recheck must not reference an unknown alias")
	require.EqualValues(t, 12, sample.N)

	filter.Platform = "OpenAI"
	sample, err = repo.GetRequestTimingAlertExact(ctx, filter, "upload_ms_p90", 0.90)
	require.NoError(t, err)
	require.EqualValues(t, 5, sample.N, "platform filter must apply to the matching group only")
	require.NotNil(t, sample.Value)
	require.InDelta(t, 4000, *sample.Value, 1e-9, "upload_ms = gateway_latency_ms - duration_ms")

	filter.Platform = "anthropic"
	sample, err = repo.GetRequestTimingAlertExact(ctx, filter, "upload_ms_p90", 0.90)
	require.NoError(t, err)
	require.EqualValues(t, 7, sample.N)
	require.InDelta(t, 1000, *sample.Value, 1e-9)

	filter.Platform = "unknown"
	sample, err = repo.GetRequestTimingAlertExact(ctx, filter, "upload_ms_p90", 0.90)
	require.NoError(t, err)
	require.EqualValues(t, 0, sample.N, "unrelated platforms must not leak into an unknown platform scope")
}

// A histogram-ambiguous minute in the middle of the window must be reconfirmed from raw
// rows before streaks are counted; when it cannot be confirmed the alert must not fire.
func TestRequestTimingAlertWindowMiddleAmbiguousMinute(t *testing.T) {
	tx := testTx(t)
	repo := newTimingAlertRepo(t)
	ctx := context.WithValue(context.Background(), timingAlertTxKey, tx)

	latest := time.Date(2026, 5, 1, 10, 7, 0, 0, time.UTC)
	middle := latest.Add(-time.Minute)
	f := insertTimingAlertFixture(t, tx, "openai")

	insertTimingAlertMinuteBucket(t, tx, latest, "openai", f.groupID, repeat(9000, 40))
	insertTimingAlertMinuteBucket(t, tx, middle, "openai", f.groupID, repeat(100, 40))
	// Parent summary rows use a negative sentinel group id. They must never be summed
	// with their own leaves, otherwise every sample would be counted twice.
	insertTimingAlertMinuteBucket(t, tx, latest, "openai", -1, repeat(9999, 99))
	// upload_ms = gateway_latency_ms - duration_ms, so raw rows confirm 200ms for this
	// minute while the histogram only knows the straddled 100ms bucket.
	for i := 0; i < 40; i++ {
		insertTimingAlertUsageLog(t, tx, f, middle.Add(time.Duration(i)*time.Second), 100, 20, 300, 2048)
	}
	// 写入 usage_logs 会触发生产环境的 INSERT 脏标记（migration 253），真实链路里聚合器会
	// 先消费掉这些分钟；本用例只验证「纯 resolver 窗口判定」，因此清掉测试自己造成的脏标记，
	// 让窗口走 ambiguous → raw 复核路径。生产脏检查保持不动（另有单测覆盖）。
	clearTimingAlertDirty(t, tx, middle, latest.Add(time.Minute))

	filter := &service.RequestTimingAlertFilter{StartTime: middle, EndTime: latest.Add(time.Minute), Platform: "openai"}
	var rawCount int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM ops_request_timing_minute`).Scan(&rawCount))
	require.Equal(t, 3, rawCount, "fixture rows must exist inside the transaction")
	buckets, err := repo.GetRequestTimingAlertBuckets(ctx, filter)
	if err != nil {
		t.Fatalf("buckets: %v", err)
	}
	require.Len(t, buckets, 2, "buckets=%v count=%d", buckets, rawCount)
	for _, b := range buckets {
		if b.BucketStart.Equal(latest) {
			require.EqualValues(t, 40, b.UploadCount, "sentinel parent rows must be excluded from the leaf sum")
			require.EqualValues(t, 40, b.Upload.N)
		}
	}
	dirty, err := repo.GetRequestTimingDirtyMinutes(ctx, middle, latest.Add(time.Minute))
	require.NoError(t, err)

	indexed := map[time.Time]*service.RequestTimingAlertBucket{}
	for _, b := range buckets {
		indexed[b.BucketStart.UTC()] = b
	}
	// Draw the threshold from the 100ms bucket so that minute is genuinely straddled.
	_, probe := indexed[middle].Upload.Quantile(0.90)
	require.NotNil(t, probe)
	require.NotNil(t, probe.Hi)
	rule := &service.OpsAlertRule{
		ID: 1, Name: "upload", Enabled: true, Severity: "P2",
		MetricType: "upload_ms_p90", Operator: ">", Threshold: probe.Lo + (*probe.Hi-probe.Lo)/2,
		WindowMinutes: 1, SustainedMinutes: 2, CooldownMinutes: 0,
		Filters: map[string]any{"min_samples": 30, "min_coverage": 0.95, "recovery_minutes": 2},
	}

	verdict := service.EvaluateRequestTimingAlertBucket(rule, indexed[middle], false, middle)
	require.True(t, verdict.Ambiguous, "fixture must straddle the threshold: %+v", verdict)

	resolve := func(bucketStart time.Time) (string, *float64, int64, *float64, bool) {
		sample, err := repo.GetRequestTimingAlertExact(ctx, &service.RequestTimingAlertFilter{
			StartTime: bucketStart,
			EndTime:   bucketStart.Add(time.Minute),
			Platform:  "openai",
		}, rule.MetricType, 0.90)
		require.NoError(t, err)
		if sample == nil || sample.Value == nil || sample.N < 30 {
			return "", nil, 0, nil, false
		}
		state := service.TimingAlertStateOK
		if *sample.Value > rule.Threshold {
			state = service.TimingAlertStateBreached
		}
		return state, sample.Value, sample.N, sample.Coverage, true
	}

	decision, _ := service.EvaluateRequestTimingAlertWindowWithResolver(rule, indexed, dirty, latest, middle, resolve)
	require.True(t, decision.ShouldFire, "raw rows confirm the middle minute, so the streak is two: %+v", decision)

	// Drop the raw rows: the middle minute can no longer be confirmed, so the streak
	// must break instead of inheriting the histogram point estimate.
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE group_id = $1`, f.groupID)
	require.NoError(t, err)

	decision, verdicts := service.EvaluateRequestTimingAlertWindowWithResolver(rule, indexed, dirty, latest, middle, resolve)
	require.False(t, decision.ShouldFire, "an unconfirmed middle minute must block firing: %+v", decision)
	require.True(t, decision.Inconclusive, "the window must be marked inconclusive so the bucket is retried")
	require.Equal(t, service.TimingAlertStateUnknown, verdicts[1].State)
}

// clearTimingAlertDirty removes the dirty marks produced by the fixture's own inserts so a
// window test can exercise the resolver path; it also asserts the expected precondition.
func clearTimingAlertDirty(t *testing.T, tx *sql.Tx, from, to time.Time) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM ops_request_timing_dirty WHERE minute_start >= $1 AND minute_start < $2`, from, to); err != nil {
		t.Fatalf("clear fixture dirty: %v", err)
	}
	var remaining int
	if err := tx.QueryRowContext(context.Background(),
		`SELECT count(*) FROM ops_request_timing_dirty WHERE minute_start >= $1 AND minute_start < $2`, from, to).Scan(&remaining); err != nil {
		t.Fatalf("count fixture dirty: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("fixture dirty rows remain: %d", remaining)
	}
}

func repeat(value float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = value
	}
	return out
}
