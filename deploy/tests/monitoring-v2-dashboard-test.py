#!/usr/bin/env python3
"""Validate generated V2 panels and execute PromQL regression cases in promtool."""
import json
from pathlib import Path
import re
import runpy
import subprocess
import tempfile

ROOT=Path(__file__).resolve().parents[2]
GEN=ROOT/'deploy/monitoring/grafana/build-dashboards.py'
ns=runpy.run_path(str(GEN))
subprocess.run(['python3',str(GEN),'--check'],check=True)
panels={}
queries=[]
for spec,count in zip(ns['LAYOUT'],[16,19,9,6,15]):
    path=GEN.parent/'dashboards'/(spec['num']+'-'+spec['id']+'.json')
    d=json.loads(path.read_text())
    assert d['uid']=='sub2api-v2-'+spec['id'] and len(d['panels'])==count
    assert d['refresh']==('10s' if spec['id']=='chain' else '30s')
    assert d['timezone']=='browser' and d['time']=={'from':'now-6h','to':'now'}
    assert not d['editable'] and not d['allowUiUpdates']
    assert not re.search(r'or\s+vector\(1\)',path.read_text())
    for src,panel in zip(spec['panels'],d['panels']):
        key=src['id'];panels[key]=panel
        assert panel['description'] and panel['datasource']['uid'] in ('sub2api-pg','sub2api-prometheus')
        if panel['datasource']['uid']=='sub2api-pg':assert panel['interval']=='1m'
        for index,target in enumerate(panel['targets']):
            if 'expr' not in target:continue
            assert target['expr']==ns['with_no_data'](ns['QUERIES'][key][index])
            queries.append(target['expr'].replace('$__rate_interval','5m').replace('$__interval','1m'))
    print(f"PASS: {d['uid']} panels={count} refresh={d['refresh']} description/datasource/interval")
assert 'count without(cpu, mode)' in panels['h6']['targets'][0]['expr']
assert 'le="262144"' in panels['c14']['targets'][0]['expr'] and '102400' not in panels['c14']['targets'][0]['expr']
assert '256 KiB' in panels['c14']['title'] and '262144' in panels['c14']['description']
for key in ['o15','c12']:
    defaults=panels[key]['fieldConfig']['defaults']
    assert defaults['unit']=='percentunit' and defaults['min']==0 and defaults['max']==1
    mapping=defaults['mappings'][0]['options']
    for value,text,color in [(0.96,'96%','green'),(0.85,'85%','yellow'),(0,'0%','yellow'),(-1,'无样本','gray')]:
        if str(value) in mapping:
            actual=mapping[str(value)];assert actual['text']==text and actual['color']==color
        else:
            steps=defaults['thresholds']['steps'];active=steps[0]['color']
            for step in steps[1:]:
                if value>=step['value']:active=step['color']
            assert f'{value*100:g}%'==text and active==color
    print(f'PASS: {key} coverage 0.96=96% green / 0.85=85% yellow / 0=0% yellow / -1=无样本 gray')


def case(name,inputs,checks):
    return dict(name=name,interval='1m',input_series=inputs,promql_expr_test=[dict(expr=expr,eval_time='10m',exp_samples=[dict(labels=labels,value=value) for labels,value in expected]) for expr,expected in checks])


fallback=ns['with_no_data']('w5_dashboard_fixture')
tests=[]
for name,values,expected in [
    ('labelled data',[('a',7),('b',2)],[('{__name__="w5_dashboard_fixture",account="a"}',7),('{__name__="w5_dashboard_fixture",account="b"}',2)]),
    ('real zero',[('a',0)],[('{__name__="w5_dashboard_fixture",account="a"}',0)]),
    ('unlabelled real zero',[(None,0)],[('{__name__="w5_dashboard_fixture"}',0)]),
    ('entirely missing',[],[('{collection="未采集"}',0)]),
]:
    inputs=[dict(series='w5_dashboard_fixture'+('' if account is None else '{account="'+account+'"}'),values=str(value)+'+0x20') for account,value in values]
    tests.append(case('no data fallback '+name,inputs,[(fallback,expected)]))
load=ns['QUERIES']['h6'][0]
tests.append(case('host load real exporter labels',[
    dict(series='node_load5{instance="a",job="node"}',values='6+0x20'),
    dict(series='node_cpu_seconds_total{instance="a",job="node",mode="idle",cpu="0"}',values='0+10x20'),
    dict(series='node_cpu_seconds_total{instance="a",job="node",mode="idle",cpu="1"}',values='0+10x20'),
],[(load.replace('without(cpu, mode)','without(cpu)'),[]),(panels['h6']['targets'][0]['expr'],[('{instance="a",job="node"}',3)])]))
for value in [0.96,0.85,0,-1]:
    tests.append(case('coverage raw ratio '+str(value),[dict(series='sub2api_metric_coverage_ratio{kind="account"}',values=str(value)+'+0x20')],[(panels['o15']['targets'][0]['expr'],[('{__name__="sub2api_metric_coverage_ratio",kind="account"}',value)])]))
for name,count,bucket,expected in [('large body existing boundary',100,80,20),('large body idle',0,0,0)]:
    tests.append(case(name,[dict(series='sub2api_request_body_bytes_count{endpoint_class="chat"}',values=f'0+{count}x20'),dict(series='sub2api_request_body_bytes_bucket{endpoint_class="chat",le="262144"}',values=f'0+{bucket}x20')],[("round(("+panels['c14']['targets'][0]['expr']+"),0.000001)",[('{}',expected)])]))
with tempfile.TemporaryDirectory(prefix='w5-dashboard-promql-') as tmp:
    directory=Path(tmp)
    (directory/'queries.yml').write_text(json.dumps({'groups':[{'name':'dashboard-queries','rules':[dict(record='w5_panel_'+str(i),expr=q) for i,q in enumerate(queries)]}]}))
    (directory/'regression_test.yml').write_text(json.dumps(dict(rule_files=[],evaluation_interval='1m',tests=tests)))
    base=['docker','run','--rm','--network','none','-v',str(directory)+':/m:ro','--entrypoint','promtool','prom/prometheus:v3.16.0']
    subprocess.run(base+['check','rules','/m/queries.yml'],check=True)
    subprocess.run(base+['test','rules','/m/regression_test.yml'],check=True)
print(f'PASS: {len(queries)} generated PromQL parsed; {len(tests)} PromQL regression cases passed')
