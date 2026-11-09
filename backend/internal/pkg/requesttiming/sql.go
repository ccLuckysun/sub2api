package requesttiming

// SQL shared with ops aggregation. Keep these statements in one place so the
// enricher and the minute aggregator use exactly the same dirty-bucket
// revision semantics.
const (
	// PatchGatewayLatencySQL updates only the row created by this request and
	// only when no value has been written yet. The created_at predicate keeps
	// the update partition-safe.
	PatchGatewayLatencySQL = `
		UPDATE usage_logs
		SET gateway_latency_ms = $1::integer
		WHERE id = $2::bigint
		  AND created_at = $3::timestamptz
		  AND gateway_latency_ms IS NULL
		RETURNING id`

	// UpsertDirtyMinuteSQL atomically marks a minute bucket dirty. revision is
	// monotonic within the row: the sequence value is allocated by nextval and
	// only replaces the stored revision when it is greater. This lets the
	// aggregator use a claimed revision for a conditional delete/CAS.
	UpsertDirtyMinuteSQL = `
		INSERT INTO ops_request_timing_dirty (
			minute_start, revision, reason, attempts, next_attempt_at
		) VALUES (
			$1::timestamptz,
			nextval('ops_request_timing_revision_seq'),
			$2::text,
			0,
			now()
		)
		ON CONFLICT (minute_start) DO UPDATE SET
			revision = GREATEST(ops_request_timing_dirty.revision, EXCLUDED.revision),
			reason = EXCLUDED.reason,
			attempts = 0,
			next_attempt_at = now()`
)
