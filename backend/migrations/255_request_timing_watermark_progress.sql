-- A running scheduler can still make no progress (e.g. a lease held by another
-- replica). Track cursor advancement separately from attempts and old repairs.
ALTER TABLE ops_request_timing_state ADD COLUMN IF NOT EXISTS watermark_updated_at timestamptz;
UPDATE ops_request_timing_state SET watermark_updated_at=clock_timestamp()
WHERE singleton AND finalized_through IS NOT NULL AND watermark_updated_at IS NULL;

CREATE OR REPLACE FUNCTION request_timing_watermark_progress()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.finalized_through IS DISTINCT FROM OLD.finalized_through THEN
    NEW.watermark_updated_at := clock_timestamp();
  END IF;
  RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS request_timing_watermark_progress ON ops_request_timing_state;
CREATE TRIGGER request_timing_watermark_progress BEFORE UPDATE ON ops_request_timing_state
FOR EACH ROW EXECUTE FUNCTION request_timing_watermark_progress();
