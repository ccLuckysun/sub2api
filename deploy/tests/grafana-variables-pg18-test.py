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
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
OWNER_LABEL = 'sub2api.ac4.owner'


def docker(*args, stdin=None, check=True):
    result = subprocess.run(['docker', *args], input=stdin, text=True,
                            capture_output=True, timeout=60)
    if check and result.returncode:
        raise RuntimeError(result.stderr)
    return result


def cleanup_container(name, owner):
    inspected = docker('inspect', name, check=False)
    if inspected.returncode:
        absent = rf'(?:Error(?: response from daemon)?: )?No such (?:object|container): {re.escape(name)}'
        if re.fullmatch(absent, inspected.stderr.strip()):
            print('CLEANUP disposable PG already absent')
            return
        raise RuntimeError(f'Cannot inspect disposable PG for cleanup: {inspected.stderr}')
    container, = json.loads(inspected.stdout)
    if (container['Config'].get('Labels') or {}).get(OWNER_LABEL) != owner:
        raise RuntimeError(f'Refusing cleanup of unowned container: {name}')
    # Remove the verified ID, not a name that could be reused after inspection.
    cleanup = docker('rm', '-f', container['Id'], check=False)
    if cleanup.returncode:
        raise RuntimeError(f'Disposable PG cleanup failed: {cleanup.stderr}')
    print('CLEANUP disposable PG exit=0; owner verified; tmpfs only, no host ports/dev/production writes')


