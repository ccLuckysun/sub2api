#!/usr/bin/env python3
"""Isolated local PG permission test; no host ports, production or SMTP access.
Uses a disposable fixture, not an application migration acceptance test.
"""
from pathlib import Path
import runpy
import secrets
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
NAME = 'sub2api-w5-pg-' + secrets.token_hex(4)
PASSWORD = secrets.token_hex(16)
FIXTURE = runpy.run_path(str(Path(__file__).with_name('grafana-variables-pg18-test.py')))


def docker(*args, stdin=None, check=True):
    return subprocess.run(['docker', *args], input=stdin, text=True, capture_output=True, check=check)


def sql(text, user='postgres', check=True):
    return docker('exec', '-i', '-e', 'PGPASSWORD='+PASSWORD, NAME, 'psql', '-X', '-qAt',
                  '-h', '127.0.0.1', '-U', user, '-d', 'sub2api', '-v', 'ON_ERROR_STOP=1',
                  stdin=text, check=check)


try:
    docker('run', '-d', '--rm', '--name', NAME, '--network', 'none', '--tmpfs', '/var/lib/postgresql',
           '-e', 'POSTGRES_PASSWORD='+PASSWORD, '-e', 'POSTGRES_DB=sub2api', 'postgres:18-alpine')
    for _ in range(60):
        if docker('exec',NAME,'pg_isready','-U','postgres',check=False).returncode == 0:
            # pg_isready can see initdb's temporary server; require TCP.
            if sql('SELECT 1;',check=False).returncode == 0: break
        time.sleep(.5)
    else: raise RuntimeError('local PG did not become ready')
    sql(FIXTURE['BASE_SCHEMA'])
    for migration in FIXTURE['SCHEMA_MIGRATIONS']:
        sql((ROOT / 'backend/migrations' / migration).read_text())
    sql("""CREATE ROLE sub2api LOGIN;
INSERT INTO users VALUES (1, 'fixture@example.invalid', 'not-a-real-hash');
INSERT INTO api_keys VALUES (1, 'fixture-key', 1, 'short-value', 'active', NULL);""")
    before=sql("SELECT rolconfig FROM pg_roles WHERE rolname='sub2api';").stdout
    provisioning = '\\getenv password PGPASSWORD\n' + (ROOT/'deploy/monitoring/sql/grafana-ro.sql').read_text()
    sql(provisioning)
    sql(provisioning)
    assert sql("SELECT rolconfig FROM pg_roles WHERE rolname='sub2api';").stdout==before
    assert sql('SELECT id,email FROM users;', user='grafana_ro').stdout.strip() == '1|fixture@example.invalid'
    assert sql('SELECT id,user_id,name,masked_key FROM grafana_api_keys;',
               user='grafana_ro').stdout.strip() == '1|1|fixture-key|…'
    print('PASS: TCP/password SELECT and idempotent provisioning; application role unchanged')
    for query in ["SET default_transaction_read_only=off; UPDATE users SET email='x';",
                  'SET default_transaction_read_only=off; CREATE TABLE public.w5_forbidden(i int);',
                  'SELECT password_hash FROM users;', 'SELECT key FROM api_keys;',
                  'SELECT credentials, extra FROM accounts;']:
        result=sql(query,user='grafana_ro',check=False)
        assert result.returncode != 0 and 'permission denied' in result.stderr, result.stderr
    print('PASS: read-only OFF still rejects UPDATE / CREATE TABLE; raw credential columns denied')
    start=time.monotonic();result=sql('SELECT pg_sleep(20);',user='grafana_ro',check=False);elapsed=time.monotonic()-start
    assert result.returncode != 0 and 'statement timeout' in result.stderr and 14<=elapsed<19, (elapsed,result.stderr)
    print(f'PASS: pg_sleep(20) canceled by 15s statement timeout ({elapsed:.2f}s)')
finally:
    docker('rm','-f',NAME,check=False)
