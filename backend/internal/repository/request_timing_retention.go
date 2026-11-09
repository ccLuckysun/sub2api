package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Protect archived distributions before TTL removes their source. This is only
// wired by production constructors after migrations; legacy SQL adapters retain
// their existing cleanup behavior.
func protectRequestTimingRetention(ctx context.Context, db *sql.DB, cutoff time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(741905201)`); err != nil {
		return err
	}
	minute := cutoff.UTC().Truncate(time.Minute)
	if !minute.Equal(cutoff.UTC()) {
		minute = minute.Add(time.Minute)
	}
	var through sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT finalized_through FROM ops_request_timing_state WHERE singleton FOR UPDATE`).Scan(&through); err != nil {
		return err
	}
	if through.Valid && minute.After(through.Time) {
		return fmt.Errorf("usage retention must wait for request timing aggregation")
	}
	if !through.Valid {
		var observed bool
		// Missing timing fields are still candidates: deleting them before the
		// first aggregation silently loses coverage/field-missing evidence.
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs ul WHERE created_at<$1 AND created_at>=now()-interval '7 days' AND `+timingCandidate+` LIMIT 1)`, minute).Scan(&observed); err != nil {
			return err
		}
		if observed {
			return fmt.Errorf("usage retention must wait for initial request timing aggregation")
		}
	}
	var dirty bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops_request_timing_dirty WHERE minute_start<$1 AND minute_start>=now()-interval '7 days')`, minute).Scan(&dirty); err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("usage retention must wait for pending request timing buckets")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ops_request_timing_state SET source_retained_from=GREATEST(source_retained_from,$1) WHERE singleton`, minute); err != nil {
		return err
	}
	return tx.Commit()
}

func invalidateRequestTimingMinutes(ctx context.Context, tx *sql.Tx, minutes map[time.Time]bool) error {
	for at := range minutes {
		// Explicit deletion discards derived data for this minute as well. If TTL
		// has already removed its source, it remains a gap rather than stale data.
		if _, err := tx.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1`, at); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ops_request_timing_dirty(minute_start,revision,reason) VALUES($1,nextval('ops_request_timing_revision_seq'),'explicit_delete') ON CONFLICT(minute_start) DO UPDATE SET revision=GREATEST(ops_request_timing_dirty.revision,EXCLUDED.revision),reason='explicit_delete',next_attempt_at=now()`, at); err != nil {
			return err
		}
	}
	return nil
}
