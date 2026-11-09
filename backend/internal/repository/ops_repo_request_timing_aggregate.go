package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const timingAggregationQuantum = 32

// A quantum is bounded by both work count and the caller deadline. Empty
// historical gaps are advanced in one indexed range probe, never one minute
// per timer tick. Dirty and forward lanes alternate so neither starves.
func (r *opsRepository) AggregateRequestTiming(ctx context.Context, now time.Time) (resultErr error) {
	now = now.UTC()
	if _, err := r.db.ExecContext(ctx, `UPDATE ops_request_timing_state SET last_run_at=$1 WHERE singleton`, now); err != nil {
		return err
	}
	defer func() {
		health, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if resultErr != nil {
			_, err := r.db.ExecContext(health, `UPDATE ops_request_timing_state SET last_error=$1,last_error_at=clock_timestamp() WHERE singleton`, resultErr.Error())
			if err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("cannot persist aggregation failure: %w", err))
			}
		} else {
			_, err := r.db.ExecContext(health, `UPDATE ops_request_timing_state SET last_success_at=clock_timestamp(),
 last_error=CASE WHEN EXISTS(SELECT 1 FROM ops_request_timing_dirty WHERE attempts>0) THEN last_error ELSE NULL END WHERE singleton`)
			if err != nil {
				resultErr = fmt.Errorf("cannot persist aggregation success: %w", err)
			}
		}
	}()
	safeEnd := now.Add(-2 * time.Minute).Truncate(time.Minute)
	floor := safeEnd.Add(-7 * 24 * time.Hour)
	var firstFailure error
	for iteration := 0; iteration < timingAggregationQuantum; iteration++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		preferDirty := iteration%2 == 1
		bucket, found, err := r.nextTimingWork(ctx, now, safeEnd, floor, preferDirty)
		if err != nil {
			return err
		}
		if !found {
			bucket, found, err = r.nextTimingWork(ctx, now, safeEnd, floor, !preferDirty)
			if err != nil {
				return err
			}
			if !found {
				break
			}
		}
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = r.aggregateTimingMinute(work, bucket)
		cancel()
		if errors.Is(err, errTimingAggregationBusy) {
			// Another replica taking the lease must not erase a failure already
			// observed earlier in this quantum.
			return firstFailure
		}
		if err != nil {
			// Persist retry scheduling even when the failed SQL consumed its own
			// timeout. Do not let one poison dirty bucket starve recent arrivals.
			retry, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			_, _ = r.db.ExecContext(retry, `INSERT INTO ops_request_timing_dirty(minute_start,revision,reason,attempts,next_attempt_at)
 VALUES($1,nextval('ops_request_timing_revision_seq'),'retry',1,clock_timestamp()+interval '60 seconds')
 ON CONFLICT(minute_start) DO UPDATE SET attempts=ops_request_timing_dirty.attempts+1,next_attempt_at=EXCLUDED.next_attempt_at`, bucket)
			cancel()
			if firstFailure == nil {
				firstFailure = err
			}
		}
	}
	if firstFailure != nil {
		return firstFailure
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start<$1`, floor); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty WHERE minute_start<$1`, floor); err != nil {
		return err
	}
	if _, err := r.RebuildMissingRequestTimingScopes(ctx, 32); err != nil {
		return err
	}
	return r.timingDriftSweep(ctx, now, safeEnd)
}

var errTimingAggregationBusy = errors.New("request timing aggregation lease busy")

