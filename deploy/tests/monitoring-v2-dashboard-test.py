#!/usr/bin/env python3
"""Validate the 3 canonical pure-Prometheus V2 dashboards and run PromQL regression cases."""
import argparse
import json
import re
import runpy
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
ALLOWED_TYPES = {'stat', 'bargauge', 'barchart', 'table', 'text'}
FORBIDDEN_TYPES = {'timeseries', 'graph', 'heatmap'}
MAIN_UIDS = [
    'sub2api-v2-overview',
    'sub2api-v2-nodes-chain',
    'sub2api-v2-host',
]
ADMIN_REALTIME_URL = '${admin_url}/admin/ops#realtime-requests'
EXPECTED = {
    'overview': dict(file='01-overview.json', uid='sub2api-v2-overview', count=14,
                     refresh='30s', time_from='now-15m', variables=['admin_url']),
    'nodes-chain': dict(file='02-nodes-chain.json', uid='sub2api-v2-nodes-chain', count=11,
                        refresh='30s', time_from='now-15m', variables=['admin_url']),
    'host': dict(file='03-host.json', uid='sub2api-v2-host', count=23,
                 refresh='30s', time_from='now-1h', variables=['admin_url']),
}


def promql(expr):
    return (expr.replace('$__range', '15m')
            .replace('$__rate_interval', '5m')
            .replace('$__interval', '1m'))


specs = ns['DASHBOARDS']
assert isinstance(specs, list) and len(specs) == 3, specs
assert [s['id'] for s in specs] == list(EXPECTED), [s['id'] for s in specs]
# The dashboards directory must contain exactly the three canonical V2 JSON files.
dash_files = sorted(p.name for p in (GEN.parent / 'dashboards').glob('0*.json'))
assert dash_files == [e['file'] for e in EXPECTED.values()], dash_files
query_text = json.dumps(ns['QUERIES'], ensure_ascii=False)
assert not re.search(r'\bor\s+vector\(1\)', query_text)
assert 'vector(1)' not in query_text
assert 'sub2api-pg' not in query_text
assert 'rawSql' not in query_text

prom_exprs = []
by_dash = {}
for spec in specs:
    expected = EXPECTED[spec['id']]
    path = GEN.parent / 'dashboards' / expected['file']
    raw = path.read_text(encoding='utf-8')
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
    assert 'sub2api-pg' not in raw
    assert '"rawSql"' not in raw
    assert '"type": "postgres"' not in raw
    assert [v['name'] for v in data['templating']['list']] == expected['variables']
    assert data['links'] == [], (data['uid'], data['links'])

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
            'ov_fail': '无失败', 'ov_cov': '未采集', 'ov_coverage': '未采集',
            'nc_switch': '无流量', 'nc_aborts': '无中断',
        }.get(pid)
        if no_value is not None:
            assert panel['fieldConfig']['defaults']['noValue'] == no_value, pid
        if pid == 'nc_switch':
            assert '[5m]' in panel['targets'][0]['expr']
            assert '5m' in panel['title']
        assert panel['datasource'] == {'type': 'prometheus', 'uid': PROM_UID}
        assert panel['interval'] == '15s'
        if panel['type'] == 'text':
            assert not panel.get('targets')
            assert panel.get('options', {}).get('content')
            continue
        queries = ns['QUERIES'][pid]
        assert len(panel.get('targets', [])) == len(queries), (data['uid'], pid)
        for query, target in zip(queries, panel['targets']):
            assert target['datasource'] == {'type': 'prometheus', 'uid': PROM_UID}
            expected_expr = (ns['with_no_data'](query['expr'])
                             if query.get('fallback') else query['expr'])
            assert target['expr'] == expected_expr, (data['uid'], pid)
            assert target['range'] is False and target['instant'] is True
            assert target.get('legendFormat') == query.get('legend', '')
            prom_exprs.append(target['expr'])

    by_dash[spec['id']] = by_pid
    assert not (set(types) & FORBIDDEN_TYPES), (data['uid'], set(types))
    for forbidden in FORBIDDEN_TYPES:
        assert f'"type": "{forbidden}"' not in raw, (data['uid'], forbidden)

    # Text-panel navigation links all three dashboards + Admin Ops realtime stream.
    nav_panels = [panel for src, panel in zip(spec['panels'], data['panels'])
                  if src['id'].endswith('_nav')]
    assert len(nav_panels) == 1, data['uid']
    nav = nav_panels[0]['options']['content']
    nav_urls = re.findall(r'<a\b[^>]*\bhref="([^"]+)"', nav)
    expected_nav = ['/d/' + uid for uid in MAIN_UIDS] + [ADMIN_REALTIME_URL]
    assert nav_urls == expected_nav, (data['uid'], nav_urls)
    assert 'target="_blank"' in nav and '实时请求流（管理后台）' in nav

    print(f'PASS: {data["uid"]} panels={len(data["panels"])} refresh={data["refresh"]} '
          f'time={data["time"]["from"]} pure-Prometheus/no-line-charts/nav')

