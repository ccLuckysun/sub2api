#!/usr/bin/env python3
"""Validate the redesigned V2 dashboards and run PromQL regression cases."""
import argparse
import json
import re
import runpy
import sqlite3
import subprocess
import tempfile
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--static-only', action='store_true',
                    help='Run dashboard assertions without Docker/PromQL execution')
args = parser.parse_args()

ROOT = Path(__file__).resolve().parents[2]
GEN = ROOT / 'deploy/monitoring/grafana/build-dashboards.py'
ns = runpy.run_path(str(GEN))
subprocess.run(['python3', str(GEN), '--check'], check=True)

PROM_UID = 'sub2api-prometheus'
PG_UID = 'sub2api-pg'
ALLOWED_TYPES = {'stat', 'bargauge', 'barchart', 'table', 'text'}
FORBIDDEN_TYPES = {'timeseries', 'graph', 'heatmap'}
MAIN_UIDS = [
    'sub2api-v2-overview', 'sub2api-v2-requests', 'sub2api-v2-nodes',
    'sub2api-v2-users', 'sub2api-v2-host',
]
EXPECTED = {
    'overview': dict(file='01-overview.json', uid='sub2api-v2-overview', count=14,
                     refresh='30s', time_from='now-15m', variables=[]),
    'requests': dict(file='02-requests.json', uid='sub2api-v2-requests', count=7,
                     refresh='10s', time_from='now-15m',
                     variables=['user', 'api_key', 'result', 'model', 'search']),
    'nodes': dict(file='03-nodes.json', uid='sub2api-v2-nodes', count=8,
                  refresh='30s', time_from='now-15m', variables=[]),
    'users': dict(file='04-users.json', uid='sub2api-v2-users', count=7,
                  refresh='1m', time_from='now-24h', variables=['user', 'api_key']),
    'host': dict(file='05-host.json', uid='sub2api-v2-host', count=23,
                 refresh='30s', time_from='now-1h', variables=[]),
    'request': dict(file='06-request.json', uid='sub2api-v2-request', count=10,
                    refresh='30s', time_from='now-1h', variables=['sample_id']),
}


def norm_sql(sql):
    return re.sub(r'\s+', ' ', sql).strip()


def override_prop(panel, field, prop_id):
    for override in panel.get('fieldConfig', {}).get('overrides', []):
        if override.get('matcher') == {'id': 'byName', 'options': field}:
            for prop in override.get('properties', []):
                if prop.get('id') == prop_id:
                    return prop.get('value')
    raise AssertionError(f'{panel["title"]}: missing override {field}/{prop_id}')


def promql(expr):
    return (expr.replace('$__range', '15m')
            .replace('$__rate_interval', '5m')
            .replace('$__interval', '1m'))


specs = ns['DASHBOARDS']
assert isinstance(specs, list) and len(specs) == 6, specs
assert [s['id'] for s in specs] == list(EXPECTED), [s['id'] for s in specs]
# The dashboards directory must contain exactly the six current V2 JSON files;
# a leftover 02-chain.json would otherwise be silently provisioned as well.
dash_files = sorted(p.name for p in (GEN.parent / 'dashboards').glob('0*.json'))
assert dash_files == [e['file'] for e in EXPECTED.values()], dash_files
query_text = json.dumps(ns['QUERIES'], ensure_ascii=False)
assert not re.search(r'\bor\s+vector\(1\)', query_text)
assert 'vector(1)' not in query_text

