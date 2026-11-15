#!/usr/bin/env python3
"""AC-2 image byte evidence: four real gateway scenarios, --image mandatory.

M-2 live counter deltas are checked against exact-ID PG samples and captured
request/response bytes; raw Prom API cumulative counters are also retained.
Gateway truncation uses the real 1024-byte upstream read cap, not mock EOF.
Client bytes received and successful downstream writes are different boundaries.
"""
import base64
import importlib.util
import json
from pathlib import Path
import secrets
import sys
import time
from urllib.parse import urlencode

sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('budget',Path(__file__).with_name('monitoring-v2-series-budget.py'))
budget=importlib.util.module_from_spec(spec);spec.loader.exec_module(budget)
smoke=budget.smoke;require=budget.require


class ByteProbe(budget.BudgetProbe):
    def configure(self,config,work,evidence,report):
        super().configure(config,work,evidence,report);report.pop('W6c')
        fixtures=smoke.ROOT/'deploy/tests/fixtures'
        config['services']['mock']['volumes']=[str(fixtures/'monitoring-v2-byte-upstream.py')+':/fixture/upstream.py:ro',
            str(fixtures/'monitoring-v2-ac9-client.py')+':/fixture/client.py:ro']
        config['services']['sub2api']['environment']['GATEWAY_UPSTREAM_RESPONSE_READ_MAX_BYTES']='1024'
        report['AC-2']={'status':'未执行','read_cap_bytes':1024,'scope':'P0 image + local mock; M2/PG/Prom API; browser dashboard unexecuted','cases':{}}
    def prepare_seed(self,sql,report,api_key):
        sql("""INSERT INTO settings(key,value) VALUES('rectifier_settings','{"enabled":true,"thinking_signature_enabled":true,"thinking_budget_enabled":true,"apikey_signature_enabled":true}') ON CONFLICT(key) DO UPDATE SET value=excluded.value;""")

    def run_scenarios(self,http,sql,report,evidence,api_key,compose,run):
        info=report['AC-2'];sequence=0
        def save(name,data):(evidence/name).write_text(json.dumps(data,indent=2)+'\n')
        def query(expression,name):
            nonlocal sequence
            sequence+=1
            code,_,raw=http('http://prometheus:9090/api/v1/query?'+urlencode({'query':expression}))
            file=f'bytes-{sequence:03d}-{name}.json';(evidence/file).write_bytes(raw)
            require(code==200,'Prom API unavailable: '+file)
            result=json.loads(raw);require(result.get('status')=='success','Prom query failed: '+file)
            return result['data']['result'],file
        def metrics(name):
            code,_,raw=http('http://sub2api:9091/metrics');require(code==200,'metrics unavailable')
            (evidence/name).write_bytes(raw)
            directions=('in_wire','req_decoded','req_upstream','resp_upstream','resp_downstream')
            return {direction:sum(smoke.metric_samples(raw.decode(),'sub2api_transfer_bytes_total',{'direction':direction})) for direction in directions}
        fields={'in_wire':'bytes_in_wire','req_decoded':'bytes_in_decoded','req_upstream':'bytes_up_req_total','resp_upstream':'bytes_resp_upstream','resp_downstream':'bytes_resp_downstream'}
        for case in ('abort','conversion','retry','truncate'):
            marker='ac2-'+str(time.time_ns())+'-'+secrets.token_hex(3)+'-'+case
            row={'status':'未执行','request_id':marker};info['cases'][case]=row
            payload={'model':smoke.MODEL,'max_tokens':32,'stream':case=='abort','messages':[{'role':'user','content':marker}]}
            if case=='retry':
                payload.update(model='claude-3-5-sonnet-latest',max_tokens=2048,thinking={'type':'enabled','budget_tokens':1024},
                    messages=[{'role':'assistant','content':[{'type':'thinking','thinking':'retained thought','signature':'invalid-ac2-signature'},{'type':'text','text':'previous answer'}]},{'role':'user','content':marker}])
            body=json.dumps(payload,separators=(',',':')).encode();path='/v1/chat/completions' if case=='conversion' else '/v1/messages'
            headers={'Host':'ac2.fixture.local','Authorization':'Bearer '+api_key,'Content-Type':'application/json','x-request-id':marker,'anthropic-version':'2023-06-01'}
            save(f'bytes-{case}-request.json',{'path':path,'body_base64':base64.b64encode(body).decode(),'headers':dict(headers,Authorization='<ephemeral-test-key>')})
            before=metrics(f'bytes-{case}-metrics-before.txt');window=sql('SELECT clock_timestamp();')
            try:
                if case=='abort':
                    result=json.loads(run(compose+['exec','-T','mock','python','/fixture/client.py'],stdin=json.dumps({'host':'sub2api','case':'abort','body_base64':base64.b64encode(body).decode(),'headers':headers})).stdout)
                    status=result['status'];response=base64.b64decode(result['body_base64']);save('bytes-abort-client.json',result)
                    require(result['client_rst'] and len(result['sse_frames_base64'])==5,'abort did not close after expected frame')
                else:
                    status,response_headers,response=http('http://sub2api:8080'+path,body,headers)
                    save(f'bytes-{case}-response-headers.json',{'status':status,'headers':response_headers})
                (evidence/f'bytes-{case}-response.bin').write_bytes(response)
                row.update(http_status=status,client_received_bytes=len(response))
                deadline=time.monotonic()+30
                while True:
                    samples=json.loads(sql(f"SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s WHERE request_id='{marker}' AND observed_at>='{window}'::timestamptz;"))
                    if samples:break
                    require(time.monotonic()<deadline,'no PG sample for exact request ID');time.sleep(.5)
                save(f'bytes-{case}-pg.json',samples);require(len(samples)==1,'PG duplicate request sample')
                sample=samples[0];row['sample']=sample
                after=metrics(f'bytes-{case}-metrics-after.txt');delta={key:after[key]-before[key] for key in before};row['m2_delta']=delta
                for direction,field in fields.items():
                    if sample.get(field) is not None:require(delta[direction]==sample[field],'M2/PG mismatch: '+direction)
                calls=json.loads(http('http://mock:8000/requests')[2]);matching=[c for c in calls if c['request_id']==marker]
                save(f'bytes-{case}-upstream.json',matching)
                request_bytes=sum(len(base64.b64decode(c['request_body_base64'])) for c in matching)
                planned_bytes=sum(c['declared_response_bytes'] for c in matching)
                row.update(physical_calls=len(matching),captured_upstream_request_bytes=request_bytes,planned_upstream_response_bytes=planned_bytes,
                    response_byte_difference=sample['bytes_resp_upstream']-sample['bytes_resp_downstream'])
                require(sample['bytes_in_wire']==len(body) and sample['bytes_up_req_total']==request_bytes,'actual request bytes disagree with records')
                if case=='abort':
                    require(status==200 and sample['result']=='client_abort' and sample['failure_reason']=='client_abort_during_response','abort root reason missing')
                    require(sample['bytes_resp_upstream']>sample['bytes_resp_downstream']>=len(response),'abort byte difference missing')
                elif case=='conversion':
                    require(status==200 and json.loads(response)['choices'][0]['message']['content']=='ac2-ok','Chat conversion failed')
                    require(sample['result']=='success' and sample['failure_reason']=='','conversion misclassified')
                    require(len(matching)==1 and base64.b64decode(matching[0]['request_body_base64'])!=body,'Chat request was not rewritten')
                    require(json.loads(base64.b64decode(matching[0]['request_body_base64']))['stream'] is True,'Anthropic upstream streaming not enabled')
                    require(sample['bytes_resp_upstream']!=sample['bytes_resp_downstream'],'protocol conversion response byte difference missing')
                    require(sample['bytes_resp_upstream']==planned_bytes and sample['bytes_resp_downstream']==len(response),'conversion byte evidence mismatch')
                elif case=='retry':
                    require(status==200 and len(matching)==2 and [c['status'] for c in matching]==[400,200],'signature retry did not execute twice')
                    require(sample['result']=='success' and sample['failure_reason']=='' and sample['attempt_count']==1,'physical retry changed logical settlement')
                    require(sample['bytes_resp_upstream']==planned_bytes and sample['bytes_resp_downstream']==len(response),'failed retry response body not counted')
                    require(sample['bytes_resp_upstream']>sample['bytes_resp_downstream'],'discarded failure body invisible')
                else:
                    require(status==502 and b'Upstream response too large' in response,'actual gateway read cap not triggered')
                    require(len(matching)==1 and planned_bytes>1024 and sample['bytes_resp_upstream']==1025,'gateway did not stop at limit+1')
                    require(sample['bytes_resp_upstream_status']=='partial','early-close status must remain partial')
                    require(sample['result']!='client_abort' and sample['failure_reason']!='client_abort_during_response','truncation mistaken for client abort')
                    require(sample['bytes_resp_downstream']==len(response),'gateway error response byte count mismatch')
                row['status']='PASS (image byte/PG/API subcase)'
            except Exception as error:row['reason']=str(error);row['status']='未执行（构造或断言未稳定完成）'
            save('bytes-comparison.json',info)
            print('AC-2 CASE',case,json.dumps(row),flush=True)
            # Preserve Prom original cumulative M2 + root-reason counters only
            # after the next scrape, avoiding stale values from the prior case.
            settled=time.time();deadline=time.monotonic()+45
            while True:
                scraped,_=query('timestamp(up{job="sub2api"})',case+'-scrape-time')
                if scraped and float(scraped[0]['value'][1])>settled:break
                require(time.monotonic()<deadline,'no post-case app scrape');time.sleep(2)
            raw_m2,m2file=query('sub2api_transfer_bytes_total',case+'-m2')
            raw_outcome,outcomefile=query('sub2api_gateway_requests_total',case+'-outcome')
            row['prom_raw_files']=[m2file,outcomefile]
            current=metrics(f'bytes-{case}-metrics-prom-check.txt')
            prom_values={r['metric']['direction']:float(r['value'][1]) for r in raw_m2}
            require(all(prom_values.get(key,0)==value for key,value in current.items()),'Prom M2 differs from live settled counters')
        info['status']='PASS (four image byte/API subcases; browser unexecuted)' if all(row['status'].startswith('PASS') for row in info['cases'].values()) else '未完成（见各类实际证据）'
        save('bytes-comparison.json',info)
        print('AC-2 MATRIX',json.dumps(info),flush=True)
        require(all(row['status'].startswith('PASS') for row in info['cases'].values()),'AC-2 incomplete; raw evidence retained for every case')


if __name__=='__main__':sys.exit(smoke.main(extension=ByteProbe()))