# 01 overview assertions
ov = by_dash['overview']
assert 'sub2api_gateway_request_duration_seconds_bucket' in ov['ov_p95_total']['targets'][0]['expr']
assert 'sub2api_request_phase_seconds_sum{phase!="headers_end"}' in ov['ov_share']['targets'][0]['expr']
assert 'sub2api_request_phase_seconds_count{phase!="headers_end"}' in ov['ov_share']['targets'][0]['expr']
assert ov['ov_share']['type'] == 'bargauge'
for pid in ['ov_p95_total', 'ov_ttft', 'ov_in', 'ov_out', 'ov_amp']:
    expr = ov[pid]['targets'][0]['expr']
    assert '[$__range]' in expr, (pid, expr)
    assert '[5m]' not in expr and '[10m]' not in expr, (pid, expr)
for pid in ['ov_cov', 'ov_coverage']:
    assert ov[pid]['fieldConfig']['defaults']['noValue'] == '未采集'
    mappings = ov[pid]['fieldConfig']['defaults']['mappings']
    assert any(m.get('type') == 'value' and
               m.get('options', {}).get('-1') == {'text': '无样本', 'color': 'gray'}
               for m in mappings), (pid, mappings)
assert len(ov['ov_acct']['targets']) == 3
assert ov['ov_acct']['transformations'][0] == {
    'id': 'joinByField', 'options': {'byField': 'account', 'mode': 'outer'}}
assert len(ov['ov_abort_retry']['targets']) == 2
print('PASS: 01-overview pure-Prometheus KPI/phase/coverage/account/abort-retry assertions')

# 02 nodes & chain assertions
nc = by_dash['nodes-chain']
assert 'sub2api_app_accounts_active' in nc['nc_online']['targets'][0]['expr']
assert 'sub2api_app_accounts_concurrency_capacity' in nc['nc_capacity']['targets'][0]['expr']
assert len(nc['nc_accounts']['targets']) == 4
assert nc['nc_accounts']['transformations'][0] == {
    'id': 'joinByField', 'options': {'byField': 'account', 'mode': 'outer'}}
assert len(nc['nc_models']['targets']) == 2
assert nc['nc_models']['transformations'][0] == {
    'id': 'joinByField', 'options': {'byField': 'model_class', 'mode': 'outer'}}
assert 'sub2api_request_phase_seconds_bucket{phase!="headers_end"}' in nc['nc_phase_p95']['targets'][0]['expr']
assert 'sub2api_transfer_bytes_total' in nc['nc_transfer']['targets'][0]['expr']
assert 'sub2api_request_body_bytes_bucket' in nc['nc_body_p95']['targets'][0]['expr']
assert 'sub2api_client_abort_total' in nc['nc_aborts']['targets'][0]['expr']
print('PASS: 02-nodes-chain pure-Prometheus account/model/chain/bytes assertions')

# 03 host & app assertions
hs = by_dash['host']
assert 'sub2api_app_db_connections' in hs['hs_db']['targets'][0]['expr']
assert 'sub2api_app_concurrency_queue_depth' in hs['hs_queue']['targets'][0]['expr']
assert 'sub2api_app_redis_up' in hs['hs_redis']['targets'][0]['expr']
assert 'sub2api_http_requests_total' in hs['hs_qps']['targets'][0]['expr']
load_expr = hs['hs_load']['targets'][0]['expr']
assert load_expr.startswith('node_load5 / ')
assert 'count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})' in load_expr
appinfo = hs['hs_appinfo']
assert len(appinfo['targets']) == 3
assert appinfo['transformations'] == [
    {'id': 'joinByField', 'options': {'byField': 'instance', 'mode': 'outer'}}]
print('PASS: 03-host pure-Prometheus app gauges, host load, and appinfo join')


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