prom_exprs = []
for spec in specs:
    expected = EXPECTED[spec['id']]
    path = GEN.parent / 'dashboards' / expected['file']
    raw = path.read_text()
    data = json.loads(raw)
    assert data['uid'] == expected['uid'] == spec['uid']
    assert data['title'] == spec['title']
    assert len(data['panels']) == expected['count']
    assert len(data['panels']) == len(spec['panels'])
    assert data['refresh'] == expected['refresh'] == spec['refresh']
    assert data['time'] == {'from': expected['time_from'], 'to': 'now'}
    assert data['timezone'] == 'browser'
    assert data['editable'] is False and data['allowUiUpdates'] is False
    assert {'sub2api', 'v2'} <= set(data['tags'])
    assert 'or vector(1)' not in raw
    assert [v['name'] for v in data['templating']['list']] == expected['variables']
    if spec['id'] == 'request':
        assert 'drilldown' in data['tags']

    by_pid = {}
    types = []
    for src, panel in zip(spec['panels'], data['panels']):
        pid = src['id']
        by_pid[pid] = panel
        types.append(panel['type'])
        assert panel['type'] == src['kind']
        assert panel['title'] == src['title']
        assert panel['type'] in ALLOWED_TYPES, (data['uid'], pid, panel['type'])
        assert panel['description'], f'{data["uid"]}/{pid}: empty description'
        no_value = {
            'ov_qps': '无流量', 'ov_p95_total': '无流量', 'ov_ttft': '无流量',
            'ov_in': '无流量', 'ov_out': '无流量', 'ov_amp': '无流量',
            'ov_fail': '无失败', 'ov_anom': '无异常请求',
            'rq_count': '无流量', 'rq_rate': '无流量', 'rq_avg': '无流量',
            'rq_slow': '无流量', 'rq_table': '无请求', 'nd_switch': '无流量',
            'ov_cov': '未采集', 'ov_coverage': '未采集',
        }.get(pid)
        if no_value is not None:
            assert panel['fieldConfig']['defaults']['noValue'] == no_value, pid
        if pid == 'nd_switch':
            assert '[5m]' in panel['targets'][0]['expr']
            assert '5m' in panel['title']
        assert panel['datasource']['uid'] in (PROM_UID, PG_UID)
        if panel['type'] == 'text':
            assert not panel.get('targets')
            assert panel.get('options', {}).get('content')
            continue
        queries = ns['QUERIES'][pid]
        assert len(panel.get('targets', [])) == len(queries), (data['uid'], pid)
        for query, target in zip(queries, panel['targets']):
            assert target['datasource']['uid'] == panel['datasource']['uid']
            if query['kind'] == 'prom':
                assert panel['datasource']['uid'] == PROM_UID
                assert panel['interval'] == '15s'
                expected_expr = (ns['with_no_data'](query['expr'])
                                 if query.get('fallback') else query['expr'])
                assert target['expr'] == expected_expr, (data['uid'], pid)
                assert target['range'] is False and target['instant'] is True
                assert target.get('legendFormat') == query.get('legend', '')
                prom_exprs.append(target['expr'])
            else:
                assert panel['datasource']['uid'] == PG_UID
                assert panel['interval'] == '1m'
                assert target['rawSql'] == query['sql'], (data['uid'], pid)
                assert target.get('rawQuery') is True
                assert target.get('editorMode') == 'code'

    assert not (set(types) & FORBIDDEN_TYPES), (data['uid'], set(types))
    for forbidden in FORBIDDEN_TYPES:
        assert f'"type": "{forbidden}"' not in raw, (data['uid'], forbidden)

    # Text-panel navigation is authoritative: all five module links are real
    # anchors. The drilldown adds an explicit back anchor to requests.
    nav_panels = [panel for src, panel in zip(spec['panels'], data['panels'])
                  if src['id'].endswith('_nav')]
    assert len(nav_panels) == 1, data['uid']
    nav = nav_panels[0]['options']['content']
    nav_urls = re.findall(r'<a\b[^>]*\bhref="([^"]+)"', nav)
    expected_nav = ['/d/' + uid for uid in MAIN_UIDS]
    if spec['id'] == 'request':
        expected_nav.insert(0, '/d/sub2api-v2-requests')
    assert nav_urls == expected_nav, (data['uid'], nav_urls)

    # Native dashboard links must not duplicate the module tabs; only the
    # request drilldown keeps a native back link with time/variables preserved.
    if spec['id'] == 'request':
        assert len(data['links']) == 1, data['links']
        back = data['links'][0]
        assert back['url'] == '/d/sub2api-v2-requests' and back['type'] == 'link'
        assert back['keepTime'] is True and back['includeVars'] is True
        assert back['targetBlank'] is False
    else:
        assert data['links'] == [], (data['uid'], data['links'])

    print(f'PASS: {data["uid"]} panels={len(data["panels"])} refresh={data["refresh"]} '
          f'time={data["time"]["from"]} types/description/datasource/interval')

# 01: phase share is a PostgreSQL-backed percentage barchart.
share = by_pid = None
for spec in specs:
    if spec['id'] == 'overview':
        path = GEN.parent / 'dashboards' / EXPECTED['overview']['file']
        overview = json.loads(path.read_text())
        by_pid = {src['id']: panel
                  for src, panel in zip(spec['panels'], overview['panels'])}
        share = by_pid['ov_share']
assert share['type'] == 'barchart'
assert share['datasource']['uid'] == PG_UID and share['interval'] == '1m'
assert share['fieldConfig']['defaults']['unit'] == 'percentunit'
assert share['options']['stacking'] == 'percent'
share_sql = norm_sql(share['targets'][0]['rawSql'])
for column in ['body_read_ms', 'wait_ms', 'gateway_ms', 'upstream_ms',
               'response_ms', 'finish_ms']:
    assert column in share_sql
