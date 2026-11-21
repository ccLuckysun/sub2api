#!/usr/bin/env python3
"""Run one controlled gateway request in a disposable local-only Compose stack.
Requires an already-built local V2 image. No production config/credentials,
backend edits, pushes or deployment to existing projects. PASS requires every
assertion, including exact request-ID correlation; internal-* is never a substitute.
"""
import argparse
import base64
from datetime import datetime,timezone
import json
import math
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import tempfile
import time

ROOT=Path(__file__).resolve().parents[2]
MOCK_IMAGE='python:3.12-alpine'
MODEL='claude-sonnet-4-20250514'


class SmokeFailure(RuntimeError):pass


def metric_samples(text,name,labels):
    samples=[]
    for line in text.splitlines():
        match=re.fullmatch(r'([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+([^\s]+)(?:\s+[^\s]+)?',line)
        if not match or match[1]!=name:continue
        parsed={key:json.loads('"'+value+'"') for key,value in re.findall(r'(\w+)="((?:\\.|[^"\\])*)"',match[2] or '')}
        if all(parsed.get(key)==value for key,value in labels.items()):
            value=float(match[3])
            if math.isfinite(value):samples.append(value)
    return samples


def main(extension=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image',required=True,help='already-built LOCAL application image/tag or digest')
    parser.add_argument('--timeout',type=int,default=240,help='startup timeout seconds (default 240)')
    parser.add_argument('--output-dir',type=Path,help='parent for a retained, sanitized evidence directory')
    args=parser.parse_args()
    if args.timeout<30:parser.error('--timeout must be >=30')
    project='sub2api-v2smoke-'+secrets.token_hex(4)
    evidence=(args.output_dir or Path('/tmp'))/(project+'-evidence')
    evidence.mkdir(parents=True,exist_ok=False);evidence.chmod(0o700)
    request_id='v2smoke-'+str(time.time_ns())+'-'+secrets.token_hex(3)
    password=secrets.token_hex(24);admin_password=secrets.token_hex(24);api_key='sk-v2smoke-'+secrets.token_hex(20)
    jwt=secrets.token_hex(32);totp=secrets.token_hex(32)
    admin_email='v2smoke-'+secrets.token_hex(4)+'@example.invalid'
    sensitive=[password,admin_password,api_key,jwt,totp]
    report={'project':project,'request_id':request_id,'requested_image':args.image,'status':'FAIL','checks':{},'cleanup':True}
    compose=[]
    started=False
    work=None
    failure=None

    def sanitize(text):
        for value in sensitive:text=text.replace(value,'<ephemeral-test-secret>')
        return text

    def run(command,check=True,stdin=None):
        result=subprocess.run([str(value) for value in command],input=stdin,text=True,capture_output=True)
        if check and result.returncode:
            raise SmokeFailure(sanitize('command failed: '+str(command[:3])+'\n'+result.stdout+'\n'+result.stderr))
        return result

    def require(condition,message):
        if not condition:raise SmokeFailure(message)

    def http(url,body=None,headers=None):
        # Docker 29 intentionally omits host port mappings for internal-only
        # networks. Run the client inside the isolated mock container instead;
        # this keeps app/DB/Redis/mock without any external network or host port.
        client = """import base64,json,sys,urllib.request,urllib.error
spec=json.load(sys.stdin)
request=urllib.request.Request(spec['url'],data=base64.b64decode(spec['body']) if spec['body'] is not None else None,headers=spec['headers'])
try:
    response=urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=20)
except urllib.error.HTTPError as error:
    response=error
except OSError as error:
    print(json.dumps({'error':str(error)}));sys.exit(0)
with response:
    print(json.dumps({'status':response.code,'headers':dict(response.headers),'body':base64.b64encode(response.read()).decode()}))
"""
        result=json.loads(run(compose+['exec','-T','mock','python','-c',client],stdin=json.dumps({
            'url':url,'body':base64.b64encode(body).decode() if body is not None else None,'headers':headers or {}})).stdout)
        if 'error' in result:raise OSError(result['error'])
        return result['status'],result['headers'],base64.b64decode(result['body'])

    def sql(query):
        return run(compose+['exec','-T','postgres','psql','-X','-U','sub2api','-d','sub2api','-v','ON_ERROR_STOP=1','-At'],stdin=query).stdout.strip()

    def redis_samples():
        raw_lines=run(compose+['exec','-T','redis','redis-cli','--raw','LRANGE','sub2api:ops:realtime_requests','0','500']).stdout.splitlines()
        out=[]
        for line in raw_lines:
            line=line.strip()
            if not line:continue
            try:out.append(json.loads(line))
            except json.JSONDecodeError:continue
        return out

    def wait_health(url,label):
        deadline=time.monotonic()+args.timeout;last=''
        while time.monotonic()<deadline:
            try:
                status,_,raw=http(url)
                if status==200:return
                last=f'{status}: {raw[:200]!r}'
            except OSError as error:last=str(error)
            time.sleep(1)
        raise SmokeFailure(label+' did not become healthy: '+last)

    try:
        # An image inspection is local-only. Reject remote Docker contexts before
        # creating any project, even if a remote context happens to be selected.
        docker_host=os.environ.get('DOCKER_HOST','')
        if os.environ.get('DOCKER_CONTEXT') or not docker_host:
            context=json.loads(run(['docker','context','inspect']).stdout)[0]
            docker_host=context['Endpoints']['docker']['Host']
        require(docker_host.startswith(('unix://','npipe://')),'refusing non-local Docker endpoint')
        image=json.loads(run(['docker','image','inspect',args.image]).stdout)[0]
        report.update(image_id=image['Id'],repo_digests=image.get('RepoDigests') or [],architecture=image['Architecture'])
        require(image['Os']=='linux','requires a Linux application image')
        print('PROJECT:',project,'REQUEST_ID:',request_id,flush=True)
        print('IMAGE:',args.image,'IMAGE_ID:',image['Id'],'REGISTRY_DIGESTS:',report['repo_digests'],flush=True)
        with tempfile.TemporaryDirectory(prefix=project+'-config-',dir='/tmp') as directory:
            work=Path(directory);os.chmod(work,0o755)
            # Compose JSON is private and temporary. Do not reuse any repository
            # .env, host bind data directory, network or named application volume.
            config={'services':{
                'postgres':{'image':'postgres:18-alpine','environment':{'POSTGRES_USER':'sub2api','POSTGRES_PASSWORD':password,'POSTGRES_DB':'sub2api'},'volumes':['pg:/var/lib/postgresql'],'healthcheck':{'test':['CMD-SHELL','pg_isready -U sub2api -d sub2api'],'interval':'1s','timeout':'3s','retries':60}},
                'redis':{'image':'redis:8-alpine','healthcheck':{'test':['CMD','redis-cli','ping'],'interval':'1s','timeout':'3s','retries':60}},
                'mock':{'image':MOCK_IMAGE,'command':['python','/fixture/upstream.py'],'volumes':[str(ROOT/'deploy/tests/fixtures/monitoring-v2-upstream.py')+':/fixture/upstream.py:ro']},
                'sub2api':{'image':image['Id'],'platform':'linux/'+image['Architecture'],'volumes':['app:/app/data'],
                    'depends_on':{'postgres':{'condition':'service_healthy'},'redis':{'condition':'service_healthy'},'mock':{'condition':'service_started'}},
                    'environment':{'AUTO_SETUP':'true','SERVER_HOST':'0.0.0.0','SERVER_PORT':'8080','SERVER_MODE':'release','RUN_MODE':'standard',
                        'DATABASE_HOST':'postgres','DATABASE_PORT':'5432','DATABASE_USER':'sub2api','DATABASE_PASSWORD':password,'DATABASE_DBNAME':'sub2api','DATABASE_SSLMODE':'disable',
                        'REDIS_HOST':'redis','REDIS_PORT':'6379','ADMIN_EMAIL':admin_email,'ADMIN_PASSWORD':admin_password,'JWT_SECRET':jwt,'TOTP_ENCRYPTION_KEY':totp,
                        'MONITORING_METRICS_ENABLED':'true','MONITORING_METRICS_BIND':'0.0.0.0:9091','MONITORING_OPS_METRICS_ENABLED':'true','MONITORING_OPS_SAMPLES_ENABLED':'true','MONITORING_OPS_SAMPLE_MAX_ITEMS':'2000','MONITORING_OPS_SAMPLE_TTL_HOURS':'24',
                        'OPS_CLEANUP_ENABLED':'false','HTTP_PROXY':'','HTTPS_PROXY':'','ALL_PROXY':'','NO_PROXY':'*',
                        'SECURITY_URL_ALLOWLIST_ENABLED':'false','SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS':'true','SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP':'true','TZ':'UTC'}}},
                'volumes':{'pg':{},'app':{}},'networks':{'default':{'internal':True}}}
            if extension is not None:extension.configure(config,work,evidence,report)
            path=work/'compose.json';path.write_text(json.dumps(config));path.chmod(0o600)
            compose=['docker','compose','-p',project,'-f',str(path)]
            try:
                run(compose+['config','--quiet'])
                started=True
                report['cleanup']=False
                run(compose+['up','-d','--pull','missing'])
                app='http://sub2api:8080';metrics_url='http://sub2api:9091/metrics';mock='http://mock:8000'
                report['http_transport']='docker compose exec mock; internal network only; no published host ports'
                wait_health(mock+'/health','mock');wait_health(app+'/health','application')
                # Seed only this disposable DB. Restart this app afterwards so
                # account whitelist + scheduler snapshots include the seed.
                seed=f"""BEGIN;
UPDATE users SET balance=1000,concurrency=10,status='active' WHERE email='{admin_email}';
INSERT INTO groups(name,platform,status,rate_multiplier) VALUES('v2smoke_group','anthropic','active',1);
INSERT INTO accounts(name,platform,type,credentials,status,concurrency,priority) VALUES('smoke_account','anthropic','apikey','{{"base_url":"http://mock:8000","api_key":"fixture-upstream-key"}}'::jsonb,'active',3,1);
INSERT INTO account_groups(account_id,group_id,priority) SELECT a.id,g.id,1 FROM accounts a,groups g WHERE a.name='smoke_account' AND g.name='v2smoke_group';
INSERT INTO api_keys(user_id,key,name,group_id,status) SELECT u.id,'{api_key}','v2smoke_key',g.id,'active' FROM users u,groups g WHERE u.email='{admin_email}' AND g.name='v2smoke_group';
COMMIT;
"""
                try:sql(seed)
                except SmokeFailure as error:
                    raise SmokeFailure('automatic seed failed (schema/image incompatibility). Minimal fixture: active funded user + anthropic standard group + apikey account with base_url=http://mock:8000 + account_groups + active api_key bound to that group. This run cannot PASS. '+str(error))
                ids=json.loads(sql("SELECT json_build_object('user_id',k.user_id,'api_key_id',k.id,'group_id',k.group_id,'account_id',a.id) FROM api_keys k,accounts a WHERE k.name='v2smoke_key' AND a.name='smoke_account';"))
                require(all(isinstance(value,int) and value>0 for value in ids.values()),'seed did not produce all required identities')
                report['seed_ids']=ids
                if extension is not None and hasattr(extension,'prepare_seed'):extension.prepare_seed(sql,report,api_key)
                if extension is not None and hasattr(extension,'prepare_isolated_pair'):extension.prepare_isolated_pair(sql,report,api_key,compose,run)
                run(compose+['restart','sub2api'])
                wait_health(app+'/health','application after seed restart')
                if extension is not None and hasattr(extension,'run_scenarios'):
                    extension.run_scenarios(http,sql,report,evidence,api_key,compose,run)
                    report['status']='PASS'
                else:
                    # Readiness is not measured using a gateway probe: there is only
                    # one controlled gateway request, so M10/PG cannot match old rows.
                    window=sql('SELECT clock_timestamp();')
                    payload={'model':MODEL,'max_tokens':32,'stream':False,'messages':[{'role':'user','content':request_id}]}
                    body=json.dumps(payload,separators=(',',':')).encode()
                    report.update(window_start=window,request_body_bytes=len(body))
                    status,headers,raw=http(app+'/v1/messages',body,{'Authorization':'Bearer '+api_key,'Content-Type':'application/json','anthropic-version':'2023-06-01','x-request-id':request_id})
                    (evidence/'response.json').write_bytes(raw)
                    require(status==200,'gateway request failed: '+str(status)+' '+raw[:1000].decode(errors='replace'))
                    response=json.loads(raw)
                    require(''.join(item.get('text','') for item in response.get('content',[]))=='v2smoke-ok','mock response content changed')
                    require(response.get('usage',{}).get('input_tokens')==11 and response['usage'].get('output_tokens')==7,'response usage differs from mock')
                    response_id=next((value for key,value in headers.items() if key.lower()=='x-request-id'),None)
                    require(response_id==request_id,'gateway response did not preserve x-request-id')
                    report['checks']['http_response']='PASS'
                    # Poll asynchronous Redis ring buffer + PG usage_logs persistence.
                    deadline=time.monotonic()+25;samples=[];usage=[]
                    while time.monotonic()<deadline:
                        samples=[s for s in redis_samples() if s.get('request_id')==request_id]
                        usage=json.loads(sql(f"SELECT coalesce(json_agg(row_to_json(u)),'[]'::json) FROM (SELECT request_id,input_tokens,output_tokens,body_bytes,gateway_latency_ms,total_cost,actual_cost FROM usage_logs WHERE api_key_id={ids['api_key_id']} AND created_at>='{window}'::timestamptz)u;"))
                        if len(samples)>1:raise SmokeFailure('duplicate Redis sample rows for exact request ID: '+str(len(samples)))
                        if len(samples)==1 and len(usage)==1:break
                        time.sleep(.5)
                    report['sample_rows']=samples;report['usage_rows']=usage
                    require(len(samples)==1,'exact x-request-id Redis sample count='+str(len(samples))+' (requires 1; internal-* fallback is not accepted)')
                    sample=samples[0]
                    for key,value in ids.items():require(sample.get(key)==value,'sample '+key+' does not match seeded identity')
                    require(sample.get('model')==MODEL and sample.get('platform')=='anthropic' and sample.get('endpoint')=='/v1/messages','sample routing/model metadata differs from request')
                    require(sample.get('account_name')=='smoke_account' and sample.get('api_key_name')=='v2smoke_key','sample display metadata missing/wrong')
                    require(sample.get('bytes_resp_downstream')==len(raw),'sample downstream bytes differ from actual response')
                    require(sample.get('result')=='success' and sample.get('http_status')==200,'sample did not record success/200')
                    require(sample.get('attempt_count')==1 and sample.get('failed_attempts')==0,'unexpected attempt settlement')
                    require(sample.get('bytes_in_wire')==len(body),'sample in_wire differs from actual request body')
                    require(len(usage)==1,'usage row count='+str(len(usage))+' (requires 1)')
                    require(usage[0]['input_tokens']==11 and usage[0]['output_tokens']==7,'stored usage differs from mock')
                    require(usage[0]['body_bytes']==len(body) and usage[0]['gateway_latency_ms'] is not None,'usage snapshot fields absent/wrong')
                    report['checks']['redis_exact_sample']='PASS'
                    report['checks']['stored_usage']='PASS'
                    deadline=time.monotonic()+95;text=''
                    while time.monotonic()<deadline:
                        code,_,raw_metrics=http(metrics_url);require(code==200,'metrics endpoint unavailable')
                        text=raw_metrics.decode()
                        coverage=metric_samples(text,'sub2api_metric_coverage_ratio',{'kind':'request'})
                        if any(value>0 for value in coverage):break
                        time.sleep(1)
                    (evidence/'metrics.txt').write_text(text)
                    for phase in ['body_read','gateway','wait','upstream','response','finish','headers_end']:
                        require(any(value>0 for value in metric_samples(text,'sub2api_request_phase_seconds_count',{'phase':phase})),'missing/nonpositive phase observation: '+phase)
                    require(metric_samples(text,'sub2api_gateway_requests_total',{'result':'success','failure_reason':''})==[1.0],'expected exactly one successful gateway request metric')
                    require(metric_samples(text,'sub2api_gateway_request_duration_seconds_count',{})==[1.0],'expected gateway total duration histogram sample')
                    require(metric_samples(text,'sub2api_app_redis_up',{})==[1.0],'expected sub2api_app_redis_up == 1')
                    require(any(value>=1.0 for value in metric_samples(text,'sub2api_app_accounts_active',{})),'expected sub2api_app_accounts_active >= 1')
                    require(metric_samples(text,'sub2api_ops_eligible_requests_total',{})==[1.0],'expected exactly one completed Ops-eligible request')
                    require(metric_samples(text,'sub2api_upstream_requests_total',{'account':'smoke_account','result':'success','failure_reason':''})==[1.0],'specific account/result attempt metric missing or wrong')
                    require(metric_samples(text,'sub2api_upstream_ttft_seconds_count',{'account':'smoke_account'})==[1.0],'account TTFT observation missing')
                    require(any(value>0 for value in metric_samples(text,'sub2api_metric_coverage_ratio',{'kind':'request'})),'M9 request coverage must be >0')
                    require(metric_samples(text,'sub2api_model_requests_total',{'model_class':'claude','result':'success'})==[1.0],'original model class metric missing/wrong')
                    report['metric_evidence']=[line for line in text.splitlines() if line.startswith(('sub2api_request_phase_seconds_count{','sub2api_gateway_requests_total{','sub2api_gateway_request_duration_seconds_count','sub2api_app_','sub2api_upstream_requests_total{','sub2api_upstream_ttft_seconds_count{','sub2api_model_requests_total{','sub2api_metric_coverage_ratio{'))]
                    report['checks']['metrics_concrete_labels']='PASS';report['checks']['m9_request_positive']='PASS'
                    mock_requests=json.loads(http(mock+'/requests')[2]);report['mock_requests']=mock_requests
                    require(len(mock_requests)==1,'expected exactly one mock upstream call')
                    require(mock_requests[0]['body'].get('model')==MODEL,'upstream model differs from fixture')
                    require(request_id in json.dumps(mock_requests[0]['body'].get('messages')),'mock did not receive controlled request marker')
                    require(mock_requests[0]['api_key_present'],'upstream authentication header missing')
                    report['checks']['mock_request']='PASS'
                    # Recheck after metrics/coverage polling, catching delayed duplicates.
                    count=len([s for s in redis_samples() if s.get('request_id')==request_id])
                    require(count==1,'Redis sample count changed after settling: '+str(count))
                    if extension is not None:extension.verify(http,sql,report,evidence,api_key)
                    report['status']='PASS'
                    print('CHECKS PASS: HTTP/mock/usage; concrete phase/account/result labels; exact Redis sample=1; M9 request>0',flush=True)
                    print('SAMPLE:',json.dumps(sample,ensure_ascii=False,default=str),flush=True)
                    print('M9_REQUEST:',metric_samples(text,'sub2api_metric_coverage_ratio',{'kind':'request'}),flush=True)
            except Exception as error:
                failure=sanitize(str(error));report['error']=failure
                print('FAIL:',failure,flush=True)
                if started:
                    report['recent_sample_diagnostic']=run(compose+['exec','-T','redis','redis-cli','--raw','LRANGE','sub2api:ops:realtime_requests','0','4'],check=False).stdout
            finally:
                if started:
                    logs=run(compose+['logs','--no-color','--tail','250'],check=False)
                    (evidence/'compose.log').write_text(sanitize(logs.stdout+'\n'+logs.stderr))
                    cleanup=run(compose+['--profile','*','down','--volumes','--remove-orphans','--timeout','20'],check=False)
                    (evidence/'cleanup.log').write_text(sanitize(cleanup.stdout+'\n'+cleanup.stderr))
                    remaining=run(['docker','ps','-aq','--filter','label=com.docker.compose.project='+project],check=False).stdout.strip()
                    volumes=run(['docker','volume','ls','-q','--filter','label=com.docker.compose.project='+project],check=False).stdout.strip()
                    networks=run(['docker','network','ls','-q','--filter','label=com.docker.compose.project='+project],check=False).stdout.strip()
                    report['cleanup']=cleanup.returncode==0 and not remaining and not volumes and not networks
                    if not report['cleanup']:
                        report['status']='FAIL';failure=failure or 'isolated project cleanup failed'
                    print('CLEANUP:',report['cleanup'],'project='+project,flush=True)
    except Exception as error:
        failure=sanitize(str(error));report['error']=failure;print('FAIL:',failure,flush=True)
    report['temporary_config_removed']=work is None or not work.exists()
    if not report['temporary_config_removed']:
        failure=failure or 'temporary Compose config directory was not removed'
    report['completed_at']=datetime.now(timezone.utc).isoformat()
    if failure:report['status']='FAIL'
    (evidence/'report.json').write_text(sanitize(json.dumps(report,ensure_ascii=False,indent=2,default=str))+'\n')
    print('RESULT:',report['status'],'TEMP_CONFIG_REMOVED:',report['temporary_config_removed'],flush=True)
    print('EVIDENCE:',evidence,flush=True)
    if report['status']!='PASS':
        print('DIAGNOSE (no old/internal request ID counts as PASS):',flush=True)
        print('  python3 -m json.tool '+shlex.quote(str(evidence/'report.json')),flush=True)
        print('  tail -n 120 '+shlex.quote(str(evidence/'compose.log')),flush=True)
        print('  python3 '+shlex.quote(str(Path(__file__).resolve()))+' --image '+shlex.quote(args.image),flush=True)
    return 0 if report['status']=='PASS' and report['cleanup'] else 1


if __name__=='__main__':sys.exit(main())
