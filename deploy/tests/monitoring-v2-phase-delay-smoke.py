#!/usr/bin/env python3
"""AC-1 remaining body_read/wait/response delay probes; --image mandatory.

15s scrape, 10s API polling, <=2s query; each target PG row matched exactly.
120s gateway quiet windows before/after, shared between adjacent probes.
The explicit wait-holder companion shares account slots/DB/Redis but has V2
disabled and is not scraped: its upstream delay cannot contaminate the target
phase histogram. Both target + companion requests are retained as evidence.
Browser first-visible timing, multi-instance production behavior unexecuted.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import copy
from datetime import datetime,timezone
import importlib.util
import json
import math
from pathlib import Path
import secrets
import sys
import time
from urllib.parse import urlencode

sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('budget',Path(__file__).with_name('monitoring-v2-series-budget.py'))
budget=importlib.util.module_from_spec(spec);spec.loader.exec_module(budget)
smoke=budget.smoke;require=budget.require


class DelayProbe(budget.BudgetProbe):
    def __init__(self,cases):self.selected_cases=cases
    def configure(self,config,work,evidence,report):
        super().configure(config,work,evidence,report);report.pop('W6c')
        fixtures=smoke.ROOT/'deploy/tests/fixtures'
        config['services']['mock']['volumes']=[str(fixtures/'monitoring-v2-delay-upstream.py')+':/fixture/upstream.py:ro',str(fixtures/'monitoring-v2-slow-body-client.py')+':/fixture/slow-client.py:ro']
        holder=copy.deepcopy(config['services']['sub2api']);holder['profiles']=['holder'];holder['volumes']=['holder:/app/data']
        holder['environment'].update(MONITORING_OPS_METRICS_ENABLED='false',MONITORING_OPS_SAMPLES_ENABLED='false')
        if 'wait' in self.selected_cases:
            config['services']['slot-holder']=holder;config['volumes']['holder']={}
        report['AC-1-delays']={'status':'未执行','scrape_seconds':15,'poll_seconds':10,'query_budget_seconds':2,
            'scope':'API phase attribution; default measured app only. Holder companion V2 disabled, same DB/Redis/account. Browser timing unexecuted.',
            'selected_cases':self.selected_cases,'cases':{}}
    def prepare_seed(self,sql,report,api_key):sql('UPDATE accounts SET concurrency=1;')
    def prepare_isolated_pair(self,sql,report,api_key,compose,run):
        self.compose=compose;self.run=run
        if 'wait' in self.selected_cases:run(compose+['up','-d','slot-holder'])

    def verify(self,http,sql,report,evidence,api_key):
        info=report['AC-1-delays'];sequence=0;compose=self.compose;run=self.run
        def save(name,data):(evidence/name).write_text(json.dumps(data,indent=2)+'\n')
        def query(expression,name):
            nonlocal sequence
            sequence+=1;start=time.monotonic()
            code,_,raw=http('http://prometheus:9090/api/v1/query?'+urlencode({'query':expression}));duration=time.monotonic()-start
            file=f'phase-{sequence:03d}-{name}.json';(evidence/file).write_bytes(raw)
            with (evidence/'phase-query-times.jsonl').open('a') as f:f.write(json.dumps({'at':datetime.now(timezone.utc).isoformat(),'query':expression,'seconds':duration,'file':file})+'\n')
            require(code==200,'Prom HTTP failure: '+file)
            result=json.loads(raw);require(result.get('status')=='success','Prom expression failure: '+file)
            return result['data']['result'],duration,file
        def quiet(until,label):
            print('AC-1 QUIET',label,'remaining_seconds='+str(round(max(0,until-time.monotonic()))),flush=True)
            while time.monotonic()<until:time.sleep(max(0,min(1,until-time.monotonic())))
        if 'wait' in self.selected_cases:
            deadline=time.monotonic()+90
            while True:
                try:
                    if http('http://slot-holder:8080/health')[0]==200:break
                except OSError:pass
                require(time.monotonic()<deadline,'holder app not healthy');time.sleep(1)
            _,_,holder_metrics=http('http://slot-holder:9091/metrics')
            (evidence/'phase-holder-metrics.txt').write_bytes(holder_metrics)
            require(b'sub2api_request_phase_seconds_count{' not in holder_metrics,'holder must not emit V2 phase samples')
        save('phase-account-config.json',json.loads(sql("SELECT json_agg(a) FROM (SELECT id,name,concurrency FROM accounts ORDER BY id)a;")))
        expression='histogram_quantile(0.95,sum by(le,phase)(rate(sub2api_request_phase_seconds_bucket{phase!="headers_end"}[1m])))'
        quiet(time.monotonic()+120,'initial pre-window')
        for kind,expected in [('body','body_read'),('wait','wait'),('response','response')]:
            if kind not in self.selected_cases:continue
            marker='ac1-'+kind+'-'+str(time.time_ns())+'-'+secrets.token_hex(3)
            window=sql('SELECT clock_timestamp();');entry={'status':'未执行','request_id':marker,'expected_phase':expected,'window_start':window,'observations':[]}
            info['cases'][kind]=entry
            payload={'model':smoke.MODEL,'max_tokens':32,'stream':False,'messages':[{'role':'user','content':marker}]}
            body=json.dumps(payload,separators=(',',':')).encode();headers={'Authorization':'Bearer '+api_key,'Content-Type':'application/json','x-request-id':marker,'anthropic-version':'2023-06-01'}
            print('AC-1 INJECT',kind,marker,flush=True)
            completed=None
            try:
                if kind=='body':
                    client=json.loads(run(compose+['exec','-T','mock','python','/fixture/slow-client.py'],stdin=json.dumps({'body_base64':base64.b64encode(body).decode(),'headers':headers})).stdout)
                    save('phase-body-client.json',client);status=client['status'];raw=base64.b64decode(client['response_base64'])
                    require(client['upload_seconds']>=2.9,'slow body did not take ~3s')
                elif kind=='wait':
                    holder_id='ac1-holder-'+str(time.time_ns())+'-'+secrets.token_hex(3)
                    holder_body=json.dumps(dict(payload,messages=[{'role':'user','content':holder_id}]),separators=(',',':')).encode()
                    holder_headers=dict(headers,**{'x-request-id':holder_id})
                    with ThreadPoolExecutor(max_workers=1) as pool:
                        future=pool.submit(http,'http://slot-holder:8080/v1/messages',holder_body,holder_headers)
                        deadline=time.monotonic()+10
                        while True:
                            calls=json.loads(http('http://mock:8000/requests')[2])
                            matching=[c for c in calls if c['request_id']==holder_id]
                            if matching and matching[0]['state']=='active':break
                            require(time.monotonic()<deadline,'holder never acquired slot');time.sleep(.05)
                        status,_,raw=http('http://sub2api:8080/v1/messages',body,headers)
                        holder_status,_,holder_raw=future.result()
                        entry.update(holder_request_id=holder_id,holder_status=holder_status)
                        require(holder_status==200,'holder request failed')
                        (evidence/'phase-holder-response.json').write_bytes(holder_raw)
                else:status,_,raw=http('http://sub2api:8080/v1/messages',body,headers)
                completed=time.monotonic();entry['completed_at']=datetime.now(timezone.utc).isoformat();entry['http_status']=status
                (evidence/('phase-'+kind+'-response.json')).write_bytes(raw)
                require(status==200 and json.loads(raw)['usage']=={'input_tokens':11,'output_tokens':7},'delayed request failed')
                for tick in (0,10,20):
                    while time.monotonic()<completed+tick:time.sleep(max(0,min(.2,completed+tick-time.monotonic())))
                    rows,duration,file=query(expression,kind+'-phase-p95')
                    values={r['metric']['phase']:float(r['value'][1]) for r in rows if math.isfinite(float(r['value'][1]))}
                    elapsed=time.monotonic()-completed;dominant=max(values,key=values.get) if values else None
                    unique=dominant is not None and sum(abs(value-values[dominant])<1e-9 for value in values.values())==1
                    entry['observations'].append({'at_seconds':elapsed,'query_seconds':duration,'p95_seconds':values,'dominant':dominant,'unique_maximum':unique,'raw_file':file})
                    if dominant==expected and unique and duration<=2 and elapsed<=30:
                        entry['prom_visible_seconds']=elapsed;break
                deadline=completed+90
                while True:
                    samples=json.loads(sql(f"SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s WHERE request_id='{marker}' AND observed_at>='{window}'::timestamptz;"))
                    if samples:break
                    require(time.monotonic()<deadline,'PG sample unavailable within 90s');time.sleep(.5)
                save('phase-'+kind+'-pg.json',samples);entry['pg_visible_seconds']=time.monotonic()-completed
                require(len(samples)==1 and entry['pg_visible_seconds']<=90,'exact PG sample count/time mismatch')
                sample=samples[0];phases={name:sample[name+'_ms'] for name in ('body_read','gateway','wait','upstream','response','finish')}
                entry['pg_phase_ms']=phases;entry['rounding']=budget.phase_rounding_evidence(phases,sample['total_ms'])
                require('prom_visible_seconds' in entry,'expected unique dominant phase not visible within 30s; PG decomposition retained')
                require(max(phases,key=phases.get)==expected and phases[expected]>=2700,'PG dominant phase/delay did not match injection')
                require(sample['http_status']==200 and sample['result']=='success' and sample['attempt_count']==1,'request did not settle as one success')
                entry['status']='PASS (API subcase)'
            except Exception as error:
                completed=completed or time.monotonic();entry['reason']=str(error)
                entry['status']='未执行（注入或实时子项未稳定完成）'
            save('phase-delay-progress.json',info)
            print('AC-1 CASE',kind,json.dumps(entry),flush=True)
            quiet(completed+120,kind+' post-window / next pre-window')
            count=int(sql(f"SELECT count(*) FROM ops_request_transfer_samples WHERE request_id='{marker}';"));entry['settled_exact_row_count']=count
            if entry['status'].startswith('PASS'):require(count==1,'delayed duplicate sample')
        calls=json.loads(http('http://mock:8000/requests')[2]);save('phase-mock-records.json',calls)
        require(len(calls)==1+len(self.selected_cases)+int('wait' in self.selected_cases),'unexpected gateway traffic outside selected injections and warmup')
        info['status']='PASS (selected API subcases; browser timing unexecuted)' if all(c['status'].startswith('PASS') for c in info['cases'].values()) else '未执行（部分场景未稳定完成，见逐项证据）'
        save('phase-delay-progress.json',info)
        print('AC-1 THREE-CASE RESULT',json.dumps(info),flush=True)
        require(all(c['status'].startswith('PASS') for c in info['cases'].values()),'AC-1 remaining cases incomplete; per-case Prom/PG evidence retained')


if __name__=='__main__':
    parser=argparse.ArgumentParser(add_help=False)
    parser.add_argument('--cases',nargs='+',choices=['body','wait','response'],default=['body','wait','response'])
    arguments,remaining=parser.parse_known_args()
    sys.argv=[sys.argv[0],*remaining]
    sys.exit(smoke.main(extension=DelayProbe(arguments.cases)))
