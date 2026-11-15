-- V2 usage fields are nullable for historical rows and incomplete observations.
-- body_bytes: actual inbound wire bytes read (M-2 in_wire).
-- gateway_latency_ms: gateway + wait + finish, in milliseconds.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS body_bytes BIGINT NULL,
    ADD COLUMN IF NOT EXISTS gateway_latency_ms BIGINT NULL;
