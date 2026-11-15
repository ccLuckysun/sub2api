-- psql -v password=... -f grafana-ro.sql (local administrator only)
\set ON_ERROR_STOP on
BEGIN;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro') THEN
    CREATE ROLE grafana_ro LOGIN;
  END IF;
END
$$;
ALTER ROLE grafana_ro WITH PASSWORD :'password';
ALTER ROLE grafana_ro SET default_transaction_read_only = on;
ALTER ROLE grafana_ro SET statement_timeout = '15s';

-- Reset direct grants on this dedicated role; never alter the application role.
ALTER ROLE grafana_ro NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
REVOKE ALL ON usage_logs, ops_request_transfer_samples, accounts, groups,
 api_keys, users, ops_system_metrics, ops_metrics_hourly, ops_metrics_daily FROM grafana_ro;
REVOKE ALL ON SCHEMA public FROM grafana_ro;
DO $$ BEGIN EXECUTE format('GRANT CONNECT ON DATABASE %I TO grafana_ro', current_database()); END $$;
GRANT USAGE ON SCHEMA public TO grafana_ro;

-- 最小权限：只授面板需要的表/列
GRANT SELECT ON usage_logs, ops_request_transfer_samples,
                accounts, groups, api_keys, ops_system_metrics,
                ops_metrics_hourly, ops_metrics_daily
  TO grafana_ro;
GRANT SELECT (id, email) ON users TO grafana_ro;
GRANT SELECT (id, name) ON api_keys TO grafana_ro;

-- 仅当迁移 owner 确定后执行；owner 用 psql 变量传入
-- ALTER DEFAULT PRIVILEGES FOR ROLE :owner IN SCHEMA public
--   GRANT SELECT ON TABLES TO grafana_ro;

-- PostgreSQL <=14 may grant CREATE to PUBLIC by default. Fail rather than
-- altering application/public grants implicitly. See MONITORING_LOCAL.md.
DO $$ BEGIN
 IF has_schema_privilege('grafana_ro', 'public', 'CREATE') THEN
  RAISE EXCEPTION 'grafana_ro inherits CREATE on public; remove PUBLIC CREATE explicitly before provisioning';
 END IF;
END $$;
COMMIT;
