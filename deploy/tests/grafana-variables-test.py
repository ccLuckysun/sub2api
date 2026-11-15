#!/usr/bin/env python3
"""AC-4: inspect provisioned dashboards and SELECT against a local Docker PG.

Never creates fixtures, roles or panels; never prints emails, key names or keys.
Without grafana_ro, use the local container role with both session and explicit
transaction read-only guards. SQL timings include docker exec/psql overhead;
Grafana interpolation is modeled, not a browser/UI timing acceptance test.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
USER_SQL = ('SELECT id AS __value, COALESCE(email, id::text) AS __text '
            'FROM users ORDER BY 2 LIMIT 1000')
KEY_SQL = ('SELECT id AS __value, name AS __text FROM api_keys '
           'WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000')


def panels(items):
    for panel in items:
        yield panel
        yield from panels(panel.get('panels', []))


def compact(text):
    return re.sub(r'\s+', '', text).lower()


EXPECTED_UIDS = {
    'sub2api-v2-overview', 'sub2api-v2-requests', 'sub2api-v2-nodes',
    'sub2api-v2-users', 'sub2api-v2-host', 'sub2api-v2-request',
}
PG_DS = {'type': 'postgres', 'uid': 'sub2api-pg'}


def assert_numeric_variable(v, name, sql):
    assert v['type'] == 'query' and v['multi'] is False
    assert v['includeAll'] is True and v['allValue'] == '-1'
    assert v['datasource'] == PG_DS
    assert v['query'] == v['definition'] == sql
    assert v['refresh'] == 2
    print(f'VARIABLE {name}: numeric query PASS; SQL={sql}')


def validate_dashboards():
    paths = sorted((ROOT / 'deploy/monitoring/grafana/dashboards').glob('0*.json'))
    assert len(paths) == 6, [p.name for p in paths]
    queries = []
    seen = set()
    for path in paths:
        data = json.loads(path.read_text())
        uid = data['uid']
        seen.add(uid)
        variables = {v['name']: v for v in data['templating']['list']}
        if uid == 'sub2api-v2-users':
            assert len(data['panels']) == 7
            assert list(variables) == ['user', 'api_key']
            assert_numeric_variable(variables['user'], 'user', USER_SQL)
            assert_numeric_variable(variables['api_key'], 'api_key', KEY_SQL)
        elif uid == 'sub2api-v2-requests':
            assert len(data['panels']) == 7
            assert list(variables) == ['user', 'api_key', 'result', 'model', 'search']
            assert_numeric_variable(variables['user'], 'user', USER_SQL)
            assert_numeric_variable(variables['api_key'], 'api_key', KEY_SQL)
            result = variables['result']
            assert result['type'] == 'custom' and result['multi'] is False
            assert result['includeAll'] is False
            assert result['query'] == result['definition'] == '全部 : all,成功 : success,失败 : failure'
            model = variables['model']
            assert model['type'] == 'query' and model['multi'] is False
            assert model['includeAll'] is True and model['allValue'] == '__all__'
            assert model['datasource'] == PG_DS
            assert 'SELECT DISTINCT model' in model['query'] and 'LIMIT 500' in model['query']
            search = variables['search']
            assert search['type'] == 'textbox' and search['includeAll'] is False
            assert search['multi'] is False
            print('VARIABLES requests: user/api_key/result/model/search PASS')
        elif uid == 'sub2api-v2-request':
            assert len(data['panels']) == 10
            assert list(variables) == ['request']
            request = variables['request']
            assert request['type'] == 'query' and request['multi'] is False
            assert request['includeAll'] is False
            assert request['datasource'] == PG_DS
            assert 'ORDER BY observed_at DESC' in request['query']
            assert 'LIMIT 1000' in request['query']
            for panel in panels(data['panels']):
                for target in panel.get('targets', []):
                    sql = target.get('rawSql', '')
                    if sql:
                        assert '${request:sqlstring}' in sql, (uid, panel['id'])
            print('VARIABLE request: recent 1000 request ids PASS')
        else:
            assert not variables, (uid, list(variables))

        for panel in panels(data['panels']):
            for target in panel.get('targets', []):
                assert not re.search(r'\$(?:\{)?(?:user|api_key)\b',
                                     target.get('expr', '')), \
                    f'Unexpected user/key filter on {uid}/{panel["id"]}'
                sql = target.get('rawSql', '')
                refs = set(re.findall(r'\$(?:\{)?(user|api_key)\b', sql))
                if uid == 'sub2api-v2-users' and sql:
                    assert panel['datasource']['uid'] == 'sub2api-pg'
                    assert refs == {'user', 'api_key'}
                    for var, col in [('user', 'user_id'), ('api_key', 'api_key_id')]:
                        pattern = (r'\(\$' + var + r'=-1or(?:[a-z_]+\.)?' +
                                   col + r'=\$' + var + r'\)')
                        assert re.search(pattern, compact(sql)), (panel['id'], var)
                        assert not re.search(r"['\"]\$" + var + r"\b", sql)
                    queries.append((panel['id'], panel['title'], sql))
                    print(f'PANEL {panel["id"]}: numeric predicates PASS; SQL={sql}')
                elif uid == 'sub2api-v2-requests' and sql:
                    assert refs <= {'user', 'api_key'}, (panel['id'], refs)
                    assert '$user' in sql and '$api_key' in sql
                else:
                    assert not refs, f'Unexpected user/key filter on {uid}/{panel["id"]}'
    assert seen == EXPECTED_UIDS, seen
    assert len(queries) == 6, len(queries)
    print('STATIC PASS: six dashboards; 04 has 7 panels and six SQL panels with '
          'numeric user/key predicates; 02 variables and request SQL allowed; '
          '06 request variable; no user/key filters on Prom/system panels')
    return queries


class ReadOnlyPG:
    def __init__(self, container):
        self.container = container
        self.role = None
        self.timings = []

    def query(self, label, sql):
        assert sql.lstrip().upper().startswith('SELECT '), sql
        assert ';' not in sql, 'Only one SELECT is accepted'
        shell = ('exec psql -X -qAt -U "${1:-${POSTGRES_USER:-postgres}}" '
                 '-d "${POSTGRES_DB:-postgres}" -v ON_ERROR_STOP=1')
        started = time.monotonic()
        result = subprocess.run(
            ['docker', 'exec', '-i', '-e',
             'PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=14900',
             self.container, 'sh', '-c', shell, 'ac4', self.role or ''],
            input='BEGIN READ ONLY;\n' + sql + ';\nROLLBACK;\n',
            text=True, capture_output=True, timeout=15)
        elapsed = time.monotonic() - started
        self.timings.append((label, elapsed))
        if result.returncode:
            raise RuntimeError(f'{label}: {elapsed*1000:.3f}ms psql exit {result.returncode}: {result.stderr.strip()}')
        assert elapsed < 15, (label, elapsed)
        value = json.loads(result.stdout.strip())
        return value, elapsed


def interpolate(sql, user, key, start, end):
    # SQL is repository-owned; values are integers selected by this test.
    sql = re.sub(r'\$__timeFilter\((\w+)\)',
                 lambda m: f'{m[1]} BETWEEN to_timestamp({start}) AND to_timestamp({end})', sql)
    sql = re.sub(r"\$__timeGroupAlias\((\w+),'5m'\)",
                 lambda m: f'floor(extract(epoch from {m[1]})/300)*300 AS "time"', sql)
    sql = re.sub(r'\$api_key\b', str(key), sql)
    sql = re.sub(r'\$user\b', str(user), sql)
    assert '$' not in sql, f'Unexpanded Grafana macro: {sql}'
    return sql


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', help='Local PostgreSQL Docker container')
    parser.add_argument('--static-only', action='store_true',
                        help='Validate dashboard JSON only; do not run SQL')
    args = parser.parse_args()
    queries = validate_dashboards()
    if args.static_only:
        return 0
    if not args.container:
        parser.error('--container is required unless --static-only is used')
    db = ReadOnlyPG(args.container)
    info, _ = db.query('initial-read-only', "SELECT json_build_object('database', current_database(), "
                       "'role', current_user, 'read_only', current_setting('transaction_read_only'), "
                       "'grafana_ro', EXISTS(SELECT FROM pg_roles WHERE rolname='grafana_ro'))")
    assert info['read_only'] == 'on'
    if info['grafana_ro']:
        db.role = 'grafana_ro'
    effective, _ = db.query('effective-read-only', "SELECT json_build_object('role', current_user, "
                            "'read_only', current_setting('transaction_read_only'), "
                            "'statement_timeout', current_setting('statement_timeout'))")
    assert effective['read_only'] == 'on'
    print('CONNECTION', json.dumps({'container': args.container, **info, 'effective': effective}))
    if not info['grafana_ro']:
        print('LIMITATION: grafana_ro absent; existing local role, session + BEGIN READ ONLY; SELECT only, no ACL provisioning/login claim')
    schema, _ = db.query('column-types', "SELECT coalesce(json_agg(row_to_json(t)), '[]'::json) FROM "
                         "(SELECT table_name,column_name,data_type FROM information_schema.columns "
                         "WHERE table_schema='public' AND ((table_name='users' AND column_name='id') OR "
                         "(table_name='api_keys' AND column_name IN ('id','user_id')) OR "
                         "(table_name IN ('usage_logs','ops_request_transfer_samples') AND "
                         "column_name IN ('user_id','api_key_id'))) ORDER BY 1,2) t")
    assert all(col['data_type'] == 'bigint' for col in schema), schema
    has_samples = any(col['table_name'] == 'ops_request_transfer_samples' for col in schema)
    print('SCHEMA', json.dumps(schema))
    if len(schema) != 7:
        print('LIMITATION: V2 sample table/columns absent; SQL errors below remain failures, no migrations performed')
    sample_exists = ("EXISTS(SELECT FROM ops_request_transfer_samples WHERE user_id=-9223372036854775807 OR api_key_id=-9223372036854775807)"
                     if has_samples else 'false')
    sample_count = '(SELECT count(*) FROM ops_request_transfer_samples)' if has_samples else 'NULL'
    fixture, _ = db.query('existing-ids-and-counts', "SELECT json_build_object("
                          "'user', coalesce((SELECT min(user_id) FROM api_keys),(SELECT min(id) FROM users)), "
                          "'key', (SELECT min(id) FROM api_keys WHERE user_id=(SELECT min(user_id) FROM api_keys)), "
                          "'missing', -9223372036854775807, "
                          "'missing_exists', EXISTS(SELECT FROM users WHERE id=-9223372036854775807) OR "
                          "EXISTS(SELECT FROM api_keys WHERE id=-9223372036854775807 OR user_id=-9223372036854775807) OR "
                          "EXISTS(SELECT FROM usage_logs WHERE user_id=-9223372036854775807 OR api_key_id=-9223372036854775807) OR "
                          + sample_exists + ", "
                          "'users', (SELECT count(*) FROM users), 'keys', (SELECT count(*) FROM api_keys), "
                          "'usage', (SELECT count(*) FROM usage_logs), 'samples', " + sample_count + ", "
                          "'end', ceil(extract(epoch from now()))::bigint)")
    assert fixture['user'] is not None, 'Single-user coverage needs an existing user; no rows inserted'
    has_key = fixture['key'] is not None
    if not has_key:
        print('LIMITATION: api_keys empty; real api_key single selection NOT EXECUTED; numeric no-match input used for SQL legality only')
    assert fixture['missing_exists'] is False
    print('DATA', json.dumps(fixture))
    options, elapsed = db.query('user-options',
        'SELECT json_build_object(\'rows\',count(*),\'numeric_value\',bool_and(pg_typeof(v.__value)::text=\'bigint\'), '
        f"'selected_present',bool_or(v.__value={fixture['user']})) FROM ({USER_SQL}) v")
    assert options['numeric_value'] is True and options['selected_present'] is True
    print(f'QUERY user-options {elapsed*1000:.3f}ms {json.dumps(options)}; labels redacted')
    choices_u = [('All', -1), ('single', fixture['user']), ('no-match', fixture['missing'])]
    choices_k = [('All', -1), ('single' if has_key else 'single-unavailable', fixture['key'] if has_key else fixture['missing'] + 1), ('no-match', fixture['missing'])]
    for state, user in choices_u:
        sql = KEY_SQL.replace('$user', str(user))
        options, elapsed = db.query('key-options/' + state,
            'SELECT json_build_object(\'rows\',count(*),\'numeric_value\',bool_and(pg_typeof(v.__value)::text=\'bigint\')) '
            f'FROM ({sql}) v')
        if state == 'no-match' or not has_key:
            assert options['rows'] == 0
        else:
            assert options['rows'] > 0 and options['numeric_value'] is True
        print(f'QUERY key-options/{state} {elapsed*1000:.3f}ms {json.dumps(options)}; labels redacted')
    start, end = fixture['end'] - 21600, fixture['end']
    tests = 0
    failures = []
    for us, user in choices_u:
        for ks, key in choices_k:
            for panel_id, title, template in queries:
                sql = interpolate(template, user, key, start, end)
                # Preserve result shape and filter fields without returning row contents.
                summary = ("SELECT json_build_object('rows',count(*),'nonempty_rows',"
                           "count(*) FILTER (WHERE v <> '{}'::jsonb),'max_requests',max((v->>'requests')::bigint),"
                           "'total_requests',sum((v->>'requests')::bigint)) "
                           f"FROM (SELECT jsonb_strip_nulls(to_jsonb(q)) AS v FROM ({sql}) q) summary")
                try:
                    got, elapsed = db.query(f'panel-{panel_id}/{us}/{ks}', summary)
                except RuntimeError as error:
                    failures.append(str(error))
                    print('QUERY FAIL', str(error))
                    continue
                if us == 'no-match' or ks == 'no-match':
                    # us_active/us_req/us_tokens/us_cost are scalar aggregates and
                    # still return one row; the two grouped tables return none.
                    if panel_id in {2, 3, 4, 5}:
                        assert got['rows'] == 1, got
                    else:
                        assert got['rows'] == 0, got
                print(f'QUERY panel={panel_id} user={us} api_key={ks} {elapsed*1000:.3f}ms {json.dumps(got)}')
                tests += 1
    status = 'PASS' if not failures and has_key else 'PARTIAL'
    print(f'{status}: variable queries=2 (user/api_key); successful panel queries={tests}/{6*9}; failed={len(failures)}; numeric bigint; read-only; successful calls <15s')
    print(f'TIMING max={max(t for _, t in db.timings)*1000:.3f}ms; calls={len(db.timings)}; includes docker/psql startup')
    digest = hashlib.sha256(''.join(template for _, _, template in queries).encode()).hexdigest()
    print(f'QUERY_TEMPLATE_SHA256 {digest}')
    print(f'AC-4 {status}: static subcheck PASS; SQL failures and missing-data limitations preserved')
    print('NOT EXECUTED by query runner: browser switching/refresh <15s, Grafana plugin interpolation, production-sized DB; role ACL/login requires separate verification')
    return 0 if status == 'PASS' else 2


if __name__ == '__main__':
    raise SystemExit(main())
