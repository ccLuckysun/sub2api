-- Statement-level transition tables mark each affected minute only once per
-- INSERT batch, atomically with the source rows. This also covers Ent/auxiliary
-- insert paths and late commits without adding a per-row hot-minute write.
CREATE OR REPLACE FUNCTION mark_request_timing_insert_minutes()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO ops_request_timing_dirty(minute_start,revision,reason)
  SELECT minute_start,nextval('ops_request_timing_revision_seq'),'usage_insert'
  FROM (SELECT DISTINCT date_trunc('minute',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS minute_start FROM request_timing_inserted_rows
        WHERE stream AND NOT openai_ws_mode AND request_type IN(0,2)) source
  ON CONFLICT(minute_start) DO UPDATE SET
    revision=GREATEST(ops_request_timing_dirty.revision,EXCLUDED.revision),
    reason='usage_insert',attempts=0,next_attempt_at=now();
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS request_timing_insert_minutes ON usage_logs;
CREATE TRIGGER request_timing_insert_minutes AFTER INSERT ON usage_logs
REFERENCING NEW TABLE AS request_timing_inserted_rows
FOR EACH STATEMENT EXECUTE FUNCTION mark_request_timing_insert_minutes();