def main():
    name = 'sub2api-ac4-' + secrets.token_hex(5)
    password = secrets.token_hex(24)
    owner = secrets.token_hex(16)

    def sql(text):
        return docker('exec', '-i', name, 'psql', '-X', '-qAt', '-U', 'postgres',
                      '-d', 'sub2api', '-v', 'ON_ERROR_STOP=1',
                      '-v', 'password=' + password, stdin=text)

    try:
        # PG18 declares VOLUME /var/lib/postgresql. Override that exact path so
        # removing this test container cannot leave an anonymous data volume.
        docker('run', '-d', '--rm', '--name', name, '--network', 'none',
               '--label', OWNER_LABEL + '=' + owner, '--tmpfs', '/var/lib/postgresql',
               '-e', 'POSTGRES_PASSWORD=' + password, '-e', 'POSTGRES_DB=sub2api',
               'postgres:18-alpine')
        mounts = json.loads(docker('inspect', name, '--format', '{{json .Mounts}}').stdout)
        assert not any(mount['Type'] == 'volume' for mount in mounts), mounts
        tmpfs = json.loads(docker('inspect', name, '--format', '{{json .HostConfig.Tmpfs}}').stdout)
        assert '/var/lib/postgresql' in tmpfs, tmpfs
        print('STORAGE /var/lib/postgresql=tmpfs; persistent volumes=0')
        for _ in range(60):
            if docker('exec', name, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres', check=False).returncode == 0:
                break
            time.sleep(.25)
        else:
            raise RuntimeError('PG18 did not become ready')
        sql('''BEGIN;
CREATE TABLE users(id bigint PRIMARY KEY,email text);
CREATE TABLE api_keys(id bigint PRIMARY KEY,name text,user_id bigint,key text,
 status text,last_used_at timestamptz);
CREATE TABLE usage_logs(id bigserial PRIMARY KEY,created_at timestamptz,
 user_id bigint,api_key_id bigint,model text,input_tokens bigint,output_tokens bigint,
 cache_read_tokens bigint,cache_creation_tokens bigint,actual_cost numeric,
 total_cost numeric,duration_ms bigint,first_token_ms bigint);
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
INSERT INTO api_keys VALUES
 (11,'key-a',1,'fixture-key-a-0001','active',NULL),
 (12,'key-b',1,'fixture-key-b-0002','disabled',now()),
 (21,'key-c',2,'fixture-key-c-0003','active',now());
INSERT INTO usage_logs(created_at,user_id,api_key_id,model,input_tokens,output_tokens,
 cache_read_tokens,cache_creation_tokens,actual_cost,total_cost,
 duration_ms,first_token_ms,body_bytes,gateway_latency_ms)
VALUES(now(),1,11,'model-a',1000,100,10,1,1,1,40000,100,101,1),
 (now(),1,12,'model-a',2000,200,20,2,2,2,50000,200,102,2),
 (now(),2,21,'model-a',3000,300,30,3,3,3,60000,300,103,3),
 (now(),1,11,'model-a',4000,400,40,4,4,4,1000,400,104,4);
INSERT INTO ops_request_transfer_samples(observed_at,request_id,user_id,api_key_id,result)
VALUES(now(),'request-1',1,11,'success'),
 (now(),'request-2',1,12,'upstream_error'),
 (now(),'request-3',2,21,'success'),
 (now(),'request-4',1,11,'timeout');
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
        checks = set()
        states = ['All', 'single', 'no-match']
        for line in result.stdout.splitlines():
            match = re.match(r'QUERY panel=(\d+) user=(\S+) api_key=(\S+) [0-9.]+ms (\{.*\})$', line)
            if not match:
                continue
            panel, user, key, payload = match.groups()
            panel = int(panel)
            identity = (panel, user, key)
            assert identity not in checks, line
            data = json.loads(payload)
            missing = user == 'no-match' or key == 'no-match'
            requests = 0 if missing else (2 if key == 'single' else (3 if user == 'single' else 4))
            users = 0 if missing else (1 if key == 'single' or user == 'single' else 2)
            keys = 0 if missing else (1 if key == 'single' else (2 if user == 'single' else 3))
            tokens = 0 if missing else (5555 if key == 'single' else (7777 if user == 'single' else 11110))
            cost = 0 if missing else (5 if key == 'single' else (7 if user == 'single' else 10))
            value = {2: users, 3: requests, 4: tokens, 5: cost, 6: requests, 7: requests}[panel]
            rows = 1 if panel in (2, 3, 4, 5) else (users if panel == 6 else keys)
            assert data['rows'] == rows, (line, rows)
            assert data['nonempty_rows'] == rows, (line, rows)
            # Empty grouped tables have no aggregate value; scalar metrics must
            # return an actual zero, not NULL, for a missing selection.
            assert data['value'] == (None if rows == 0 else value), (line, value)
            if panel in (3, 6, 7):
                assert data['total_requests'] == (None if rows == 0 else requests), (line, requests)
            checks.add(identity)
        assert checks == {(panel, user, key) for panel in range(2, 8)
                          for user in states for key in states}, checks
        print('FILTER_RESULTS PASS 54/54: requests All/user1/key11=4/3/2; '
              'active users=2/1/1; tokens=11110/7777/5555; cost=10/7/5; '
              'user rows=2/1/1; key rows=3/2/1; no-match scalar=0/table=empty')
        print('AC-4 SQL/role/filter PASS; browser/Grafana live switching still NOT EXECUTED')
        return 0
    finally:
        # Even a timed-out/lost `docker run` response may have created a container.
        cleanup_container(name, owner)


class ContainerLifecycleTests(unittest.TestCase):
    def inspected(self, owner):
        data = [{'Id': 'owned-container-id', 'Config': {'Labels': {OWNER_LABEL: owner}}}]
        return subprocess.CompletedProcess([], 0, json.dumps(data), '')

    def test_run_timeout_after_create_cleans_up_owned_id(self):
        timeout = subprocess.TimeoutExpired(['docker', 'run'], 60)
        removed = subprocess.CompletedProcess([], 0, '', '')
        with mock.patch(__name__ + '.secrets.token_hex', return_value='owned-token'), \
             mock.patch(__name__ + '.docker', side_effect=[timeout, self.inspected('owned-token'), removed]) as command:
            with self.assertRaises(subprocess.TimeoutExpired):
                main()
        run_args = command.call_args_list[0].args
        self.assertEqual(run_args[0], 'run')
        self.assertIn(OWNER_LABEL + '=owned-token', run_args)
        self.assertEqual(command.call_args_list[1:], [
            mock.call('inspect', 'sub2api-ac4-owned-token', check=False),
            mock.call('rm', '-f', 'owned-container-id', check=False)])

    def test_unknown_owner_never_removed(self):
        for owner in [None, 'someone-else']:
            with self.subTest(owner=owner), \
                 mock.patch(__name__ + '.docker', return_value=self.inspected(owner)) as command:
                with self.assertRaisesRegex(RuntimeError, 'Refusing cleanup of unowned container'):
                    cleanup_container('test-name', 'owned-token')
                command.assert_called_once_with('inspect', 'test-name', check=False)

    def test_absent_container_safe(self):
        absent = subprocess.CompletedProcess([], 1, '[]', 'Error: No such object: test-name\n')
        with mock.patch(__name__ + '.docker', return_value=absent) as command:
            cleanup_container('test-name', 'owned-token')
            command.assert_called_once_with('inspect', 'test-name', check=False)

    def test_other_inspect_error_fails_loudly(self):
        error = subprocess.CompletedProcess([], 1, '', 'Cannot connect to the Docker daemon')
        with mock.patch(__name__ + '.docker', return_value=error) as command:
            with self.assertRaisesRegex(RuntimeError, 'Cannot inspect disposable PG for cleanup'):
                cleanup_container('test-name', 'owned-token')
            command.assert_called_once_with('inspect', 'test-name', check=False)


if __name__ == '__main__':
    if sys.argv[1:] == ['--lifecycle-tests']:
        unittest.main(argv=[sys.argv[0]], verbosity=2)
    else:
        raise SystemExit(main())
