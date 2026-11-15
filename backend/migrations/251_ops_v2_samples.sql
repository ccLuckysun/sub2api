-- V2 request-level transfer samples (MONITORING_V2_API section 7.3).
-- Forward-only: create a new table or add missing columns without rewriting data.
-- Required observation/request identity fields intentionally have no defaults.
-- Existing incompatible types/constraints require a separate pre-deployment audit;
-- adding a missing required field to populated legacy data must fail, not invent it.
CREATE TABLE IF NOT EXISTS ops_request_transfer_samples (
  id BIGSERIAL PRIMARY KEY,
  observed_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL,
  user_id BIGINT, api_key_id BIGINT, account_id BIGINT, group_id BIGINT,
  platform TEXT, model TEXT, endpoint_class TEXT, endpoint TEXT,
  result TEXT, http_status INT, failure_reason TEXT,
  attempt_count INT, failed_attempts INT, node TEXT,
  body_read_ms INT, gateway_ms INT, wait_ms INT, upstream_ms INT,
  headers_end_ms INT, response_ms INT, finish_ms INT, total_ms INT,
  bytes_in_declared BIGINT, bytes_in_wire BIGINT, bytes_in_decoded BIGINT,
  bytes_up_req_total BIGINT, bytes_resp_upstream BIGINT, bytes_resp_downstream BIGINT,
  bytes_in_wire_status TEXT, bytes_up_req_total_status TEXT,
  bytes_resp_upstream_status TEXT, bytes_resp_downstream_status TEXT,
  bytes_in_wire_source TEXT, bytes_up_req_total_source TEXT,
  bytes_resp_upstream_source TEXT, bytes_resp_downstream_source TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE ops_request_transfer_samples
    ADD COLUMN IF NOT EXISTS id BIGSERIAL PRIMARY KEY,
    ADD COLUMN IF NOT EXISTS observed_at TIMESTAMPTZ NOT NULL,
    ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL,
    ADD COLUMN IF NOT EXISTS user_id BIGINT,
    ADD COLUMN IF NOT EXISTS api_key_id BIGINT,
    ADD COLUMN IF NOT EXISTS account_id BIGINT,
    ADD COLUMN IF NOT EXISTS group_id BIGINT,
    ADD COLUMN IF NOT EXISTS platform TEXT,
    ADD COLUMN IF NOT EXISTS model TEXT,
    ADD COLUMN IF NOT EXISTS endpoint_class TEXT,
    ADD COLUMN IF NOT EXISTS endpoint TEXT,
    ADD COLUMN IF NOT EXISTS result TEXT,
    ADD COLUMN IF NOT EXISTS http_status INT,
    ADD COLUMN IF NOT EXISTS failure_reason TEXT,
    ADD COLUMN IF NOT EXISTS attempt_count INT,
    ADD COLUMN IF NOT EXISTS failed_attempts INT,
    ADD COLUMN IF NOT EXISTS node TEXT,
    ADD COLUMN IF NOT EXISTS body_read_ms INT,
    ADD COLUMN IF NOT EXISTS gateway_ms INT,
    ADD COLUMN IF NOT EXISTS wait_ms INT,
    ADD COLUMN IF NOT EXISTS upstream_ms INT,
    ADD COLUMN IF NOT EXISTS headers_end_ms INT,
    ADD COLUMN IF NOT EXISTS response_ms INT,
    ADD COLUMN IF NOT EXISTS finish_ms INT,
    ADD COLUMN IF NOT EXISTS total_ms INT,
    ADD COLUMN IF NOT EXISTS bytes_in_declared BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_in_wire BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_in_decoded BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_up_req_total BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_resp_upstream BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_resp_downstream BIGINT,
    ADD COLUMN IF NOT EXISTS bytes_in_wire_status TEXT,
    ADD COLUMN IF NOT EXISTS bytes_up_req_total_status TEXT,
    ADD COLUMN IF NOT EXISTS bytes_resp_upstream_status TEXT,
    ADD COLUMN IF NOT EXISTS bytes_resp_downstream_status TEXT,
    ADD COLUMN IF NOT EXISTS bytes_in_wire_source TEXT,
    ADD COLUMN IF NOT EXISTS bytes_up_req_total_source TEXT,
    ADD COLUMN IF NOT EXISTS bytes_resp_upstream_source TEXT,
    ADD COLUMN IF NOT EXISTS bytes_resp_downstream_source TEXT,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Keep the initial index budget to the three contract query paths (section 7.4).
-- A time-leading index does not eliminate sorting for ORDER BY total_ms DESC.
CREATE INDEX IF NOT EXISTS idx_ops_transfer_samples_observed_at
    ON ops_request_transfer_samples (observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_ops_transfer_samples_user_observed_at
    ON ops_request_transfer_samples (user_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_ops_transfer_samples_api_key_observed_at
    ON ops_request_transfer_samples (api_key_id, observed_at);
