#!/usr/bin/env python3
"""AC-5: ten startup accounts, twenty measured requests per account, Prom API.

--image is mandatory. Each account has one group/key, preventing random routing.
Two warmups/account (success + failure) initialize the rate baseline, separately
recorded from the 200 measured requests (18 success + 2 controlled 400/account).
QPS compares identical scrape endpoints; client load throughput is also reported.
No Grafana/browser rendering or P1/P2 coverage is claimed.
"""
from concurrent.futures import ThreadPoolExecutor
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
spec=importlib.util.spec_from_file_location('series_budget',Path(__file__).with_name('monitoring-v2-series-budget.py'))
budget=importlib.util.module_from_spec(spec);spec.loader.exec_module(budget)
smoke=budget.smoke
require=budget.require


class AccountsProbe(budget.BudgetProbe):
    def configure(self,config,work,evidence,report):
        super().configure(config,work,evidence,report)
        report.pop('W6c')
        report['AC-5']={'status':'未执行','scrape_interval_seconds':15,'scope':'P0 Anthropic JSON; ten startup accounts; API-only, no browser same-screen check'}

    def prepare_seed(self,sql,report,api_key):
        self.keys={'smoke_account':api_key}
        for number in range(2,11):
            name=f'acct{number:02d}';key=secrets.token_hex(32);self.keys[name]=key
            sql(f"""BEGIN;
INSERT INTO groups(name,platform,status,rate_multiplier) VALUES('{name}','anthropic','active',1);
INSERT INTO accounts(name,platform,type,credentials,status,concurrency,priority)
VALUES('{name}','anthropic','apikey','{{"base_url":"http://mock:8000","api_key":"ac5-test-only"}}'::jsonb,'active',3,1);
INSERT INTO account_groups(account_id,group_id,priority) SELECT a.id,g.id,1 FROM accounts a,groups g WHERE a.name='{name}' AND g.name='{name}';
INSERT INTO api_keys(user_id,key,name,group_id,status) SELECT {report['seed_ids']['user_id']},'{key}','{name}',g.id,'active' FROM groups g WHERE g.name='{name}';
COMMIT;""")
        identities=json.loads(sql("SELECT json_agg(row_to_json(x)) FROM (SELECT a.id AS account_id,a.name,k.id AS api_key_id,k.group_id FROM accounts a JOIN account_groups ag ON ag.account_id=a.id JOIN api_keys k ON k.group_id=ag.group_id ORDER BY a.id)x;"))
        require(len(identities)==10 and {row['name'] for row in identities}==set(self.keys),'ten-account seed failed')
        report['AC-5']['seed_identities']=identities

    def verify(self,http,sql,report,evidence,api_key):
        info=report['AC-5'];names=set(self.keys);sequence=0;records=[]
        prefix='ac5-'+str(time.time_ns())+'-'+secrets.token_hex(3)
        info['request_prefix']=prefix

        def query(expression,name,at=None):
            nonlocal sequence
            sequence+=1
            params={'query':expression}
            if at is not None:params['time']=f'{at:.6f}'
            started=time.monotonic();status,_,raw=http('http://prometheus:9090/api/v1/query?'+urlencode(params));elapsed=time.monotonic()-started
            filename=f'ac5-{sequence:03d}-{name}.json';(evidence/filename).write_bytes(raw)
            with (evidence/'ac5-prom-requests.jsonl').open('a') as f:f.write(json.dumps({'at':datetime.now(timezone.utc).isoformat(),'params':params,'elapsed_seconds':elapsed,'file':filename})+'\n')
            require(status==200,'Prom API unavailable: '+filename)
            data=json.loads(raw);require(data.get('status')=='success','Prom query failed: '+filename)
            return data['data']['result']

        def values(rows,label='account'):
            result={row['metric'][label]:float(row['value'][1]) for row in rows}
            require(all(math.isfinite(value) for value in result.values()),'non-finite Prom values')
            return result

        def request(name,index,measured):
            failed=index>=18 if measured else index==1
            marker=f'ac5-{name}-'+('fail' if failed else 'ok')+'-'+prefix+'-'+('measure' if measured else 'warmup')+'-'+str(index)
            body=json.dumps({'model':smoke.MODEL,'max_tokens':32,'stream':False,'messages':[{'role':'user','content':marker}]},separators=(',',':')).encode()
            began=time.monotonic();at=datetime.now(timezone.utc).isoformat()
            status,headers,raw=http('http://sub2api:8080/v1/messages',body,{'Authorization':'Bearer '+self.keys[name],'Content-Type':'application/json','anthropic-version':'2023-06-01','x-request-id':marker})
            record={'account':name,'index':index,'measured':measured,'request_id':marker,'started_at':at,'elapsed_seconds':time.monotonic()-began,'status':status,'expected_status':400 if failed else 200,'response_bytes':len(raw)}
            record['request_id_preserved']=next((v for k,v in headers.items() if k.lower()=='x-request-id'),None)==marker
            try:
                response=json.loads(raw)
                record['response_matches']=response.get('error',{}).get('type')=='invalid_request_error' if failed else response.get('content',[{}])[0].get('text')=='v2smoke-ok'
                if not failed:record['usage_matches']=response.get('usage')=={'input_tokens':11,'output_tokens':7}
            except (ValueError,IndexError,TypeError):record['response_matches']=False
            # Each worker appends one complete JSON line; file writing is done
            # centrally after collecting results, so no credentials are persisted.
            return record

        # W6b already supplied the successful first-account warmup.
        for name in sorted(names):
            if name!='smoke_account':records.append(request(name,0,False))
            records.append(request(name,1,False))
        (evidence/'ac5-warmup-records.json').write_text(json.dumps(records,indent=2)+'\n')
        require(all(r['status']==r['expected_status'] and r['response_matches'] for r in records),'warmup response mismatch; see ac5-warmup-records.json')

        def latest_scrape():
            rows=query('timestamp(up{job="sub2api"})','scrape-timestamp')
            require(len(rows)==1,'requires one app scrape target')
            return float(rows[0]['value'][1])

        # Wait for a scrape after both result series and all ten TTFT buckets exist.
        warm_done=time.time();deadline=time.monotonic()+45
        while True:
            baseline=latest_scrape()
            if baseline>warm_done:break
            require(time.monotonic()<deadline,'no post-warmup scrape');time.sleep(2)
        base=query('sub2api_upstream_requests_total','baseline-counter',baseline+.001)
        base_ttft=query('sub2api_upstream_ttft_seconds_count','baseline-ttft-count',baseline+.001)
        base_ttft_sum=values(query('sub2api_upstream_ttft_seconds_sum','baseline-ttft-sum',baseline+.001))
        require(set(values(base_ttft))==names,'startup labels not exactly ten known accounts')
        expected_base=sum(float(row['value'][1]) for row in base)
        require(expected_base==20,'warmup must be exactly two attempts/account, including W6b warmup')
        window=sql('SELECT clock_timestamp();');start=time.monotonic()
        def worker(name):return [request(name,index,True) for index in range(20)]
        with ThreadPoolExecutor(max_workers=10) as pool:
            measured=[row for batch in pool.map(worker,sorted(names)) for row in batch]
        duration=time.monotonic()-start;finished=time.time()
        (evidence/'ac5-request-records.json').write_text(json.dumps(measured,indent=2)+'\n')
        info.update(measured_requests=len(measured),client_load_seconds=duration,client_load_qps=len(measured)/duration,
                    client_successes=sum(r['status']==200 for r in measured),client_failures=sum(r['status']!=200 for r in measured))
        require(len(measured)==200 and all(r['status']==r['expected_status'] and r['response_matches'] and r['request_id_preserved'] and r.get('usage_matches',True) for r in measured),'controlled measurement mismatched; see request records')
        deadline=time.monotonic()+45
        while True:
            end=latest_scrape()
            if end>finished:break
            require(time.monotonic()<deadline,'no post-measurement scrape');time.sleep(2)
        final=query('sub2api_upstream_requests_total','final-counter',end+.001)
        final_ttft=query('sub2api_upstream_ttft_seconds_count','final-ttft-count',end+.001)
        final_ttft_sum=values(query('sub2api_upstream_ttft_seconds_sum','final-ttft-sum',end+.001))
        # Range starts 9ms before the baseline sample, includes that sample and
        # every later 15s scrape, and evaluates 1ms after the final sample.
        span=end-baseline;range_seconds=span+.01
        interval=f'{int(round(range_seconds*1000))}ms';at=end+.001
        qps=values(query(f'sum by(account)(rate(sub2api_upstream_requests_total[{interval}]))','qps',at))
        failure=values(query(f'sum by(account)(rate(sub2api_upstream_requests_total{{result!="success"}}[{interval}])) / sum by(account)(rate(sub2api_upstream_requests_total[{interval}])) * 100','failure-percent',at))
        p95=values(query(f'histogram_quantile(0.95,sum by(le,account)(rate(sub2api_upstream_ttft_seconds_bucket[{interval}])))','ttft-p95',at))
        require(set(qps)==names and set(failure)==names and set(p95)==names,'Prom dimensions not exactly the ten seeded accounts')
        def counters(rows):return {(r['metric']['account'],r['metric']['result'],r['metric'].get('failure_reason','')):float(r['value'][1]) for r in rows}
        before=counters(base);after=counters(final);deltas={key:after.get(key,0)-before.get(key,0) for key in set(before)|set(after)}
        base_counts=values(base_ttft);end_counts=values(final_ttft)
        per_account={}
        for name in sorted(names):
            count=sum(v for key,v in deltas.items() if key[0]==name)
            failures=sum(v for key,v in deltas.items() if key[0]==name and key[1]!='success')
            ttft_count=end_counts[name]-base_counts[name]
            require(count==20 and failures==2,'attempt counter delta mismatch for '+name)
            require(ttft_count==18,'TTFT must observe each successful locked response once: '+name)
            require(abs(failure[name]-10)<1e-6,'failure ratio differs from 2/20: '+name)
            require(abs(qps[name]-20/span)<1e-6,'QPS differs from scrape-aligned request count/span: '+name)
            number=1 if name=='smoke_account' else int(name[-2:])
            require(p95[name]>=number*.05 and p95[name]<=5,'TTFT P95 incompatible with injected delay: '+name)
            per_account[name]={'injected_delay_ms':number*50,'requests':int(count),'successes':int(count-failures),'failures':int(failures),'qps':qps[name],'failure_percent':failure[name],'ttft_observations':int(ttft_count),'ttft_p95_seconds':p95[name]}
        info.update(per_account=per_account,prom_scrape_start=baseline,prom_scrape_end=end,prom_scrape_span_seconds=span,
                    prom_rate_range=interval,prom_qps=sum(qps.values()),m11_account_labels=len(p95),warmup_attempts=20)
        sample_deadline=time.monotonic()+30
        while True:
            rows=json.loads(sql(f"SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s WHERE observed_at>='{window}'::timestamptz AND request_id LIKE '%{prefix}-measure-%';"))
            if len(rows)>=200:break
            require(time.monotonic()<sample_deadline,'measured PG rows missing: '+str(len(rows)));time.sleep(.5)
        (evidence/'ac5-pg-rows.json').write_text(json.dumps(rows,indent=2)+'\n')
        require(len(rows)==200 and {r['request_id'] for r in rows}=={r['request_id'] for r in measured},'PG exact request ID set mismatch')
        identities={r['name']:r for r in info['seed_identities']};lookup={r['request_id']:r for r in measured}
        for row in rows:
            record=lookup[row['request_id']];identity=identities[record['account']]
            require(row['account_id']==identity['account_id'] and row['api_key_id']==identity['api_key_id'] and row['group_id']==identity['group_id'],'scheduler/account metadata mismatch')
            require(row['http_status']==record['status'] and row['attempt_count']==1,'PG status/attempt mismatch')
        for name in sorted(names):
            successful=[row['headers_end_ms'] for row in rows if lookup[row['request_id']]['account']==name and row['http_status']==200]
            require(len(successful)==18 and all(isinstance(value,int) for value in successful),'PG successful TTFT observations missing: '+name)
            # M-11 and PG headers_end use the same attempt first-byte - sent
            # duration. PG floors each to ms; Prom sum retains float seconds.
            total_ms=(final_ttft_sum[name]-base_ttft_sum[name])*1000
            error_ms=total_ms-sum(successful)
            require(-1e-5<=error_ms<=len(successful)+1e-5,'M-11 sum differs from PG beyond per-sample ms rounding: '+name)
            per_account[name].update(pg_empirical_ttft_p95_ms=sorted(successful)[math.ceil(.95*len(successful))-1],
                                     prom_ttft_sum_ms=total_ms,pg_ttft_sum_ms=sum(successful),ttft_sum_rounding_error_ms=error_ms)
        mock_calls=json.loads(http('http://mock:8000/requests')[2]);(evidence/'ac5-mock-requests.json').write_text(json.dumps(mock_calls,indent=2)+'\n')
        require(len(mock_calls)==220,'expected 20 warmup + 200 measured calls; possible hidden retry')
        require(sum(r['body']['messages'][0]['content'] in lookup for r in mock_calls)==200,'mock measured marker count mismatch')
        info['status']='PASS (P0/API-only; browser same-screen unexecuted)'
        print('AC-5 PASS',json.dumps({k:v for k,v in info.items() if k not in ('seed_identities','per_account')}),flush=True)
        print('AC-5 PER ACCOUNT',json.dumps(per_account),flush=True)


if __name__=='__main__':sys.exit(smoke.main(extension=AccountsProbe()))
