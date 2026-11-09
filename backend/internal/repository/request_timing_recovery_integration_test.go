//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These are scheduler regression fixtures in an isolated testcontainers DB,
// never evidence of real client business traffic.
func TestRequestTimingResumeSkipsHoursOfEmptyMinutesAndServesDirtyLane(t *testing.T) {
	ctx := context.Background()
	r := NewOpsRepository(integrationDB).(*opsRepository)
	now := time.Now().UTC().Truncate(time.Minute)
	safe := now.Add(-2 * time.Minute)
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID})
	a := mustCreateAccount(t, client, &service.Account{Name: "timing-recovery", Platform: service.PlatformOpenAI})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, u.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, key.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, a.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
	})
	// A six-hour sleep with two real source minutes and an older late patch.
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=$1,source_retained_from=NULL,last_error=NULL WHERE singleton`, now.Add(-6*time.Hour))
	require.NoError(t, err)
	old := now.Add(-8 * time.Hour)
	recent := now.Add(-5 * time.Minute)
	for i, at := range []time.Time{old, recent} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,stream,request_type,gateway_latency_ms,body_bytes,duration_ms,first_token_ms,created_at)
 VALUES($1,$2,$3,$4,'gpt-4.1',true,2,1000,2048,700,500,$5)`, u.ID, key.ID, a.ID, fmt.Sprintf("recovery-%d-%d", u.ID, i), at.Add(time.Second))
		require.NoError(t, err)
	}
	start := time.Now()
	require.NoError(t, r.AggregateRequestTiming(ctx, now))
	require.Less(t, time.Since(start), 5*time.Second)
	var through time.Time
	var lastRun, lastSuccess sql.NullTime
	var failure sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT finalized_through,last_run_at,last_success_at,last_error FROM ops_request_timing_state WHERE singleton`).Scan(&through, &lastRun, &lastSuccess, &failure))
	require.Equal(t, safe, through.UTC(), "one bounded run must skip the empty sleep gap")
	require.True(t, lastRun.Valid)
	require.True(t, lastSuccess.Valid)
	require.False(t, failure.Valid)
	for _, at := range []time.Time{old, recent} {
		var n int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT candidate_count FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform=''`, at).Scan(&n))
		require.Positive(t, n, "old dirty bucket must not starve behind forward cursor")
	}
	pending, err := r.TimingAggregationPending(ctx, now)
	require.NoError(t, err)
	require.False(t, pending)
	// Empty cursor lag is different from no requests. Health must reveal it even
	// when computed_at is fresh and last_error is NULL.
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=$1,computed_at=now(),last_run_at=now(),last_error=NULL WHERE singleton`, now.Add(-4*time.Hour))
	require.NoError(t, err)
	trend := &service.RequestTimingTrend{StartTime: recent, EndTime: safe, BucketSeconds: 60, Mode: "raw_exact", Points: []service.RequestTimingPoint{{BucketStart: recent}}}
	require.NoError(t, r.timingMetadata(ctx, trend))
	require.Equal(t, "catching_up", trend.Aggregation.Status)
	require.Greater(t, *trend.Aggregation.LagSeconds, int64(3*3600))
	require.False(t, trend.Points[0].Finalized)
	// A heartbeat alone is not progress. Surface a stuck cursor even if polling
	// continues and SQL has never produced an exception.
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET watermark_updated_at=now()-interval '10 minutes' WHERE singleton`)
	require.NoError(t, err)
	require.NoError(t, r.timingMetadata(ctx, trend))
	require.Equal(t, "error", trend.Aggregation.Status)
	require.Contains(t, *trend.Aggregation.LastError, "stalled")
	require.NoError(t, r.AggregateRequestTiming(ctx, now))
	require.NoError(t, r.timingMetadata(ctx, trend))
	require.Equal(t, "healthy", trend.Aggregation.Status)
	require.Zero(t, *trend.Aggregation.LagSeconds)
	require.True(t, trend.Points[0].Finalized)
}

