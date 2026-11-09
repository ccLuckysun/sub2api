package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Request-timing alert repository: exclusive to the timing alert evaluator.
//
// It is exposed through the optional service.RequestTimingAlertRepository interface so
// existing OpsRepository mocks keep compiling untouched.

type timingAlertTxKeyType struct{}

var timingAlertTxKey timingAlertTxKeyType

func timingAlertConn(ctx context.Context, db *sql.DB) any {
	if tx, ok := ctx.Value(timingAlertTxKey).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return db
}

type timingAlertQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (r *opsRepository) timingAlertConn(ctx context.Context) timingAlertQueryer {
	if tx, ok := ctx.Value(timingAlertTxKey).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return r.db
}

// GetRequestTimingAlertBuckets aggregates every leaf row (platform × group) of the
// requested scope into one series of UTC minutes.
func (r *opsRepository) GetRequestTimingAlertBuckets(ctx context.Context, filter *service.RequestTimingAlertFilter) ([]*service.RequestTimingAlertBucket, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil timing alert filter")
	}
	args := []any{filter.StartTime.UTC(), filter.EndTime.UTC()}
	// Leaf rows only: the aggregator also stores low-cardinality parent summaries in
	// this table with a negative sentinel group id. Counting a parent row next to its
	// own children would duplicate every sample in the alert histogram.
	where := []string{"bucket_start >= $1", "bucket_start < $2", "group_id >= " + timingAlertLeafGroupFloor}
	if filter.Platform != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Platform)))
		where = append(where, fmt.Sprintf("platform = $%d", len(args)))
	}
	if filter.GroupID != nil {
		args = append(args, *filter.GroupID)
		where = append(where, fmt.Sprintf("group_id = $%d", len(args)))
	}

	q := `SELECT bucket_start, candidate_count, upload_count, rate_count, real_count,
	       known_body_count, large_body_count, hist_version, hist_schema,
	       upload_hist, rate_hist, body_hist, real_hist
	  FROM ops_request_timing_minute
	 WHERE ` + strings.Join(where, " AND ") + `
	 ORDER BY bucket_start`
	rows, err := r.timingAlertConn(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byBucket := map[time.Time]*service.RequestTimingAlertBucket{}
	order := make([]time.Time, 0, 64)
	for rows.Next() {
		var at time.Time
		var candidate, uploads, rates, reals, known, large int64
		var version int
		var schema string
		var blobs [4][]byte
		if err := rows.Scan(&at, &candidate, &uploads, &rates, &reals, &known, &large, &version, &schema, &blobs[0], &blobs[1], &blobs[2], &blobs[3]); err != nil {
			return nil, err
		}
		if version != int(stats.Version) || schema != stats.SchemaChecksum() {
			return nil, fmt.Errorf("timing histogram schema mismatch: version=%d schema=%s", version, schema)
		}
		key := at.UTC()
		bucket := byBucket[key]
		if bucket == nil {
			bucket = &service.RequestTimingAlertBucket{
				BucketStart: key,
				Upload:      stats.New(stats.Upload),
				Rate:        stats.New(stats.Rate),
				Body:        stats.New(stats.Body),
				Real:        stats.New(stats.Real),
			}
			byBucket[key] = bucket
			order = append(order, key)
		}
		bucket.CandidateCount += candidate
		bucket.UploadCount += uploads
		bucket.RateCount += rates
		bucket.RealCount += reals
		bucket.KnownBodyCount += known
		bucket.LargeBodyCount += large
		for i, metric := range []stats.Metric{stats.Upload, stats.Rate, stats.Body, stats.Real} {
			hist, err := stats.Decode(blobs[i], metric)
			if err != nil {
				return nil, err
			}
			target := bucket.Upload
			switch metric {
			case stats.Rate:
				target = bucket.Rate
			case stats.Body:
				target = bucket.Body
			case stats.Real:
				target = bucket.Real
			}
			if err := target.Merge(hist); err != nil {
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*service.RequestTimingAlertBucket, 0, len(order))
	for _, key := range order {
		out = append(out, byBucket[key])
	}
	return out, nil
}

// GetRequestTimingDirtyMinutes lists minutes whose aggregation is still pending.
func (r *opsRepository) GetRequestTimingDirtyMinutes(ctx context.Context, from, to time.Time) (map[time.Time]struct{}, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	rows, err := r.timingAlertConn(ctx).QueryContext(ctx, `SELECT minute_start FROM ops_request_timing_dirty WHERE minute_start >= $1 AND minute_start < $2`, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dirty := map[time.Time]struct{}{}
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		dirty[at.UTC()] = struct{}{}
	}
	return dirty, rows.Err()
}

// timingAlertPlatform mirrors repository.timingPlatform but is written against this
// query's aliases (v/g/a). Keeping it local means a change to the root projection
// cannot silently turn alerting SQL into an "missing FROM-clause entry for ul" error.
const timingAlertPlatform = `lower(COALESCE(NULLIF(g.platform,''),NULLIF(a.platform,''),'unknown'))`

// timingAlertLeafGroupFloor is the lowest group id owned by a real group. Parent
// summary rows use a negative sentinel id and must never be summed with their leaves.
const timingAlertLeafGroupFloor = "0"

// GetRequestTimingAlertExact rechecks a histogram-straddled threshold against the same
// projection the rest of the feature uses (v_request_timing), so validity rules cannot
// drift between alerting and the API. The scan is bounded and deadline-bound: over
// budget scopes report OverBudget instead of silently answering approximately.
func (r *opsRepository) GetRequestTimingAlertExact(ctx context.Context, filter *service.RequestTimingAlertFilter, metric string, percentile float64) (*service.RequestTimingAlertSample, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil timing alert filter")
	}
	switch metric {
	case "upload_ms_p90", "upload_kbps_p50", "real_ttfb_ms_p95", "large_body_ratio":
	default:
		return nil, fmt.Errorf("unsupported timing alert metric %q", metric)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	args := []any{filter.StartTime.UTC(), filter.EndTime.UTC()}
	where := []string{"v.created_at >= $1", "v.created_at < $2", "v.is_http_stream"}
	if filter.Platform != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Platform)))
		where = append(where, fmt.Sprintf("%s = $%d", timingAlertPlatform, len(args)))
	}
	if filter.GroupID != nil {
		args = append(args, *filter.GroupID)
		where = append(where, fmt.Sprintf("v.group_id = $%d", len(args)))
	}

	q := `SELECT v.upload_ms, v.upload_kbps, v.real_ttfb_ms, v.body_bytes,
	             v.is_valid_upload, v.is_valid_rate, v.is_valid_real, v.is_valid_body
	        FROM v_request_timing v
	        LEFT JOIN groups g ON g.id = v.group_id
	        LEFT JOIN accounts a ON a.id = v.account_id
	       WHERE ` + strings.Join(where, " AND ") + `
	       LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, service.RequestTimingRowBudget+1)

	rows, err := r.timingAlertConn(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]float64, 0, 256)
	var candidate int64
	var largeBody int64
	for rows.Next() {
		candidate++
		var upload, rate, real, body sql.NullFloat64
		var validUpload, validRate, validReal, validBody sql.NullBool
		if err := rows.Scan(&upload, &rate, &real, &body, &validUpload, &validRate, &validReal, &validBody); err != nil {
			return nil, err
		}
		switch metric {
		case "upload_ms_p90":
			if validUpload.Valid && validUpload.Bool && upload.Valid {
				values = append(values, upload.Float64)
			}
		case "upload_kbps_p50":
			if validRate.Valid && validRate.Bool && rate.Valid {
				values = append(values, rate.Float64)
			}
		case "real_ttfb_ms_p95":
			if validReal.Valid && validReal.Bool && real.Valid {
				values = append(values, real.Float64)
			}
		case "large_body_ratio":
			if validBody.Valid && validBody.Bool && body.Valid {
				if body.Float64 > float64(service.RequestTimingLargeBody) {
					largeBody++
				}
				values = append(values, body.Float64)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sample := &service.RequestTimingAlertSample{Candidate: candidate}
	if candidate > service.RequestTimingRowBudget {
		sample.OverBudget = true
		return sample, nil
	}
	sample.N = int64(len(values))
	if sample.N == 0 {
		return sample, nil
	}
	if n := float64(candidate); n > 0 {
		coverage := float64(sample.N) / n
		sample.Coverage = &coverage
	}
	if metric == "large_body_ratio" {
		value := float64(largeBody) / float64(sample.N)
		sample.Value = &value
		return sample, nil
	}
	sample.Value = stats.Exact(values, percentile)
	return sample, nil
}

// WithRequestTimingAlertRuleLock runs fn inside a transaction holding the per-rule
// advisory lock, so firing/resolution for one rule cannot interleave.
func (r *opsRepository) WithRequestTimingAlertRuleLock(ctx context.Context, ruleID int64, fn func(ctx context.Context, tx service.RequestTimingAlertTx) error) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if ruleID <= 0 {
		return fmt.Errorf("invalid rule id")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ruleID); err != nil {
		return err
	}
	txCtx := context.WithValue(ctx, timingAlertTxKey, tx)
	if err := fn(txCtx, &timingAlertTx{repo: r, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type timingAlertTx struct {
	repo *opsRepository
	tx   *sql.Tx
}

const timingAlertEventColumns = `id, COALESCE(rule_id, 0), COALESCE(severity, ''), COALESCE(status, ''),
	COALESCE(title, ''), COALESCE(description, ''), metric_value, threshold_value, dimensions,
	fired_at, resolved_at, email_sent, created_at`

func (t *timingAlertTx) GetActiveAlertEvent(ctx context.Context, ruleID int64) (*service.OpsAlertEvent, error) {
	row := t.tx.QueryRowContext(ctx, `SELECT `+timingAlertEventColumns+` FROM ops_alert_events WHERE rule_id=$1 AND status=$2 ORDER BY fired_at DESC LIMIT 1`, ruleID, service.OpsAlertStatusFiring)
	ev, err := scanOpsAlertEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ev, err
}

func (t *timingAlertTx) GetLatestAlertEvent(ctx context.Context, ruleID int64) (*service.OpsAlertEvent, error) {
	row := t.tx.QueryRowContext(ctx, `SELECT `+timingAlertEventColumns+` FROM ops_alert_events WHERE rule_id=$1 ORDER BY fired_at DESC LIMIT 1`, ruleID)
	ev, err := scanOpsAlertEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ev, err
}

func (t *timingAlertTx) CreateAlertEvent(ctx context.Context, event *service.OpsAlertEvent) (*service.OpsAlertEvent, error) {
	if event == nil {
		return nil, fmt.Errorf("nil event")
	}
	dimensions, err := opsNullJSONMap(event.Dimensions)
	if err != nil {
		return nil, err
	}
	row := t.tx.QueryRowContext(ctx, `INSERT INTO ops_alert_events (
		rule_id, severity, status, title, description, metric_value, threshold_value, dimensions,
		fired_at, resolved_at, email_sent, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW())
	RETURNING `+timingAlertEventColumns,
		opsNullInt64(&event.RuleID), opsNullString(event.Severity), opsNullString(event.Status),
		opsNullString(event.Title), opsNullString(event.Description), opsNullFloat64(event.MetricValue),
		opsNullFloat64(event.ThresholdValue), dimensions, event.FiredAt, opsNullTime(event.ResolvedAt), event.EmailSent)
	return scanOpsAlertEvent(row)
}

func (t *timingAlertTx) UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error {
	if eventID <= 0 || strings.TrimSpace(status) == "" {
		return fmt.Errorf("invalid event update")
	}
	_, err := t.tx.ExecContext(ctx, `UPDATE ops_alert_events SET status=$2, resolved_at=$3 WHERE id=$1`, eventID, strings.TrimSpace(status), opsNullTime(resolvedAt))
	return err
}

func (t *timingAlertTx) LoadRequestTimingAlertState(ctx context.Context, ruleID int64) (*service.RequestTimingAlertState, error) {
	row := t.tx.QueryRowContext(ctx, `SELECT rule_id, evaluated_bucket, state, sample_count, coverage_ratio, metric_value, COALESCE(reason, ''), updated_at
		FROM ops_request_timing_alert_state WHERE rule_id=$1`, ruleID)
	return scanTimingAlertStateRow(row)
}

func (t *timingAlertTx) SaveRequestTimingAlertState(ctx context.Context, state *service.RequestTimingAlertState) error {
	if state == nil {
		return fmt.Errorf("nil timing alert state")
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO ops_request_timing_alert_state (
		rule_id, evaluated_bucket, state, sample_count, coverage_ratio, metric_value, reason, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	ON CONFLICT (rule_id) DO UPDATE SET
		evaluated_bucket = EXCLUDED.evaluated_bucket,
		state = EXCLUDED.state,
		sample_count = EXCLUDED.sample_count,
		coverage_ratio = EXCLUDED.coverage_ratio,
		metric_value = EXCLUDED.metric_value,
		reason = EXCLUDED.reason,
		updated_at = EXCLUDED.updated_at`,
		state.RuleID, state.EvaluatedBucket.UTC(), state.State, state.SampleCount,
		opsNullFloat64(state.Coverage), opsNullFloat64(state.MetricValue), opsNullString(state.Reason), state.UpdatedAt.UTC())
	return err
}