func (r *opsRepository) nextTimingWork(ctx context.Context, now, safeEnd, floor time.Time, dirty bool) (time.Time, bool, error) {
	if dirty {
		var bucket time.Time
		err := r.db.QueryRowContext(ctx, `SELECT minute_start FROM ops_request_timing_dirty
 WHERE minute_start<$1 AND minute_start>=$2 AND next_attempt_at<=clock_timestamp()
 ORDER BY minute_start LIMIT 1`, safeEnd, floor).Scan(&bucket)
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return bucket, err == nil, err
	}
	// Serialize prefix advancement with minute aggregation and retention. A
	// late insert racing this snapshot is covered by its atomic dirty marker.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(741905201)`).Scan(&locked); err != nil {
		return time.Time{}, false, err
	}
	if !locked {
		return time.Time{}, false, nil
	}
	var through, retained sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT finalized_through,source_retained_from FROM ops_request_timing_state WHERE singleton FOR UPDATE`).Scan(&through, &retained); err != nil {
		return time.Time{}, false, err
	}
	cursor := floor
	if through.Valid && through.Time.After(cursor) {
		cursor = through.Time
	}
	if retained.Valid && retained.Time.After(cursor) {
		cursor = retained.Time
	}
	if !cursor.Before(safeEnd) {
		return time.Time{}, false, nil
	}
	var next sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT ul.created_at FROM usage_logs ul WHERE ul.created_at>=$1 AND ul.created_at<$2 AND `+timingCandidate+` ORDER BY ul.created_at LIMIT 1`, cursor, safeEnd).Scan(&next); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, err
	}
	destination := safeEnd
	if next.Valid {
		destination = next.Time.UTC().Truncate(time.Minute)
	}
	// An exhausted forward bucket is also represented as dirty+backoff. Honor
	// it without processing it 16 times in a single invocation.
	if destination.Equal(cursor) {
		var deferred bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops_request_timing_dirty WHERE minute_start=$1 AND next_attempt_at>clock_timestamp())`, cursor).Scan(&deferred); err != nil {
			return time.Time{}, false, err
		}
		if deferred {
			return time.Time{}, false, nil
		}
	}
	if !through.Valid || destination.After(through.Time) {
		if _, err := tx.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=GREATEST(finalized_through,$1),computed_at=clock_timestamp() WHERE singleton`, destination); err != nil {
			return time.Time{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, false, err
	}
	return destination, next.Valid, nil
}

// TimingAggregationPending is used only for scheduling the next bounded
// quantum. It never turns an incomplete cursor into a successful idle state.
func (r *opsRepository) TimingAggregationPending(ctx context.Context, now time.Time) (bool, error) {
	end := now.UTC().Add(-2 * time.Minute).Truncate(time.Minute)
	var pending bool
	err := r.db.QueryRowContext(ctx, `SELECT finalized_through IS NULL OR finalized_through<$1
 OR EXISTS(SELECT 1 FROM ops_request_timing_dirty WHERE minute_start<$1 AND minute_start>=$2 AND next_attempt_at<=clock_timestamp())
 OR EXISTS(SELECT 1 FROM ops_request_timing_minute m WHERE m.group_id>=0 AND m.bucket_start>=$2
 AND (NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=m.bucket_start AND p.group_id=-1 AND p.platform='')
 OR NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=m.bucket_start AND p.group_id=-1 AND p.platform=m.platform)))
 FROM ops_request_timing_state WHERE singleton`, end, end.Add(-7*24*time.Hour)).Scan(&pending)
	return pending, err
}

