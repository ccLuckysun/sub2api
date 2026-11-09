-- computed_at describes a materialized minute, not scheduler health. Keep
-- attempt/success/error times separate so a lagging cursor cannot look healthy.
ALTER TABLE ops_request_timing_state ADD COLUMN IF NOT EXISTS last_run_at timestamptz;
ALTER TABLE ops_request_timing_state ADD COLUMN IF NOT EXISTS last_success_at timestamptz;
ALTER TABLE ops_request_timing_state ADD COLUMN IF NOT EXISTS last_error_at timestamptz;
