#!/usr/bin/env python3
"""Disposable PG18 + actual 251/252 migrations, then read-only AC-4 queries.
Only a unique network-isolated container is written. No dev/production writes.
"""
import hashlib
import json
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]


def main():
    name = 'sub2api-ac4-' + secrets.token_hex(5)
    password = secrets.token_hex(24)

    def docker(*args, stdin=None, check=True):
        result = subprocess.run(['docker', *args], input=stdin, text=True,
                                capture_output=True, timeout=60)
        if check and result.returncode:
            raise RuntimeError(result.stderr)
        return result

    def sql(text):
        return docker('exec', '-i', name, 'psql', '-X', '-qAt', '-U', 'postgres',
                      '-d', 'sub2api', '-v', 'ON_ERROR_STOP=1',
                      '-v', 'password=' + password, stdin=text)

    try:
        docker('run', '-d', '--rm', '--name', name, '--network', 'none',
               '-e', 'POSTGRES_PASSWORD=' + password, '-e', 'POSTGRES_DB=sub2api',
               'postgres:18-alpine')
        for _ in range(60):
            if docker('exec', name, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres', check=False).returncode == 0:
                break
            time.sleep(.25)
        else:
            raise RuntimeError('PG18 did not become ready')
        sql('''BEGIN;
CREATE TABLE users(id bigint PRIMARY KEY,email text);
CREATE TABLE api_keys(id bigint PRIMARY KEY,name text,user_id bigint);
CREATE TABLE usage_logs(id bigserial PRIMARY KEY,created_at timestamptz,
 user_id bigint,api_key_id bigint,model text,input_tokens bigint,output_tokens bigint,
 cache_read_tokens bigint,total_cost numeric,duration_ms bigint,first_token_ms bigint);
CREATE TABLE accounts(id bigint); CREATE TABLE groups(id bigint);
CREATE TABLE ops_system_metrics(id bigint); CREATE TABLE ops_metrics_hourly(id bigint);
CREATE TABLE ops_metrics_daily(id bigint);
COMMIT;''')
        for migration in ['251_ops_v2_samples.sql', '252_add_usage_logs_v2_fields.sql']:
            path = ROOT / 'backend/migrations' / migration
            sql('BEGIN;\n' + path.read_text() + '\nCOMMIT;')
            print(f'MIGRATION {migration}: PASS SHA256={hashlib.sha256(path.read_bytes()).hexdigest()}')
        sql('''BEGIN;
INSERT INTO users VALUES(1,'one@example.invalid'),(2,'two@example.invalid');
INSERT INTO api_keys VALUES(11,'key-a',1),(12,'key-b',1),(21,'key-c',2);
INSERT INTO usage_logs(created_at,user_id,api_key_id,model,input_tokens,output_tokens,
 cache_read_tokens,total_cost,duration_ms,first_token_ms,body_bytes,gateway_latency_ms)
VALUES(now()-interval '2 minutes',1,11,'model-a',1000,100,10,1,40000,100,101,1),
 (now()-interval '2 minutes',1,12,'model-a',2000,200,20,2,50000,200,102,2),
 (now()-interval '2 minutes',2,21,'model-a',3000,300,30,3,60000,300,103,3),
 (now()-interval '2 minutes',1,11,'model-a',4000,400,40,4,1000,400,104,4);
INSERT INTO ops_request_transfer_samples(observed_at,request_id,user_id,api_key_id,result)
VALUES(now()-interval '2 minutes','request-1',1,11,'success'),
 (now()-interval '2 minutes','request-2',1,12,'upstream_error'),
 (now()-interval '2 minutes','request-3',2,21,'success'),
 (now()-interval '2 minutes','request-4',1,11,'timeout');
COMMIT;''')
        sql((ROOT / 'deploy/monitoring/sql/grafana-ro.sql').read_text())
        print('SEED users=2 api_keys=3 usage_logs=4 samples=4; 251/252 applied; grafana_ro provisioned')
        auth = docker('exec', '-i', '-e', 'PGPASSWORD=' + password, name,
                      'psql', '-X', '-qAt', '-h', '127.0.0.1', '-U', 'grafana_ro',
                      '-d', 'sub2api', '-v', 'ON_ERROR_STOP=1',
                      stdin="SELECT current_user,current_setting('default_transaction_read_only');")
        assert auth.stdout.strip() == 'grafana_ro|on', auth.stdout
        print('ACL TCP password login grafana_ro/default_transaction_read_only=on: PASS')
        for statement in ['UPDATE users SET email=email', 'CREATE TABLE public.ac4_forbidden(id int)']:
            result = docker('exec', '-i', name, 'psql', '-X', '-qAt', '-U', 'grafana_ro', '-d', 'sub2api',
                            '-v', 'ON_ERROR_STOP=1',
                            stdin='BEGIN; SET LOCAL transaction_read_only=off; ' + statement + '; ROLLBACK;', check=False)
            assert result.returncode and 'permission denied' in result.stderr, result.stderr
        print('ACL UPDATE/CREATE denied even with transaction_read_only=off: PASS')
        result = subprocess.run([sys.executable, str(ROOT / 'deploy/tests/grafana-variables-test.py'),
                                 '--container', name], text=True, capture_output=True, timeout=60)
        print(result.stdout, end='')
        if result.returncode:
            raise RuntimeError(f'variable runner exit={result.returncode}: {result.stderr}')
        checks = 0
        for line in result.stdout.splitlines():
            match = re.match(r'QUERY panel=(\d+) user=(\S+) api_key=(\S+) [0-9.]+ms (\{.*\})$', line)
            if not match:
                continue
            panel, user, key, payload = match.groups()
            data = json.loads(payload)
            missing = user == 'no-match' or key == 'no-match'
            expected = 0 if missing else (2 if key == 'single' else (3 if user == 'single' else 4))
            if int(panel) in (1, 2, 3, 6):
                assert (data['total_requests'] or 0) == expected, (line, expected)
            if int(panel) == 4:
                expected_slow = 0 if missing else (1 if key == 'single' else (2 if user == 'single' else 3))
                assert data['rows'] == expected_slow, (line, expected_slow)
            if int(panel) == 5:
                assert data['rows'] == (0 if missing else 1), line
            checks += 1
        assert checks == 54
        print('FILTER_RESULTS PASS 54/54: All=4, user1=3, key11=2 requests; slow=3/2/1; no-match=0')
        print('AC-4 SQL/role/filter PASS; browser/Grafana live switching still NOT EXECUTED')
        return 0
    finally:
        cleanup = docker('rm', '-f', name, check=False)
        print(f'CLEANUP disposable PG exit={cleanup.returncode}; no host ports/dev/production writes')


if __name__ == '__main__':
    raise SystemExit(main())