assert '$__timeFilter(observed_at)' in share_sql
print('PASS: ov_share barchart/PG/percent with six mutually exclusive phase averages')

# Traffic windows use the selected range; collection coverage keeps its sentinel.
for pid in ['ov_in', 'ov_out', 'ov_amp', 'ov_ttft']:
    expr = by_pid[pid]['targets'][0]['expr']
    assert '[$__range]' in expr, (pid, expr)
    assert '[5m]' not in expr and '[10m]' not in expr, (pid, expr)

# Coverage panels render -1 as grey 无样本, not 0% or success.
for pid in ['ov_cov', 'ov_coverage']:
    assert by_pid[pid]['fieldConfig']['defaults']['noValue'] == '未采集'
    mappings = by_pid[pid]['fieldConfig']['defaults']['mappings']
    assert any(m.get('type') == 'value' and
               m.get('options', {}).get('-1') == {'text': '无样本', 'color': 'gray'}
               for m in mappings), (pid, mappings)
print('PASS: coverage -1 mapping is 无样本/gray')
detail_url = '/d/sub2api-v2-request?var-sample_id=${__value.raw}&from=${__from}&to=${__to}'
anomalies = by_pid['ov_anom']
assert override_prop(anomalies, '详情', 'links')[0]['url'] == detail_url
assert 's.id::text AS "详情"' in anomalies['targets'][0]['rawSql']

# 02: joined request table, ordered latest first, hard limit 200.
for spec in specs:
    if spec['id'] == 'requests':
        path = GEN.parent / 'dashboards' / EXPECTED['requests']['file']
        requests = json.loads(path.read_text())
        by_pid = {src['id']: panel
                  for src, panel in zip(spec['panels'], requests['panels'])}
        break
table = by_pid['rq_table']
sql = table['targets'][0]['rawSql']
compact = norm_sql(sql)
# Key-constrained LATERAL lookup uses a client ID when present. Local IDs are
# eligible only for historical samples without a UUID; late client billing
# must remain NULL rather than attach a stale local row. LIMIT 1 avoids fanout.
usage_join = norm_sql("""
LEFT JOIN LATERAL (
  SELECT ul.upstream_model, ul.inbound_endpoint, ul.upstream_endpoint, ul.stream,
         ul.billing_mode, ul.input_tokens, ul.output_tokens, ul.cache_read_tokens,
         ul.cache_creation_tokens, ul.actual_cost, ul.ip_address, ul.user_agent
  FROM usage_logs ul
  WHERE ul.api_key_id = s.api_key_id
    AND (ul.request_id = 'client:' || s.client_request_id
         OR (NULLIF(s.client_request_id, '') IS NULL
             AND ul.request_id = 'local:' || s.request_id))
  ORDER BY (ul.request_id = 'client:' || s.client_request_id) DESC,
           ul.created_at DESC, ul.id DESC
  LIMIT 1
) ul ON TRUE
""")
assert compact.count(usage_join) == 1, compact
assert ("OR (NULLIF(s.client_request_id, '') IS NULL AND "
        "ul.request_id = 'local:' || s.request_id))") in compact
assert 'LEFT JOIN usage_logs ul ON' not in compact
for join in [
    'LEFT JOIN users u ON u.id = s.user_id',
    'LEFT JOIN grafana_api_keys k ON k.id = s.api_key_id',
    'LEFT JOIN accounts a ON a.id = s.account_id',
    'LEFT JOIN groups g ON g.id = s.group_id',
]:
    assert join in compact, join
assert 's.id::text AS "详情"' in compact and 'k.masked_key' in compact
assert re.search(r'ORDER BY s\.observed_at DESC, s\.id DESC LIMIT 200', compact)
for token in ['$__timeFilter(s.observed_at)', '$user', '$api_key', '${result:sqlstring}',
              '${model:sqlstring}', '${search:sqlstring}']:
    assert token in sql, token
# A normal query option, not custom includeAll/allValue, ensures sqlstring
# formats the sentinel too. Grafana's custom All bypasses the formatter, while
# its default PostgreSQL formatter auto-quotes includeAll concrete values.
model_variable = next(v for v in requests['templating']['list'] if v['name'] == 'model')
assert model_variable.get('includeAll') is False, model_variable
assert model_variable.get('multi') is False, model_variable
assert model_variable.get('allValue') in (None, ''), model_variable
assert model_variable['current']['value'] == '__all__', model_variable
assert model_variable['current']['text'] == '全部', model_variable
request_sql = [target['rawSql'] for panel in requests['panels']
               for target in panel.get('targets', []) if 'rawSql' in target]
