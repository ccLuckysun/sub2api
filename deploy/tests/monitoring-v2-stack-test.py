#!/usr/bin/env python3
"""Boot only monitoring services on an isolated internal network; no app or mail.
Checks native Grafana provisioning and samples cgroup memory. Never a load test.
"""
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

ROOT=Path(__file__).resolve().parents[2]
PROJECT='w5-validation-'+secrets.token_hex(3)
PASSWORD=secrets.token_hex(16)
ENV=dict(os.environ,GRAFANA_ADMIN_PASSWORD=PASSWORD,GRAFANA_PG_PASSWORD='local-fixture-unused',POSTGRES_PASSWORD='local-fixture-unused')


def run(args,check=True):
    return subprocess.run(args,text=True,capture_output=True,check=check,env=ENV)


with tempfile.TemporaryDirectory(prefix='sub2api-w5-') as tmp:
    config=json.loads(run(['docker','compose','--project-directory',str(ROOT/'deploy'),'-f',str(ROOT/'deploy/docker-compose.dev.yml'),'-f',str(ROOT/'deploy/docker-compose.monitoring.yml'),'config','--format','json']).stdout)
    selected=['prometheus','grafana','alertmanager']
    config['services']={name:config['services'][name] for name in selected}
    for name,svc in config['services'].items():
        svc.pop('depends_on',None);svc.pop('container_name',None);svc['ports']=[]
    config['networks']={'sub2api-network':{'internal':True}}
    config['volumes']={name:{} for name in ['prometheus_data','grafana_data','alertmanager_data']}
    path=Path(tmp)/'compose.json';path.write_text(json.dumps(config));path.chmod(0o600)
    compose=['docker','compose','-p',PROJECT,'-f',str(path)]
    try:
        run(compose+['up','-d','--pull','never'])
        cid=run(compose+['ps','-q','grafana']).stdout.strip()
        auth=base64.b64encode(('admin:'+PASSWORD).encode()).decode()
        def get(endpoint):
            r=run(['docker','exec',cid,'wget','-q','-O','-','--header','Authorization: Basic '+auth,'http://localhost:3000'+endpoint],check=False)
            return json.loads(r.stdout) if r.returncode==0 else None
        for _ in range(120):
            data=get('/api/search?type=dash-db')
            if data and len(data)==6:break
            time.sleep(.5)
        else:raise RuntimeError('six dashboards were not provisioned')
        expected=['sub2api-v2-'+x for x in ['overview','requests','nodes','users','host','request']]
        assert sorted(x['uid'] for x in data)==sorted(expected), data
        for uid,n in zip(expected,[14,7,8,7,23,10]):
            d=get('/api/dashboards/uid/'+uid)['dashboard'];assert len(d['panels'])==n
        print('PASS: native Grafana provisioned exactly six V2 dashboards (14/7/8/7/23/10); V1 absent',flush=True)
        datasource=get('/api/datasources/uid/sub2api-pg')
        assert datasource['user']=='grafana_ro' and datasource['secureJsonFields']['password'] is True
        print('PASS: PG datasource provisioned with secure password and grafana_ro',flush=True)
        for index in range(3):
            if index:time.sleep(20)
            data=run(['docker','stats','--no-stream','--format','{{.Name}} {{.MemUsage}}']+run(compose+['ps','-q']).stdout.split()).stdout.strip()
            print('Memory sample '+str(index+1)+':\n'+data,flush=True)
        print('PASS: isolated startup/provisioning; 40s memory sampling is NOT long-duration RSS certification',flush=True)
    finally:
        run(compose+['down','--volumes','--remove-orphans'],check=False)