type timingAlertStateRow interface {
	Scan(dest ...any) error
}

func scanTimingAlertStateRow(row timingAlertStateRow) (*service.RequestTimingAlertState, error) {
	var state service.RequestTimingAlertState
	var bucket sql.NullTime
	var coverage, value sql.NullFloat64
	if err := row.Scan(&state.RuleID, &bucket, &state.State, &state.SampleCount, &coverage, &value, &state.Reason, &state.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if bucket.Valid {
		state.EvaluatedBucket = bucket.Time.UTC()
	}
	if coverage.Valid {
		v := coverage.Float64
		state.Coverage = &v
	}
	if value.Valid {
		v := value.Float64
		state.MetricValue = &v
	}
	return &state, nil
}

// ReadRequestTimingAlertState is used by tests and diagnostics outside the lock.
func (r *opsRepository) ReadRequestTimingAlertState(ctx context.Context, ruleID int64) (*service.RequestTimingAlertState, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	row := r.timingAlertConn(ctx).QueryRowContext(ctx, `SELECT rule_id, evaluated_bucket, state, sample_count, coverage_ratio, metric_value, COALESCE(reason, ''), updated_at
		FROM ops_request_timing_alert_state WHERE rule_id=$1`, ruleID)
	return scanTimingAlertStateRow(row)
}

// ListRequestTimingAlertStates loads the persisted evaluation of several rules in one
// query so the alert-rule list stays O(1) round trips.
func (r *opsRepository) ListRequestTimingAlertStates(ctx context.Context, ruleIDs []int64) (map[int64]*service.RequestTimingAlertState, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if len(ruleIDs) == 0 {
		return map[int64]*service.RequestTimingAlertState{}, nil
	}
	unique := make([]int64, 0, len(ruleIDs))
	seen := make(map[int64]struct{}, len(ruleIDs))
	for _, id := range ruleIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[int64]*service.RequestTimingAlertState{}, nil
	}

	rows, err := r.timingAlertConn(ctx).QueryContext(ctx,
		`SELECT rule_id, evaluated_bucket, state, sample_count, coverage_ratio,
		        metric_value, COALESCE(reason, ''), updated_at
		   FROM ops_request_timing_alert_state
		  WHERE rule_id = ANY($1)`, pq.Array(unique))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]*service.RequestTimingAlertState, len(unique))
	for rows.Next() {
		var state service.RequestTimingAlertState
		var evaluated sql.NullTime
		var coverage, metric sql.NullFloat64
		if err := rows.Scan(&state.RuleID, &evaluated, &state.State, &state.SampleCount,
			&coverage, &metric, &state.Reason, &state.UpdatedAt); err != nil {
			return nil, err
		}
		if evaluated.Valid {
			state.EvaluatedBucket = evaluated.Time.UTC()
		}
		if coverage.Valid {
			state.Coverage = &coverage.Float64
		}
		if metric.Valid {
			state.MetricValue = &metric.Float64
		}
		out[state.RuleID] = &state
	}
	return out, rows.Err()
}
