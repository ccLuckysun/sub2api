#!/usr/bin/env python3
"""W6c: local V2 image, four isolated 15s scrape targets, raw AC evidence.

Usage: python3 deploy/tests/monitoring-v2-series-budget.py --image LOCAL_IMAGE
Reuses W6b's automatic seed, local-only transport, identity checks and cleanup.
AC-10 is scoped to this fresh single-instance Docker test topology/load, not a
production cardinality bound. AC-1's optional upstream-delay probe uses 10s API
polls, not Grafana/browser timing; full four-fault AC-1 remains unexecuted.
"""
from collections import defaultdict
from datetime import datetime,timezone
import importlib.util
import json
import math
from pathlib import Path
import secrets
import sys
import time
from urllib.parse import urlencode

ROOT=Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('v2_smoke',Path(__file__).with_name('monitoring-v2-deploy-smoke.py'))
smoke=importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def require(condition,message):
    if not condition:raise smoke.SmokeFailure(message)


def family_counts(count_response,metadata_response):
    """Combine all targets using Prometheus TYPE metadata, never name alone.

    Summary quantile series already have the parent name and join its children.
    Reject ambiguous cross-target types/name collisions instead of undercounting.
    Synthetic scrape/recording metrics without metadata retain their full name.
    """
    types={name:{entry['type'] for entry in entries} for name,entries in metadata_response['data'].items()}
    families=defaultdict(int)
    for row in count_response['data']['result']:
        name=row['metric']['__name__'];family=name
        require(len(types.get(name,set()))<=1,'ambiguous cross-target TYPE metadata: '+name)
        for suffix in ('_bucket','_sum','_count','_created'):
            parent=name[:-len(suffix)] if name.endswith(suffix) else ''
            parent_types=types.get(parent,set())
            if parent_types & {'histogram','summary'}:
                require(len(parent_types)==1,'ambiguous parent TYPE metadata: '+parent)
                require(not types.get(name),'cross-target parent/child metric name collision: '+name)
                require(suffix!='_bucket' or parent_types=={'histogram'},'summary cannot own bucket child: '+name)
                family=parent;break
        families[family]+=int(float(row['value'][1]))
    return dict(sorted(families.items(),key=lambda item:(-item[1],item[0])))


def phase_rounding_evidence(phases,total):
    """Six independently truncated millisecond phases can differ by <=5ms."""
    summed=sum(phases.values());error=abs(summed-total)
    require(error<=5,'phase rounding error exceeds 5ms: '+str(error))
    return {'sum_ms':summed,'total_ms':total,'absolute_rounding_error_ms':error,'tolerance_ms':5}