assert len(request_sql) == 5
model_filter = "(${model:sqlstring} = '__all__' OR s.model = ${model:sqlstring})"
for query in request_sql:
    assert model_filter in norm_sql(query), query
    assert "'${model}'" not in query, query
    assert '($user = -1 OR s.user_id = $user)' in norm_sql(query), query
    assert '($api_key = -1 OR s.api_key_id = $api_key)' in norm_sql(query), query
# Exercise the SQL literals promised by :sqlstring, including apostrophes and
# SQL-like model names. This is not a substitute for live Grafana interpolation.
with sqlite3.connect(':memory:') as connection:
    connection.execute('CREATE TABLE ops_request_transfer_samples (model TEXT)')
    values = ['model-a', 'model-b', "vendor\'s-model", "x\' OR 1=1 --"]
    connection.executemany('INSERT INTO ops_request_transfer_samples VALUES (?)',
                           [(value,) for value in values])
    for selected, count in [('__all__', 4), ('model-a', 1), ('missing', 0),
                            ("vendor\'s-model", 1), ("x\' OR 1=1 --", 1)]:
        literal = "'" + selected.replace("'", "''") + "'"
        predicate = model_filter.replace('${model:sqlstring}', literal)
        actual = connection.execute('SELECT count(*) FROM ops_request_transfer_samples s WHERE ' + predicate).fetchone()[0]
        assert actual == count, (selected, actual)
    model_query = model_variable['query']
    if isinstance(model_query, dict):
        model_query = model_query['query']
    assert '$__timeFilter(observed_at)' in model_query, model_query
    model_query = model_query.replace('$__timeFilter(observed_at)', '1=1')
    for empty in (False, True):
        if empty:
            connection.execute('DELETE FROM ops_request_transfer_samples')
        cursor = connection.execute(model_query)
        columns = [column[0] for column in cursor.description]
        options = [dict(zip(columns, row)) for row in cursor.fetchall()]
        sentinel = {'__text': '全部', '__value': '__all__'}
        assert options[0] == sentinel, options
        assert options.count(sentinel) == 1, options
        assert len(options) == (1 if empty else 5), options
assert table['options']['sortBy'] == [{'displayName': '时间', 'desc': True}]
links = override_prop(table, '详情', 'links')
assert override_prop(table, '详情', 'custom.cellOptions') == {'type': 'data-links'}
assert links[0]['url'] == detail_url
assert by_pid['rq_legend']['options']['content'].count('span') >= 6
print('PASS: 02 table joins five relations, ORDER BY observed_at DESC LIMIT 200, '
      'five variables and detail data-link')

# 06: request detail page has the six-phase table, bytes and token sections.
for spec in specs:
    if spec['id'] == 'request':
        path = GEN.parent / 'dashboards' / EXPECTED['request']['file']
        detail = json.loads(path.read_text())
        by_pid = {src['id']: panel
                  for src, panel in zip(spec['panels'], detail['panels'])}
        break
for pid in ['dt_basic', 'dt_tokens']:
    detail_sql = norm_sql(by_pid[pid]['targets'][0]['rawSql'])
    assert detail_sql.count(usage_join) == 1, pid
    assert 'LEFT JOIN usage_logs ul ON' not in detail_sql, pid
phase_sql = norm_sql(by_pid['dt_phase']['targets'][0]['rawSql'])
for column in ['body_read_ms', 'wait_ms', 'gateway_ms', 'upstream_ms',
               'response_ms', 'finish_ms']:
    assert column in phase_sql, column
for label in ['读取请求体', '槽位等待', '网关本地', '上游耗时', '响应传输', '收尾']:
    assert label in phase_sql, label
bytes_sql = norm_sql(by_pid['dt_bytes']['targets'][0]['rawSql'])
for column in ['bytes_in_wire', 'bytes_in_decoded', 'bytes_up_req_total',
               'bytes_resp_upstream', 'bytes_resp_downstream']:
    assert column in bytes_sql, column
tokens_sql = norm_sql(by_pid['dt_tokens']['targets'][0]['rawSql'])
for column in ['input_tokens', 'output_tokens', 'cache_read_tokens',
               'cache_creation_tokens', 'actual_cost']:
    assert column in tokens_sql, column
for panel in by_pid.values():
    for target in panel.get('targets', []):
        query = norm_sql(target['rawSql'])
        assert 'WHERE s.id = CASE WHEN ${sample_id:sqlstring}' in query
        assert 'request_id = ${' not in query and '$__timeFilter(s.observed_at)' in query
