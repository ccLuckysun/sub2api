-- psql -v password=... < grafana-ro.sql (local administrator only)
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL search_path = pg_catalog, public;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro') THEN
    CREATE ROLE grafana_ro LOGIN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = 'grafana_ro'::regrole) THEN
    RAISE EXCEPTION 'grafana_ro must not be a member of other roles';
  END IF;
END
$$;
ALTER ROLE grafana_ro WITH LOGIN PASSWORD :'password';
ALTER ROLE grafana_ro SET default_transaction_read_only = on;
ALTER ROLE grafana_ro SET statement_timeout = '15s';

-- Reset direct grants on this dedicated role; never alter the application role.
ALTER ROLE grafana_ro NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM grafana_ro;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM grafana_ro;
REVOKE ALL ON SCHEMA public FROM grafana_ro;
DO $$
DECLARE
  old_grant record;
BEGIN
  FOR old_grant IN
    SELECT c.relname, string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum) AS columns
    FROM pg_class c
    JOIN pg_attribute a ON a.attrelid = c.oid
    WHERE c.relnamespace = 'public'::regnamespace AND a.attnum > 0 AND NOT a.attisdropped
      AND EXISTS (SELECT 1 FROM aclexplode(a.attacl) WHERE grantee = 'grafana_ro'::regrole)
    GROUP BY c.oid, c.relname
  LOOP
    EXECUTE format('REVOKE ALL (%s) ON public.%I FROM grafana_ro', old_grant.columns, old_grant.relname);
  END LOOP;
  EXECUTE format('REVOKE ALL ON DATABASE %I FROM grafana_ro', current_database());
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO grafana_ro', current_database());
END
$$;
GRANT USAGE ON SCHEMA public TO grafana_ro;

CREATE OR REPLACE VIEW public.grafana_api_keys WITH (security_barrier = true) AS
SELECT id, user_id, name, status, last_used_at,
       CASE WHEN char_length(key) >= 24 THEN left(key, 8) || '…' || right(key, 4)
            ELSE '…' END AS masked_key
FROM public.api_keys;
ALTER VIEW public.grafana_api_keys OWNER TO CURRENT_USER;
REVOKE ALL ON public.grafana_api_keys FROM PUBLIC;
GRANT SELECT ON public.grafana_api_keys TO grafana_ro;

GRANT SELECT (id, email) ON public.users TO grafana_ro;
GRANT SELECT (id, name) ON public.groups TO grafana_ro;
GRANT SELECT (id, name, platform, status, deleted_at, schedulable, concurrency,
              overload_until, rate_limited_at) ON public.accounts TO grafana_ro;
GRANT SELECT (id, request_id, api_key_id, user_id, account_id, created_at, upstream_model,
              inbound_endpoint, upstream_endpoint, stream, billing_mode, input_tokens,
              output_tokens, cache_read_tokens, cache_creation_tokens, actual_cost,
              duration_ms, ip_address, user_agent) ON public.usage_logs TO grafana_ro;
GRANT SELECT (id, observed_at, request_id, client_request_id, user_id, api_key_id, account_id,
              group_id, platform, model, endpoint, result, http_status, failure_reason,
              attempt_count, failed_attempts, node, body_read_ms, gateway_ms, wait_ms,
              upstream_ms, headers_end_ms, response_ms, finish_ms, total_ms, bytes_in_wire,
              bytes_in_decoded, bytes_up_req_total, bytes_resp_upstream, bytes_resp_downstream,
              bytes_in_wire_status, bytes_up_req_total_status, bytes_resp_upstream_status,
              bytes_resp_downstream_status) ON public.ops_request_transfer_samples TO grafana_ro;
GRANT SELECT (created_at, db_conn_active, db_conn_idle, concurrency_queue_depth, redis_ok, qps, tps)
  ON public.ops_system_metrics TO grafana_ro;

-- PostgreSQL <=14 may grant CREATE to PUBLIC by default. Fail rather than
-- altering application/public grants implicitly. See MONITORING_LOCAL.md.
DO $$
BEGIN
  IF has_schema_privilege('grafana_ro', 'public', 'CREATE') THEN
    RAISE EXCEPTION 'grafana_ro inherits CREATE on public; remove PUBLIC CREATE explicitly before provisioning';
  END IF;
  IF has_any_column_privilege('grafana_ro', 'public.api_keys', 'SELECT') OR EXISTS (
    SELECT 1 FROM pg_attribute
    WHERE attnum > 0 AND NOT attisdropped
      AND ((attrelid = 'public.accounts'::regclass AND attname NOT IN (
        'id', 'name', 'platform', 'status', 'deleted_at', 'schedulable', 'concurrency',
        'overload_until', 'rate_limited_at'))
        OR (attrelid = 'public.users'::regclass AND attname NOT IN ('id', 'email')))
      AND has_column_privilege('grafana_ro', attrelid, attnum, 'SELECT')
  ) THEN
    RAISE EXCEPTION 'grafana_ro retains access to protected base columns; check object ownership and PUBLIC grants';
  END IF;
END
$$;
COMMIT;
