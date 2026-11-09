package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
)

// Scope rows reuse the exact histogram schema. group=-1 is reserved for a
// materialized scope; platform="" is global, other platforms are per-platform.
// Real leaves always have group>=0 and must never be summed with these rows.
type timingLeaf struct {
	platform string
	group    int64
	acc      *timingAccumulator
}

func addTimingScopeSummaries(leaves map[string]*timingLeaf) error {
	scopes := map[string]*timingLeaf{"": {platform: "", group: -1, acc: newTimingAccumulator()}}
	for _, leaf := range leaves {
		if leaf.group < 0 {
			continue
		}
		if scopes[leaf.platform] == nil {
			scopes[leaf.platform] = &timingLeaf{platform: leaf.platform, group: -1, acc: newTimingAccumulator()}
		}
		targets := []*timingAccumulator{scopes[""].acc}
		// Empty platform is already the global scope. An orphaned/legacy leaf
		// must contribute once, rather than merge into the same accumulator twice.
		if leaf.platform != "" {
			targets = append(targets, scopes[leaf.platform].acc)
		}
		for _, target := range targets {
			target.quality.Add(leaf.acc.quality)
			target.totalObserved += leaf.acc.totalObserved
			target.bodyObserved += leaf.acc.bodyObserved
			for i := range target.hists {
				if err := target.hists[i].Merge(leaf.acc.hists[i]); err != nil {
					return err
				}
			}
		}
	}
	for platform, scope := range scopes {
		if scope.acc.quality.CandidateCount > 0 {
			leaves[fmt.Sprintf("%s/-1", platform)] = scope
		}
	}
	return nil
}

func saveTimingLeaf(ctx context.Context, tx *sql.Tx, bucket time.Time, entry *timingLeaf, revision int64) error {
	a := entry.acc
	var encoded [4][]byte
	for i := range encoded {
		var err error
		encoded[i], err = a.hists[i].Encode()
		if err != nil {
			return err
		}
	}
	q := a.quality
	_, err := tx.ExecContext(ctx, `INSERT INTO ops_request_timing_minute(bucket_start,platform,group_id,hist_version,hist_schema,candidate_count,complete_count,invalid_count,upload_count,ttft_count,real_count,rate_count,known_body_count,large_body_count,upload_hist,rate_hist,body_hist,real_hist,revision,total_observed_count,body_observed_count,computed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,now())`, bucket, entry.platform, entry.group, int(stats.Version), stats.SchemaChecksum(), q.CandidateCount, q.CompleteCount, q.InvalidCount, q.UploadCount, q.TTFTCount, q.RealCount, q.RateCount, q.KnownBodyCount, q.LargeBodyCount, encoded[0], encoded[1], encoded[2], encoded[3], revision, a.totalObserved, a.bodyObserved)
	return err
}

// RebuildMissingRequestTimingScopes folds already materialized leaf blobs. It
// never reads usage_logs and therefore also works after raw TTL expiration.
// A caller-supplied limit and deadline bound upgrade/backfill work.
func (r *opsRepository) RebuildMissingRequestTimingScopes(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 256 {
		limit = 32
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT m.bucket_start FROM ops_request_timing_minute m
 WHERE m.group_id>=0 AND (NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=m.bucket_start AND p.group_id=-1 AND p.platform='')
 OR NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=m.bucket_start AND p.group_id=-1 AND p.platform=m.platform))
 ORDER BY m.bucket_start LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var minutes []time.Time
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			_ = rows.Close()
			return 0, err
		}
		minutes = append(minutes, at)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	completed := 0
	for _, at := range minutes {
		if err := r.rebuildTimingScopeMinute(ctx, at); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func (r *opsRepository) rebuildTimingScopeMinute(ctx context.Context, at time.Time) error {
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
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT platform,group_id,hist_version,hist_schema,candidate_count,complete_count,invalid_count,upload_count,ttft_count,real_count,rate_count,known_body_count,large_body_count,total_observed_count,body_observed_count,upload_hist,rate_hist,body_hist,real_hist,revision
 FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id>=0`, at)
	if err != nil {
		return err
	}
	leaves := make(map[string]*timingLeaf)
	var revision int64
	for rows.Next() {
		entry := &timingLeaf{acc: newTimingAccumulator()}
		q := &entry.acc.quality
		var version int
		var schema string
		var data [4][]byte
		var rev int64
		if err := rows.Scan(&entry.platform, &entry.group, &version, &schema, &q.CandidateCount, &q.CompleteCount, &q.InvalidCount, &q.UploadCount, &q.TTFTCount, &q.RealCount, &q.RateCount, &q.KnownBodyCount, &q.LargeBodyCount, &entry.acc.totalObserved, &entry.acc.bodyObserved, &data[0], &data[1], &data[2], &data[3], &rev); err != nil {
			_ = rows.Close()
			return err
		}
		if version != int(stats.Version) || schema != stats.SchemaChecksum() {
			_ = rows.Close()
			return fmt.Errorf("timing scope schema mismatch")
		}
		for i, metric := range []stats.Metric{stats.Upload, stats.Rate, stats.Body, stats.Real} {
			h, err := stats.Decode(data[i], metric)
			if err != nil {
				_ = rows.Close()
				return err
			}
			entry.acc.hists[i] = h
		}
		q.Finalize()
		leaves[fmt.Sprintf("%s/%d", entry.platform, entry.group)] = entry
		if rev > revision {
			revision = rev
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := addTimingScopeSummaries(leaves); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1`, at); err != nil {
		return err
	}
	for _, leaf := range leaves {
		if leaf.group == -1 {
			if err := saveTimingLeaf(ctx, tx, at, leaf, revision); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