assert 's.request_id AS "请求 ID"' in by_pid['dt_basic']['targets'][0]['rawSql']
print('PASS: 06 six-phase/bytes/token detail panels use the unique sample ID')

# Host load divides by the cpu count without the mode label; app info joins
# three Prometheus frames by instance.
for spec in specs:
    if spec['id'] == 'host':
        path = GEN.parent / 'dashboards' / EXPECTED['host']['file']
        host = json.loads(path.read_text())
        by_pid = {src['id']: panel
                  for src, panel in zip(spec['panels'], host['panels'])}
        break
load_expr = by_pid['hs_load']['targets'][0]['expr']
assert load_expr.startswith('node_load5 / ')
assert 'count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})' in load_expr
appinfo = by_pid['hs_appinfo']
assert len(appinfo['targets']) == 3
assert appinfo['transformations'] == [
    {'id': 'joinByField', 'options': {'byField': 'instance', 'mode': 'outer'}}]
print('PASS: host load count-without(cpu, mode); app info joinByField(instance)')


def case(name, inputs, checks):
    return dict(
        name=name,
        interval='1m',
        input_series=inputs,
        promql_expr_test=[
            dict(expr=expr, eval_time='10m',
                 exp_samples=[dict(labels=labels, value=value)
                              for labels, value in expected])
            for expr, expected in checks
        ],
    )


tests = []
fallback = ns['with_no_data']('w5_dashboard_fixture')
for name, values, expected in [
    ('labelled data', [('a', 7), ('b', 2)],
     [('{__name__="w5_dashboard_fixture",account="a"}', 7),
      ('{__name__="w5_dashboard_fixture",account="b"}', 2)]),
    ('real zero', [('a', 0)],
     [('{__name__="w5_dashboard_fixture",account="a"}', 0)]),
    ('unlabelled real zero', [(None, 0)],
     [('{__name__="w5_dashboard_fixture"}', 0)]),
    ('entirely missing', [],
     [('{collection="未采集"}', 0)]),
]:
    inputs = [
        dict(series='w5_dashboard_fixture' +
             ('' if account is None else '{account="' + account + '"}'),
             values=str(value) + '+0x20')
        for account, value in values
    ]
    tests.append(case('no data fallback ' + name, inputs, [(fallback, expected)]))

load = ns['QUERIES']['hs_load'][0]['expr']
tests.append(case('host load real exporter labels', [
    dict(series='node_load5{instance="a",job="node"}', values='6+0x20'),
    dict(series='node_cpu_seconds_total{instance="a",job="node",mode="idle",cpu="0"}',
         values='0+10x20'),
    dict(series='node_cpu_seconds_total{instance="a",job="node",mode="idle",cpu="1"}',
         values='0+10x20'),
], [
    (load.replace('without(cpu, mode)', 'without(cpu)'), []),
    (load, [('{instance="a",job="node"}', 3)]),
]))

coverage = ns['QUERIES']['ov_coverage'][0]['expr']
for value in [0.96, 0.85, 0, -1]:
    tests.append(case('coverage raw ratio ' + str(value), [
        dict(series='sub2api_metric_coverage_ratio{kind="account"}',
             values=str(value) + '+0x20'),
    ], [
        (coverage, [('{__name__="sub2api_metric_coverage_ratio",kind="account"}', value)]),
    ]))

queries = [promql(expr) for expr in prom_exprs]
assert all('vector(1)' not in expr for expr in queries)
if args.static_only:
    print(f'PASS: dashboard static regressions; {len(queries)} PromQL expressions prepared')
    print('SKIP: PromQL parsing/regression execution (--static-only; requires Docker)')
    raise SystemExit(0)
with tempfile.TemporaryDirectory(prefix='w5-dashboard-promql-') as tmp:
    directory = Path(tmp)
    (directory / 'queries.yml').write_text(json.dumps({
        'groups': [{'name': 'dashboard-queries',
                    'rules': [dict(record='w5_panel_' + str(i), expr=q)
                              for i, q in enumerate(queries)]}]
    }))
    (directory / 'regression_test.yml').write_text(json.dumps(
        dict(rule_files=[], evaluation_interval='1m', tests=tests)))
    base = ['docker', 'run', '--rm', '--network', 'none',
            '-v', str(directory) + ':/m:ro', '--entrypoint', 'promtool',
            'prom/prometheus:v3.16.0']
    subprocess.run(base + ['check', 'rules', '/m/queries.yml'], check=True)
    subprocess.run(base + ['test', 'rules', '/m/regression_test.yml'], check=True)
print(f'PASS: {len(queries)} generated PromQL parsed; '
      f'{len(tests)} PromQL regression cases passed')