class BudgetProbe:
    def configure(self,config,work,evidence,report):
        jobs={'sub2api':'sub2api:9091','prometheus':'localhost:9090','node':'node-exporter:9100','alertmanager':'alertmanager:9093'}
        prometheus={'global':{'scrape_interval':'15s','scrape_timeout':'10s','evaluation_interval':'15s'},
                    'scrape_configs':[{'job_name':job,'static_configs':[{'targets':[target]}]} for job,target in jobs.items()],
                    'rule_files':['/etc/prometheus/rules/'+name+'.yml' for name in ('slo','chain','infra')],
                    'alerting':{'alertmanagers':[{'static_configs':[{'targets':['alertmanager:9093']}]}]}}
        prom=work/'prometheus.json';prom.write_text(json.dumps(prometheus));prom.chmod(0o644)
        alert=work/'alertmanager.json';alert.write_text(json.dumps({'route':{'receiver':'blackhole'},'receivers':[{'name':'blackhole'}]}));alert.chmod(0o644)
        volumes=[str(prom)+':/etc/prometheus/prometheus.yml:ro','prom:/prometheus']
        volumes += [str(ROOT/'deploy/monitoring/prometheus/rules'/(name+'.yml'))+':/etc/prometheus/rules/'+name+'.yml:ro' for name in ('slo','chain','infra')]
        config['services'].update({
            'prometheus':{'image':'prom/prometheus:v3.16.0','command':['--config.file=/etc/prometheus/prometheus.yml','--storage.tsdb.path=/prometheus','--storage.tsdb.retention.time=2h'],
                          'volumes':volumes},
            'node-exporter':{'image':'prom/node-exporter:v1.12.1'},
            'alertmanager':{'image':'prom/alertmanager:v0.28.1','command':['--config.file=/etc/alertmanager/alertmanager.yml','--storage.path=/alertmanager'],
                            'volumes':[str(alert)+':/etc/alertmanager/alertmanager.yml:ro','alert:/alertmanager']}})
        config['volumes'].update({'prom':{},'alert':{}})
        (evidence/'prometheus-config.json').write_text(json.dumps(prometheus,indent=2)+'\n')
        report['W6c']={'AC-10':{'status':'未执行'},'AC-1':{'status':'未执行',
            'reason':'Full four-fault injection and Grafana first-visible timing are outside this API-only upstream probe.'},
            'scope':'fresh single-instance Docker stack; default node-exporter container/VM view; four scrape jobs; no production host mounts; no host ports',
            'scrape_interval_seconds':15,'jobs':jobs,'rules':['slo.yml','chain.yml','infra.yml']}

    def verify(self,http,sql,report,evidence,api_key):
        info=report['W6c'];sequence=0

        def fetch(path,name):
            nonlocal sequence
            sequence+=1
            started=time.monotonic();status,_,raw=http('http://prometheus:9090'+path);elapsed=time.monotonic()-started
            filename=f'{sequence:03d}-{name}.json'
            (evidence/filename).write_bytes(raw)
            with (evidence/'prometheus-requests.jsonl').open('a') as log:
                log.write(json.dumps({'at':datetime.now(timezone.utc).isoformat(),'path':path,'status':status,'elapsed_seconds':elapsed,'file':filename})+'\n')
            require(status==200,'Prometheus HTTP '+str(status)+'; raw response '+filename)
            data=json.loads(raw)
            require(data.get('status')=='success','Prometheus query failed: '+filename)
            return data,elapsed,filename

        def query(expression,name):return fetch('/api/v1/query?'+urlencode({'query':expression}),name)

        # Successful up=1 samples, not merely elapsed sleep or failed scrapes.
        deadline=time.monotonic()+120
        while True:
            data,_,_=query('sum_over_time(up[2m])','successful-scrapes')
            successful={row['metric']['job']:float(row['value'][1]) for row in data['data']['result']}
            if set(successful)==set(info['jobs']) and all(value>=3 for value in successful.values()):break
            require(time.monotonic()<deadline,'did not observe >=3 successful 15s scrapes per target: '+str(successful))
            time.sleep(3)
        info['successful_scrapes_before_measurement']=successful
        targets,_,_=fetch('/api/v1/targets','targets')
        active=targets['data']['activeTargets']
        require(len(active)==4 and all(target['health']=='up' for target in active),'all four targets must be up')

        def budget(name):
            info['AC-10']['status']='未执行（采集中）'
            head,_,headfile=query('prometheus_tsdb_head_series',name+'-head')
            counts,_,countfile=query('count by (__name__)({__name__=~".+"})',name+'-all-names')
            metadata,_,metadatafile=fetch('/api/v1/metadata',name+'-metadata')
            current,_,tsdbfile=fetch('/api/v1/status/tsdb',name+'-tsdb')
            status,_,raw=http('http://sub2api:9091/metrics');require(status==200,'application metrics unavailable')
            (evidence/(name+'-app-metrics.txt')).write_bytes(raw)
            families=family_counts(counts,metadata)
            require(families,'no families; cannot pass empty evidence')
            sub2api={key:value for key,value in families.items() if key.startswith('sub2api_')}
            require(sub2api,'no sub2api families; cannot pass empty V2 evidence')
            require(len(head['data']['result'])==1,'requires one self-scraped Prometheus head gauge')
            gauge=float(head['data']['result'][0]['value'][1]);actual=current['data']['headStats']['numSeries']
            largest=max(families.values())
            result={'at':datetime.now(timezone.utc).isoformat(),'head_series_gauge':gauge,'head_series_current':actual,
                    'family_scope':'all metric names across all four targets, plus synthetic/recording metrics',
                    'largest_family':next(iter(families)),'largest_family_series':largest,'families':families,
                    'sub2api_largest_family':next(iter(sub2api)),'sub2api_largest_family_series':max(sub2api.values()),
                    'raw_files':[headfile,countfile,metadatafile,tsdbfile]}
            info.setdefault('measurements',[]).append(result)
            require(gauge>0 and actual>0 and max(gauge,actual)<8000,'AC-10 whole-head budget exceeded: '+str(result))
            require(largest<2000,'AC-10 combined family budget exceeded: '+str(result))
            print('AC-10',name,'head_gauge='+str(gauge),'head_current='+str(actual),'largest_family='+result['largest_family']+':'+str(largest),flush=True)
            return result

        budget('before-probe')
        info['AC-10']['status']='PASS (isolated test topology/load only)'
        # Guarantee >=2min without gateway traffic before the experimental request.
        # The previous W6b request initialized histogram buckets for a valid rate
        # baseline; it is outside the 1m query range when this probe starts.
        print('AC-1 upstream probe: starting 120s pre-request quiet window',flush=True)
        quiet_until=time.monotonic()+120
        while time.monotonic()<quiet_until:time.sleep(max(0,min(1,quiet_until-time.monotonic())))
        request_id='v2smoke-delay-3000-'+str(time.time_ns())+'-'+secrets.token_hex(3)
        window=sql('SELECT clock_timestamp();')
        body=json.dumps({'model':smoke.MODEL,'max_tokens':32,'stream':False,'messages':[{'role':'user','content':request_id}]},separators=(',',':')).encode()
        status,headers,raw=http('http://sub2api:8080/v1/messages',body,{'Authorization':'Bearer '+api_key,'Content-Type':'application/json','anthropic-version':'2023-06-01','x-request-id':request_id})
        completed=time.monotonic()
        probe={'status':'未执行','request_id':request_id,'window_start':window,'http_status':status,'completed_at':datetime.now(timezone.utc).isoformat(),
               'clock':'host observation after HTTP completion; docker-exec overhead included in query duration',
               'refresh_seconds':10,'query_budget_seconds':2,'prom_deadline_seconds':30,'injected_upstream_delay_ms':3000,'observations':[]}
        info['upstream_realtime_probe']=probe
        (evidence/'ac1-response.json').write_bytes(raw)
        expression='histogram_quantile(0.95, sum by (le,phase) (rate(sub2api_request_phase_seconds_bucket{phase!="headers_end"}[1m])))'
        try:
            require(status==200 and json.loads(raw)['content'][0]['text']=='v2smoke-ok','delayed probe HTTP/mock response failed')
            require(next((value for key,value in headers.items() if key.lower()=='x-request-id'),None)==request_id,'delayed response request ID mismatch')
            # API polling emulates 10s refresh; it does not claim browser timing.
            for tick in (0,10,20):
                while time.monotonic()<completed+tick:time.sleep(max(0,min(.2,completed+tick-time.monotonic())))
                data,query_seconds,filename=query(expression,'ac1-phase-p95')
                values={row['metric']['phase']:float(row['value'][1]) for row in data['data']['result'] if math.isfinite(float(row['value'][1]))}
                elapsed=time.monotonic()-completed
                dominant=max(values,key=values.get) if values else None
                probe['observations'].append({'elapsed_seconds':elapsed,'query_seconds':query_seconds,'phase_p95_seconds':values,'dominant':dominant,'raw_file':filename})
                if dominant=='upstream' and query_seconds<=2 and elapsed<=30:
                    probe['prom_dominant']='upstream';probe['prom_visible_after_seconds']=elapsed;break
            require(probe.get('prom_dominant')=='upstream','upstream not stably identifiable within 30s/2s query budget')
            pg_deadline=completed+90
            while True:
                rows=json.loads(sql(f"SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s WHERE request_id='{request_id}' AND observed_at>='{window}'::timestamptz;"))
                if rows:break
                require(time.monotonic()<pg_deadline,'PG sample not visible within 90s');time.sleep(.5)
            probe['pg_visible_after_seconds']=time.monotonic()-completed;probe['sample_rows']=rows
            (evidence/'ac1-pg-row.json').write_text(json.dumps(rows,indent=2)+'\n')
            require(len(rows)==1 and probe['pg_visible_after_seconds']<=90,'PG exact row count/timing failed')
            sample=rows[0]
            phases={phase:sample[phase+'_ms'] for phase in ('body_read','gateway','wait','upstream','response','finish')}
            require(all(isinstance(value,(int,float)) for value in phases.values()),'PG phases unavailable')
            probe['phase_rounding']=phase_rounding_evidence(phases,sample['total_ms'])
            require(max(phases,key=phases.get)=='upstream' and phases['upstream']>=2900,'PG did not confirm injected upstream dominant phase')
            require(sample['result']=='success' and sample['http_status']==200 and sample['attempt_count']==1,'delayed request did not settle normally')
            probe['pg_phase_ms']=phases;probe['status']='PASS (upstream/API-only subcase)'
            print('AC-1 upstream/API subcase PASS: Prom',probe['prom_visible_after_seconds'],'s; PG',probe['pg_visible_after_seconds'],'s',flush=True)
        except Exception as error:
            probe['status']='未执行（实时子项未完成）';probe['reason']=str(error)
            print('AC-1 remains 未执行:',str(error),flush=True)
        print('AC-1: starting 120s post-request quiet window; full AC remains 未执行',flush=True)
        quiet_until=completed+120
        while time.monotonic()<quiet_until:time.sleep(max(0,min(1,quiet_until-time.monotonic())))
        probe['post_quiet_seconds']=time.monotonic()-completed
        rows=json.loads(sql(f"SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s WHERE request_id='{request_id}' AND observed_at>='{window}'::timestamptz;"))
        probe['settled_exact_row_count']=len(rows)
        calls=json.loads(http('http://mock:8000/requests')[2])
        probe['mock_total_calls']=len(calls)
        require(len(calls)==2 and calls[-1]['body']['messages'][0]['content']==request_id,'isolated window has unexpected upstream calls')
        if probe['status'].startswith('PASS'):require(len(rows)==1,'delayed duplicate sample after quiet window')
        budget('after-probe')
        info['AC-10']['status']='PASS (isolated test topology/load only)'


if __name__=='__main__':sys.exit(smoke.main(extension=BudgetProbe()))
