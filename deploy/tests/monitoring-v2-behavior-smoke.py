#!/usr/bin/env python3
"""AC-9 image-level V2 off/on replay; cloned PG DB and independent Redis/data.

--image required. Saves raw protocol/chunk/frame/PG evidence and every allowed
volatile/observational difference. Instrumentation panic and exact Go Close/
Flush call counts cannot be injected/observed in this stock image: unexecuted,
with separate W2c1 component references. No production endpoints or credentials.
"""
import base64
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import time
import uuid

sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('smoke',Path(__file__).with_name('monitoring-v2-deploy-smoke.py'))
smoke=importlib.util.module_from_spec(spec);spec.loader.exec_module(smoke)
def require(condition,message):
    if not condition:raise smoke.SmokeFailure(message)


class BehaviorProbe:
    def configure(self,config,work,evidence,report):
        self.evidence=evidence
        fixture=smoke.ROOT/'deploy/tests/fixtures'
        config['services']['mock']['volumes']=[str(fixture/'monitoring-v2-ac9-upstream.py')+':/fixture/upstream.py:ro',str(fixture/'monitoring-v2-ac9-client.py')+':/fixture/client.py:ro']
        config['services']['redis-off']=copy.deepcopy(config['services']['redis']);config['services']['redis-off']['profiles']=['off']
        off=copy.deepcopy(config['services']['sub2api']);off['profiles']=['off'];off['volumes']=['app-off:/app/data']
        off['environment'].update(DATABASE_DBNAME='sub2api_off',REDIS_HOST='redis-off',MONITORING_OPS_METRICS_ENABLED='false',MONITORING_OPS_SAMPLES_ENABLED='false')
        off['depends_on'].pop('redis');off['depends_on']['redis-off']={'condition':'service_healthy'}
        config['services']['sub2api-off']=off;config['volumes']['app-off']={}
        report['AC-9']={'status':'未执行','image_test':'same pinned image ID, Ops metrics/samples true vs false',
            'isolation':'cloned initial PG database, independent Redis service and app data volumes, internal network',
            'unexecuted':['stock-image instrumentation panic/failing collector/sink injection','exact Go Close/Flush call counts','HTTP2 cancellation isolation'],
            'component_references':['docs/MONITORING_V2_VALIDATION.md §2.3.4/2.3.5','/tmp/w2c1-tracked-body-fixed-race.log','/tmp/w2c1-public-identity-fixed-race.log']}

    def prepare_isolated_pair(self,sql,report,api_key,compose,run):
        sql('CREATE DATABASE sub2api_off;')
        dump=run(compose+['exec','-T','postgres','pg_dump','-U','sub2api','-d','sub2api','--no-owner','--no-privileges']).stdout
        run(compose+['exec','-T','postgres','psql','-X','-U','sub2api','-d','sub2api_off','-v','ON_ERROR_STOP=1'],stdin=dump)
        report['AC-9']['clone_dump_sha256']=hashlib.sha256(dump.encode()).hexdigest()
        run(compose+['up','-d','sub2api-off'])

    def run_scenarios(self,http,sql,report,evidence,api_key,compose,run):
        info=report['AC-9']
        def db(query,side):
            return run(compose+['exec','-T','postgres','psql','-X','-U','sub2api','-d','sub2api_off' if side=='off' else 'sub2api','-At','-v','ON_ERROR_STOP=1'],stdin=query).stdout.strip()
        def save(name,data):
            (evidence/name).write_text(json.dumps(data,indent=2)+'\n')
        deadline=time.monotonic()+90
        while True:
            try:
                if http('http://sub2api-off:8080/health')[0]==200:break
            except OSError:pass
            require(time.monotonic()<deadline,'off app not healthy');time.sleep(1)
        initial={}
        for side in ('off','on'):
            snapshot={}
            for table in ('users','groups','accounts','account_groups','api_keys','usage_logs'):
                order="account_id,group_id" if table=="account_groups" else "id"
                snapshot[table]=json.loads(db(f"SELECT coalesce(json_agg(x),'[]'::json) FROM (SELECT * FROM {table} ORDER BY {order})x;",side))
            initial[side]=snapshot
        save('ac9-initial-seed-equality.json',{'equal':initial['off']==initial['on'],'tables':list(initial['on'])})
        # Store hashes, not credential/password material from the cloned seed.
        require(initial['off']==initial['on'],'cloned initial business tables differ')
        info['initial_seed_equal']=True;info['cases']={};raw_results={}
        for case in ('json','sse','abort'):
            pair={};upstream={};usage={}
            marker='ac9-'+case;body=json.dumps({'model':smoke.MODEL,'max_tokens':32,'stream':case!='json','messages':[{'role':'user','content':marker}]},separators=(',',':')).encode()
            headers={'Host':'ac9.fixture.local','Authorization':'Bearer '+api_key,'Content-Type':'application/json','anthropic-version':'2023-06-01','x-request-id':marker,'User-Agent':'ac9-fixed-client','Accept-Encoding':'identity','Connection':'close'}
            save('ac9-'+case+'-client-request.json',{'method':'POST','path':'/v1/messages','headers':dict(headers,Authorization='sha256:'+hashlib.sha256(headers['Authorization'].encode()).hexdigest()),'body_base64':base64.b64encode(body).decode()})
            for side in ('off','on'):
                records_before=json.loads(http('http://mock:8000/requests')[2]);count_before=int(db('SELECT count(*) FROM usage_logs;',side))
                request={'host':'sub2api-off' if side=='off' else 'sub2api','case':case,'body_base64':base64.b64encode(body).decode(),'headers':headers}
                result=json.loads(run(compose+['exec','-T','mock','python','/fixture/client.py'],stdin=json.dumps(request)).stdout)
                pair[side]=result;save(f'ac9-{case}-{side}-response.json',result)
                (evidence/f'ac9-{case}-{side}-body.bin').write_bytes(base64.b64decode(result['body_base64']))
                deadline=time.monotonic()+20
                while True:
                    records=json.loads(http('http://mock:8000/requests')[2]);new=records[len(records_before):]
                    if new and new[-1]['state']!='sending':break
                    require(time.monotonic()<deadline,'mock response did not settle');time.sleep(.25)
                require(len(new)==1,'unexpected retry/handler replay: '+case+'/'+side)
                upstream[side]=new[0];save(f'ac9-{case}-{side}-upstream.json',new[0])
                # Wait for async billing after transport has settled, then record
                # the exact row delta. Abort may legitimately create no usage.
                deadline=time.monotonic()+10;previous=None;stable=0
                while True:
                    rows=json.loads(db(f"SELECT coalesce(json_agg(x),'[]'::json) FROM (SELECT * FROM usage_logs ORDER BY id OFFSET {count_before})x;",side))
                    if rows==previous:stable+=1
                    else:previous=rows;stable=0
                    if stable>=3 and (rows or case=='abort'):break
                    require(time.monotonic()<deadline,'usage did not settle');time.sleep(.5)
                usage[side]=rows;save(f'ac9-{case}-{side}-usage.json',rows)
            def canonical_headers(rows):
                normalized=[]
                for key,value in rows:
                    key=key.lower()
                    if key=='date':value='<wall-clock>'
                    elif key=='x-client-request-id':
                        require(str(uuid.UUID(value))==value,'generated client ID is not canonical UUID')
                        value='<generated-client-id>'
                    normalized.append((key,value))
                return sorted(normalized)
            # Preserve header names/multiplicity. Raw Date/UUID values and
            # UUID->usage correlation are retained, not silently discarded.
            client_ids={side:[value for key,value in pair[side]['headers'] if key.lower()=='x-client-request-id'] for side in pair}
            require(all(len(values)==1 for values in client_ids.values()),'missing/duplicate generated client ID')
            checks={'client_request_identical':True,
                    'upstream_request_body_equal':upstream['off']['body_base64']==upstream['on']['body_base64'],
                    'upstream_request_headers_equal':canonical_headers(upstream['off']['headers'])==canonical_headers(upstream['on']['headers']),
                    'response_status_equal':pair['off']['status']==pair['on']['status']==200,
                    'response_headers_equal_after_declared_normalization':canonical_headers(pair['off']['headers'])==canonical_headers(pair['on']['headers']),
                    'response_body_equal':pair['off']['body_base64']==pair['on']['body_base64'],
                    'sse_frames_equal':pair['off']['sse_frames_base64']==pair['on']['sse_frames_base64'],
                    'http_chunks_equal':[r['base64'] for r in pair['off']['chunks']]==[r['base64'] for r in pair['on']['chunks']],
                    'client_abort_equal':pair['off']['client_rst']==pair['on']['client_rst']==(case=='abort'),
                    'one_upstream_attempt_each':True,'usage_row_count_equal':len(usage['off'])==len(usage['on'])}
            checks['generated_id_usage_correlation']=all(row['request_id']=='client:'+client_ids[side][0] for side in usage for row in usage[side])
            volatile={'id','request_id','created_at','duration_ms','first_token_ms'}
            observation={'body_bytes','gateway_latency_ms'}
            def billing_rows(rows):return [{key:value for key,value in row.items() if key not in volatile|observation} for row in rows]
            checks['usage_billing_fields_equal']=billing_rows(usage['off'])==billing_rows(usage['on'])
            if case!='abort':require(len(usage['on'])==1,'successful case must write exactly one usage row')
            differences=[]
            for off,on in zip(usage['off'],usage['on']):
                differences.extend({'field':key,'off':off.get(key),'on':on.get(key),'category':'observational' if key in observation else 'volatile' if key in volatile else 'unexpected'} for key in set(off)|set(on) if off.get(key)!=on.get(key))
            for side in ('off','on'):
                result=pair[side]
                if case!='json':
                    require(len(result['frame_arrival_seconds'])>=5,'missing observable incremental SSE frames')
                    require(result['frame_arrival_seconds'][4]-result['frame_arrival_seconds'][0]>.4,'SSE buffered instead of incrementally visible')
            info['cases'][case]={'checks':checks,'usage_differences':differences,
                'generated_client_ids':client_ids,'header_normalization':['Date wall clock value','canonical UUID x-client-request-id; linked to usage.request_id'],
                'response_date_values':{side:[v for k,v in pair[side]['headers'] if k.lower()=='date'] for side in pair},
                'upstream_end_state':{side:upstream[side]['state'] for side in upstream},
                'frame_arrival_seconds':{side:pair[side]['frame_arrival_seconds'] for side in pair},
                'usage_rows':{side:len(usage[side]) for side in usage}}
            raw_results[case]=pair
            save('ac9-comparison.json',info)
            require(all(checks.values()),'AC-9 differences in '+case+': '+str([key for key,value in checks.items() if not value]))
            print('AC-9 CASE PASS',case,json.dumps(info['cases'][case]),flush=True)
        for side in ('off','on'):
            host='sub2api-off' if side=='off' else 'sub2api'
            _,_,metrics=http('http://'+host+':9091/metrics');(evidence/f'ac9-{side}-metrics.txt').write_bytes(metrics)
            has_v2=b'sub2api_request_phase_seconds_count{' in metrics
            require(has_v2==(side=='on'),'V2 switch did not affect metrics as expected')
        off_samples=int(db('SELECT count(*) FROM ops_request_transfer_samples;','off'))
        on_samples=json.loads(db("SELECT coalesce(json_agg(s),'[]'::json) FROM ops_request_transfer_samples s;",'on'))
        save('ac9-on-samples.json',on_samples)
        require(off_samples==0 and len(on_samples)==3 and all(row['attempt_count']==1 for row in on_samples),'sample toggle/attempt evidence mismatch')
        balances={side:db('SELECT balance::text FROM users ORDER BY id;',side) for side in ('off','on')}
        require(balances['off']==balances['on'],'final billed balances differ')
        info.update(status='PASS (image protocol/billing subcases; exception injection unexecuted)',final_balances=balances,off_sample_rows=off_samples,on_sample_rows=len(on_samples))
        save('ac9-comparison.json',info)
        print('AC-9 IMAGE SUBCASES PASS',json.dumps(info),flush=True)


if __name__=='__main__':sys.exit(smoke.main(extension=BehaviorProbe()))