func TestRequestTimingRetryBackoffDoesNotStarveOtherDirtyMinutes(t *testing.T) {
	ctx := context.Background()
	r := NewOpsRepository(integrationDB).(*opsRepository)
	now := time.Now().UTC().Truncate(time.Minute)
	old, newer := now.Add(-2*time.Hour), now.Add(-10*time.Minute)
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO ops_request_timing_dirty(minute_start,revision,attempts,next_attempt_at) VALUES($1,nextval('ops_request_timing_revision_seq'),2,clock_timestamp()+interval '1 hour'),($2,nextval('ops_request_timing_revision_seq'),0,clock_timestamp()-interval '1 second')`, old, newer)
	require.NoError(t, err)
	bucket, found, err := r.nextTimingWork(ctx, now, now.Add(-2*time.Minute), now.Add(-7*24*time.Hour), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, newer, bucket.UTC())
	require.NoError(t, r.aggregateTimingMinute(ctx, bucket))
	var attempts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT attempts FROM ops_request_timing_dirty WHERE minute_start=$1`, old).Scan(&attempts))
	require.Equal(t, 2, attempts)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty WHERE minute_start=$1`, old)
	require.NoError(t, err)
}

func TestRequestTimingDirtyRecomputeCannotJumpForwardWatermark(t *testing.T) {
	ctx := context.Background()
	r := NewOpsRepository(integrationDB).(*opsRepository)
	now := time.Now().UTC().Truncate(time.Minute)
	cursor := now.Add(-4 * time.Hour)
	dirty := now.Add(-10 * time.Minute)
	_, err := integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=$1,source_retained_from=NULL WHERE singleton`, cursor)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO ops_request_timing_dirty(minute_start,revision) VALUES($1,nextval('ops_request_timing_revision_seq')) ON CONFLICT(minute_start) DO UPDATE SET revision=EXCLUDED.revision`, dirty)
	require.NoError(t, err)
	require.NoError(t, r.aggregateTimingMinute(ctx, dirty))
	var actual time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT finalized_through FROM ops_request_timing_state WHERE singleton`).Scan(&actual))
	require.Equal(t, cursor, actual.UTC())
}

func TestRequestTimingFailureIsVisibleEvenAfterDeadlineAndRecoveryClearsIt(t *testing.T) {
	ctx := context.Background()
	r := NewOpsRepository(integrationDB).(*opsRepository)
	now := time.Now().UTC().Truncate(time.Minute)
	_, err := integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=$1,source_retained_from=NULL,last_error=NULL WHERE singleton`, now.Add(-3*time.Hour))
	require.NoError(t, err)
	lock, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = lock.ExecContext(ctx, `LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = r.AggregateRequestTiming(deadline, now)
	cancel()
	require.Error(t, err)
	require.NoError(t, lock.Rollback())
	trend := &service.RequestTimingTrend{StartTime: now.Add(-time.Hour), EndTime: now, Mode: "raw_exact", BucketSeconds: 60}
	require.NoError(t, r.timingMetadata(ctx, trend))
	require.Equal(t, "error", trend.Aggregation.Status)
	require.NotNil(t, trend.Aggregation.LastError)
	require.NotNil(t, trend.Aggregation.LastErrorAt)
	require.NoError(t, r.AggregateRequestTiming(ctx, now))
	require.NoError(t, r.timingMetadata(ctx, trend))
	require.Nil(t, trend.Aggregation.LastError)
}

func TestRequestTimingColdStartRetentionPreservesCandidatesWithMissingFields(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID})
	a := mustCreateAccount(t, client, &service.Account{Name: "timing-retention", Platform: service.PlatformOpenAI})
	now := time.Now().UTC().Truncate(time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, u.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, key.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, a.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=NULL,source_retained_from=NULL WHERE singleton`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,stream,request_type,duration_ms,created_at)
 VALUES($1,$2,$3,$4,'gpt-4.1',true,2,700,$5)`, u.ID, key.ID, a.ID, fmt.Sprintf("retention-%d", u.ID), now.Add(-48*time.Hour))
	require.NoError(t, err)
	// Remove the insert marker to prove that the raw missing-field candidate,
	// independently of dirty bookkeeping, prevents premature TTL deletion.
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty`)
	require.NoError(t, err)
	err = protectRequestTimingRetention(ctx, integrationDB, now.Add(-24*time.Hour))
	require.ErrorContains(t, err, "initial request timing aggregation")
	var retained sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT source_retained_from FROM ops_request_timing_state WHERE singleton`).Scan(&retained))
	require.False(t, retained.Valid)
	// Sources outside the monitoring retention window do not block ordinary
	// long-term usage cleanup on a new installation.
	_, err = integrationDB.ExecContext(ctx, `UPDATE usage_logs SET created_at=$1 WHERE user_id=$2`, now.Add(-10*24*time.Hour), u.ID)
	require.NoError(t, err)
	require.NoError(t, protectRequestTimingRetention(ctx, integrationDB, now.Add(-9*24*time.Hour)))
}