func (r *opsRepository) aggregateTimingMinute(ctx context.Context, bucket time.Time) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(741905201)`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errTimingAggregationBusy
	}
	// The outer scheduling read is only a hint. Retention can commit between it
	// and this transaction: recheck under the same advisory lock and snapshot.
	// FOR SHARE makes an obsolete RR snapshot fail serialization, not read an
	// obsolete retention boundary after waiting for its updater.
	var retained sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT source_retained_from FROM ops_request_timing_state WHERE singleton FOR SHARE`).Scan(&retained); err != nil {
		return err
	}
	if retained.Valid && bucket.Before(retained.Time) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty WHERE minute_start=$1`, bucket); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=CASE WHEN finalized_through IS NOT NULL AND finalized_through>=$2 THEN GREATEST(finalized_through,$1) ELSE finalized_through END WHERE singleton`, bucket.Add(time.Minute), bucket); err != nil {
			return err
		}
		return tx.Commit()
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM ops_request_timing_dirty WHERE minute_start=$1`, bucket).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// The revision and source share one snapshot. A concurrent mark survives CAS.
	rows, err := tx.QueryContext(ctx, `SELECT `+timingLogColumns+`,`+timingPlatform+` FROM usage_logs ul `+timingJoins+` WHERE ul.created_at >= $1 AND ul.created_at < $2 AND `+timingCandidate, bucket, bucket.Add(time.Minute))
	if err != nil {
		return err
	}
	leaves := make(map[string]*timingLeaf)
	for rows.Next() {
		var platform string
		l := &service.UsageLog{}
		var group, gw, body, duration, first sql.NullInt64
		var upstream, billing sql.NullString
		var requestType int16
		err = rows.Scan(&l.ID, &l.CreatedAt, &l.UserID, &l.APIKeyID, &l.AccountID, &group, &l.Model, &l.RequestID, &upstream, &l.Stream, &requestType, &l.OpenAIWSMode, &gw, &body, &duration, &first, &l.InputTokens, &l.CacheReadTokens, &l.OutputTokens, &l.ImageCount, &l.ImageOutputTokens, &l.VideoCount, &billing, &platform)
		if err != nil {
			_ = rows.Close()
			return err
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
			v := body.Int64
			l.BodyBytes = &v
		}
		if duration.Valid {
			v := int(duration.Int64)
			l.DurationMs = &v
		}
		if first.Valid {
			v := int(first.Int64)
			l.FirstTokenMs = &v
		}
		if billing.Valid {
			v := billing.String
			l.BillingMode = &v
		}
		key := fmt.Sprintf("%s/%d", platform, group.Int64)
		entry := leaves[key]
		if entry == nil {
			entry = &timingLeaf{platform: platform, group: group.Int64, acc: newTimingAccumulator()}
			leaves[key] = entry
		}
		entry.acc.add(l, false)
		if len(leaves) > 10000 {
			_ = rows.Close()
			return fmt.Errorf("timing minute exceeds 10000 active leaves")
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1`, bucket); err != nil {
		return err
	}
	if err := addTimingScopeSummaries(leaves); err != nil {
		return err
	}
	for _, entry := range leaves {
		if err := saveTimingLeaf(ctx, tx, bucket, entry, revision); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty WHERE minute_start=$1 AND revision=$2`, bucket, revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=CASE WHEN finalized_through IS NOT NULL AND finalized_through>=$2 THEN GREATEST(finalized_through,$1) ELSE finalized_through END,computed_at=clock_timestamp() WHERE singleton`, bucket.Add(time.Minute), bucket); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *opsRepository) timingDriftSweep(ctx context.Context, now, safeEnd time.Time) error {
	var checked sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT drift_checked_at FROM ops_request_timing_state WHERE singleton`).Scan(&checked); err != nil {
		return err
	}
	if checked.Valid && now.Sub(checked.Time) < 15*time.Minute {
		return nil
	}
	// Aggregate only a bounded recent horizon; expected markers remain the source
	// of correctness, this catches missed integrations after a rolling upgrade.
	_, err := r.db.ExecContext(ctx, `WITH source AS (
 SELECT date_trunc('minute',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS m,count(*) AS n,count(gateway_latency_ms) AS observed,count(body_bytes) AS bodies
 FROM usage_logs ul WHERE created_at >= $1 AND created_at < $2 AND `+timingCandidate+` GROUP BY 1
), stored AS (
 SELECT bucket_start AS m,sum(candidate_count) AS n,sum(total_observed_count) AS observed,sum(body_observed_count) AS bodies FROM ops_request_timing_minute WHERE bucket_start >= $1 AND bucket_start < $2 AND group_id>=0 GROUP BY 1
), changed AS (
 SELECT s.m FROM source s LEFT JOIN stored t USING(m)
 WHERE s.n IS DISTINCT FROM t.n OR s.observed IS DISTINCT FROM t.observed OR s.bodies IS DISTINCT FROM t.bodies
)
INSERT INTO ops_request_timing_dirty(minute_start,revision,reason)
 SELECT m,nextval('ops_request_timing_revision_seq'),'drift' FROM changed
 ON CONFLICT(minute_start) DO UPDATE SET revision=GREATEST(ops_request_timing_dirty.revision,EXCLUDED.revision),reason='drift',next_attempt_at=now()`, safeEnd.Add(-120*time.Minute), safeEnd)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE ops_request_timing_state SET drift_checked_at=$1 WHERE singleton`, now)
	return err
}
