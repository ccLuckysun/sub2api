-- Request telemetry is independent of billing. Existing rows remain unknown.
CREATE SEQUENCE IF NOT EXISTS ops_request_timing_revision_seq AS bigint;
CREATE TABLE IF NOT EXISTS ops_request_timing_dirty (
  minute_start timestamptz PRIMARY KEY,
  revision bigint NOT NULL DEFAULT nextval('ops_request_timing_revision_seq'),
  reason text NOT NULL DEFAULT 'usage',
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ops_request_timing_dirty_due ON ops_request_timing_dirty(next_attempt_at,minute_start);

CREATE TABLE IF NOT EXISTS ops_request_timing_minute (
  bucket_start timestamptz NOT NULL,
  platform text NOT NULL,
  group_id bigint NOT NULL DEFAULT 0,
  hist_version smallint NOT NULL,
  hist_schema text NOT NULL,
  candidate_count bigint NOT NULL,
  complete_count bigint NOT NULL,
  invalid_count bigint NOT NULL,
  upload_count bigint NOT NULL,
  ttft_count bigint NOT NULL,
  real_count bigint NOT NULL,
  rate_count bigint NOT NULL,
  known_body_count bigint NOT NULL,
  large_body_count bigint NOT NULL,
  total_observed_count bigint NOT NULL,
  body_observed_count bigint NOT NULL,
  upload_hist bytea NOT NULL,
  rate_hist bytea NOT NULL,
  body_hist bytea NOT NULL,
  real_hist bytea NOT NULL,
  revision bigint NOT NULL,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(bucket_start,platform,group_id),
  CONSTRAINT request_timing_hist_size CHECK (
    octet_length(upload_hist)+octet_length(rate_hist)+octet_length(body_hist)+octet_length(real_hist)<=65536
  )
);
CREATE INDEX IF NOT EXISTS ops_request_timing_minute_group ON ops_request_timing_minute(group_id,bucket_start);
CREATE INDEX IF NOT EXISTS ops_request_timing_minute_platform ON ops_request_timing_minute(platform,bucket_start);

CREATE TABLE IF NOT EXISTS ops_request_timing_state (
  singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
  finalized_through timestamptz,
  computed_at timestamptz,
  source_retained_from timestamptz,
  drift_checked_at timestamptz,
  last_error text
);
INSERT INTO ops_request_timing_state(singleton) VALUES(true) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS ops_request_timing_alert_state (
  rule_id bigint PRIMARY KEY REFERENCES ops_alert_rules(id) ON DELETE CASCADE,
  evaluated_bucket timestamptz,
  state text NOT NULL,
  sample_count bigint NOT NULL DEFAULT 0,
  coverage_ratio double precision,
  metric_value double precision,
  reason text,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE VIEW v_request_timing AS
WITH b AS (
 SELECT ul.id,ul.created_at,ul.user_id,ul.api_key_id,ul.account_id,ul.group_id,ul.model,
        ul.request_id,ul.upstream_request_id,ul.stream,ul.request_type,ul.openai_ws_mode,
        ul.gateway_latency_ms,ul.body_bytes,ul.duration_ms,ul.first_token_ms,
        ul.input_tokens,ul.cache_read_tokens,ul.output_tokens,
        ul.input_tokens::bigint+ul.cache_read_tokens::bigint AS ctx_tokens,
        (ul.stream AND NOT ul.openai_ws_mode AND
          (ul.request_type=2 OR (ul.request_type=0 AND ul.gateway_latency_ms IS NOT NULL))) AS is_http_stream,
        (ul.image_count=0 AND ul.image_output_tokens=0 AND ul.video_count=0
          AND (ul.billing_mode IS NULL OR ul.billing_mode='token')) AS text_usage
 FROM usage_logs ul
), d AS (
 SELECT b.*,
   CASE WHEN is_http_stream AND gateway_latency_ms IS NOT NULL AND duration_ms IS NOT NULL
     AND gateway_latency_ms>=0 AND duration_ms>=0
     THEN gateway_latency_ms::bigint-duration_ms::bigint END AS raw_upload_ms,
   CASE WHEN is_http_stream AND gateway_latency_ms IS NOT NULL AND duration_ms IS NOT NULL
     AND gateway_latency_ms>=0 AND duration_ms>=0
     THEN GREATEST(gateway_latency_ms::bigint-duration_ms::bigint,0) END AS upload_ms,
   CASE WHEN is_http_stream AND first_token_ms IS NOT NULL AND first_token_ms>=0
     THEN first_token_ms::bigint END AS upstream_ttft_ms,
   CASE WHEN is_http_stream AND duration_ms IS NOT NULL AND first_token_ms IS NOT NULL
     AND duration_ms>=0 AND first_token_ms>=0
     THEN GREATEST(duration_ms::bigint-first_token_ms::bigint,0) END AS stream_ms,
   COALESCE(gateway_latency_ms<0 OR duration_ms<0 OR first_token_ms<0 OR body_bytes<0,FALSE)
     OR (is_http_stream AND COALESCE(first_token_ms>duration_ms,FALSE))
     OR (is_http_stream AND COALESCE(gateway_latency_ms::bigint-duration_ms::bigint < -300,FALSE)) AS invalid
 FROM b
), v AS (
 SELECT d.*,
   upload_ms+upstream_ttft_ms AS real_ttfb_ms,
   upload_ms+upstream_ttft_ms+stream_ms-gateway_latency_ms::bigint AS timing_residual_ms,
   (is_http_stream AND NOT invalid AND raw_upload_ms>=0) IS TRUE AS is_valid_upload,
   (is_http_stream AND NOT invalid AND raw_upload_ms>=0 AND first_token_ms IS NOT NULL) IS TRUE AS is_valid_ttft,
   (is_http_stream AND NOT invalid AND gateway_latency_ms IS NOT NULL AND duration_ms IS NOT NULL AND body_bytes>=0) IS TRUE AS is_valid_body
 FROM d
)
SELECT v.*,
 gateway_latency_ms AS total_ms,
 is_valid_ttft AS is_valid_real,
 (is_valid_upload AND is_valid_body AND upload_ms>0) AS is_valid_rate,
 (is_valid_upload AND is_valid_ttft AND is_valid_body) AS is_complete_timing,
 CASE WHEN invalid THEN 'invalid'
      WHEN NOT is_http_stream OR gateway_latency_ms IS NULL OR duration_ms IS NULL THEN 'unavailable'
      WHEN raw_upload_ms<0 OR first_token_ms IS NULL OR body_bytes IS NULL THEN 'partial'
      ELSE 'complete' END AS timing_status,
 'legacy_difference_v1'::text AS timing_method,
 CASE WHEN invalid THEN 'inconsistent_observation'
      WHEN NOT is_http_stream THEN 'unsupported_protocol'
      WHEN gateway_latency_ms IS NULL OR duration_ms IS NULL THEN 'missing_total_or_duration'
      WHEN raw_upload_ms<0 THEN 'clamped_total'
      WHEN first_token_ms IS NULL THEN 'missing_first_token'
      WHEN body_bytes IS NULL THEN 'missing_body_bytes' END AS reason,
 CASE WHEN is_valid_upload AND is_valid_body AND upload_ms>0
      THEN body_bytes::numeric*1000/1024/upload_ms END AS upload_kbps,
 CASE WHEN body_bytes>=0 AND ctx_tokens>0 THEN body_bytes::numeric/ctx_tokens END AS bytes_per_token,
 CASE WHEN text_usage AND output_tokens>0 AND duration_ms>0 THEN output_tokens::numeric*1000/duration_ms END AS tps_total,
 CASE WHEN text_usage AND is_valid_ttft AND output_tokens>0 AND stream_ms>0 THEN output_tokens::numeric*1000/stream_ms END AS tps_stream
FROM v;

COMMENT ON VIEW v_request_timing IS
 'HTTP streaming server-span estimates; upload includes gateway overhead; NULL is unknown, never zero-filled.';
