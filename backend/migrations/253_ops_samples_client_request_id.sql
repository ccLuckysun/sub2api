-- Correlate request samples with usage_logs.
--
-- usage_logs.request_id is a billing id, not the inbound x-request-id:
--   client:<ctxkey.ClientRequestID> when present, else local:<ctxkey.RequestID>.
-- Samples store the inbound request id, so the correlation UUID must be kept
-- separately. Nullable and additive: historical rows keep NULL and fall back to
-- the local:<request_id> match, and no existing column changes meaning.
ALTER TABLE ops_request_transfer_samples
    ADD COLUMN IF NOT EXISTS client_request_id TEXT NULL;

COMMENT ON COLUMN ops_request_transfer_samples.client_request_id IS
    'ctxkey.ClientRequestID (correlation UUID); usage_logs.request_id = ''client:'' || client_request_id';
