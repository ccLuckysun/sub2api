-- Request timing raw facts on usage_logs.
--
-- gateway_latency_ms: server-side span measured by the HTTP logger from request
--   start to the completed response (Logger completion point). It is written
--   asynchronously after the billing row is committed, so historical / failed
--   rows may remain NULL.
-- body_bytes: canonical decoded request body size after Content-Encoding
--   decompression and before lenient JSON normalization. NULL means the body
--   was not observed (no body, read failure, or collection disabled).
--
-- Deliberately additive and nullable: no backfill, no default, no index.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS gateway_latency_ms INTEGER;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS body_bytes BIGINT;
