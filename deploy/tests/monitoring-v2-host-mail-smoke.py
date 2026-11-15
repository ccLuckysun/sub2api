#!/usr/bin/env python3
"""AC-6/7: W6c isolated node metrics + real Alertmanager -> local SMTP DATA.

--image required. No published ports, host root mounts, SMTP auth or relay.
Node resource scope is Docker container/VM, not the production host. AC-6 saves
instant and range responses. AC-7 sends a unique synthetic alert over the real
Alertmanager v2 API and verifies actual firing and resolved MIME messages.
"""
import base64
from datetime import datetime,timedelta,timezone
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
smoke=budget.smoke
require=budget.require


class HostMailProbe(budget.BudgetProbe):
    def configure(self,config,work,evidence,report):
        super().configure(config,work,evidence,report);report.pop('W6c')
        self.alert_id='ac7-'+str(time.time_ns())+'-'+secrets.token_hex(3)
        self.evidence=evidence
        config['services']['smtp']={'image':smoke.MOCK_IMAGE,'command':['python','/fixture/smtp.py'],
            'volumes':[str(smoke.ROOT/'deploy/tests/fixtures/monitoring-local-smtp.py')+':/fixture/smtp.py:ro']}
        alert={'route':{'receiver':'blackhole','group_by':['alertname','test_id'],'group_wait':'1s','group_interval':'2s','repeat_interval':'1h',
                        'routes':[{'receiver':'local-smtp','matchers':['alertname="AC7LocalSMTP"','test_id="'+self.alert_id+'"']}]},
               'receivers':[{'name':'blackhole'},{'name':'local-smtp','email_configs':[{'to':'inbox@example.invalid','from':'alerts@example.invalid',
                  'smarthost':'smtp:1025','require_tls':False,'send_resolved':True,
                  'headers':{'Subject':'AC7 {{ .Status }} {{ .CommonLabels.test_id }}'},
                  'text':'status={{ .Status }} test_id={{ .CommonLabels.test_id }} alerts={{ len .Alerts }}',
                  'html':'status={{ .Status }} test_id={{ .CommonLabels.test_id }}'}]}]}
        (work/'alertmanager.json').write_text(json.dumps(alert))
        (evidence/'ac7-alertmanager-config.json').write_text(json.dumps(alert,indent=2)+'\n')
        report['AC-6']={'status':'未执行','scope':'default node-exporter Docker container/VM view; no production host/root mounts; Prom instant and 30s range'}
        report['AC-7']={'status':'未执行','test_id':self.alert_id,'smtp':'smtp:1025','recipient':'inbox@example.invalid',
                       'scope':'real local SMTP DATA only; no relay/auth/TLS/external delivery'}

    def verify(self,http,sql,report,evidence,api_key):
        sequence=0
        def fetch(url,name,body=None):
            nonlocal sequence
            sequence+=1
            status,_,raw=http(url,body,{'Content-Type':'application/json'} if body is not None else None)
            filename=f'host-mail-{sequence:03d}-{name}.json';(evidence/filename).write_bytes(raw)
            with (evidence/'host-mail-requests.jsonl').open('a') as f:f.write(json.dumps({'at':datetime.now(timezone.utc).isoformat(),'url':url,'status':status,'file':filename})+'\n')
            require(200<=status<300,'HTTP '+str(status)+'; see '+filename)
            return json.loads(raw) if raw else None,filename

        def query(expression,name):
            data,file=fetch('http://prometheus:9090/api/v1/query?'+urlencode({'query':expression}),name)
            require(data.get('status')=='success','Prom query failed: '+file)
            return data['data']['result'],file

        deadline=time.monotonic()+90
        while True:
            rows,_=query('sum_over_time(up{job="node"}[2m])','node-successful-scrapes')
            if rows and float(rows[0]['value'][1])>=3:break
            require(time.monotonic()<deadline,'node target lacks three successful scrapes');time.sleep(2)
        expressions={
            'cpu_busy_percent':'100 * (1 - avg(rate(node_cpu_seconds_total{job="node",mode="idle"}[1m])))',
            'cpu_cores':'count(node_cpu_seconds_total{job="node",mode="idle"})',
            'memory_total_bytes':'node_memory_MemTotal_bytes{job="node"}',
            'memory_available_bytes':'node_memory_MemAvailable_bytes{job="node"}',
            'memory_used_percent':'100 * (1 - node_memory_MemAvailable_bytes{job="node"} / node_memory_MemTotal_bytes{job="node"})',
            'filesystem_size_bytes':'node_filesystem_size_bytes{job="node",fstype!~"tmpfs|devtmpfs|proc|sysfs|cgroup2?|squashfs|nsfs"}',
            'filesystem_available_bytes':'node_filesystem_avail_bytes{job="node",fstype!~"tmpfs|devtmpfs|proc|sysfs|cgroup2?|squashfs|nsfs"}',
            'disk_read_bytes_per_second':'rate(node_disk_read_bytes_total{job="node"}[1m])',
            'disk_write_bytes_per_second':'rate(node_disk_written_bytes_total{job="node"}[1m])',
            'network_receive_bytes_per_second':'rate(node_network_receive_bytes_total{job="node",device!="lo"}[1m])',
            'network_transmit_bytes_per_second':'rate(node_network_transmit_bytes_total{job="node",device!="lo"}[1m])',
            'load1':'node_load1{job="node"}','load5':'node_load5{job="node"}','load15':'node_load15{job="node"}'}
        info=report['AC-6'];info['metrics']={};instant={}
        for name,expression in expressions.items():
            rows,file=query(expression,name)
            require(rows,'empty node metric: '+name)
            numbers=[float(row['value'][1]) for row in rows]
            require(all(math.isfinite(v) and v>=-1e-6 for v in numbers),'nonfinite/negative node metric: '+name)
            if name.endswith('_percent'):require(all(v<=100+1e-6 for v in numbers),'resource percentage out of bounds: '+name)
            if name in ('cpu_cores','memory_total_bytes','filesystem_size_bytes'):require(all(v>0 for v in numbers),'nonpositive capacity: '+name)
            instant[name]=rows;info['metrics'][name]={'series':len(rows),'min':min(numbers),'max':max(numbers),'raw_file':file}
            end=time.time()
            data,rangefile=fetch('http://prometheus:9090/api/v1/query_range?'+urlencode({'query':expression,'start':end-30,'end':end,'step':15}),name+'-range')
            require(data.get('status')=='success' and data['data']['result'],'empty resource curve: '+name)
            for row in data['data']['result']:
                require(len(row['values'])>=2 and all(math.isfinite(float(value)) and float(value)>=-1e-6 for _,value in row['values']),'invalid resource curve: '+name)
            info['metrics'][name]['range_file']=rangefile
        def indexed(rows):return {tuple(sorted((k,v) for k,v in row['metric'].items() if k!='__name__')):float(row['value'][1]) for row in rows}
        for capacity,available in [('memory_total_bytes','memory_available_bytes'),('filesystem_size_bytes','filesystem_available_bytes')]:
            sizes=indexed(instant[capacity]);avails=indexed(instant[available])
            require(set(sizes)==set(avails) and all(0<=avails[key]<=sizes[key] for key in sizes),'available capacity exceeds total/mismatched devices: '+capacity)
        info['status']='PASS (Docker container/VM scope)'
        print('AC-6 PASS',json.dumps(info),flush=True)

        # Matching unique route is the only receiver capable of sending mail.
        mailbox,_=fetch('http://smtp:8025/messages','mailbox-empty');require(mailbox==[],'mailbox not initially empty')
        now=datetime.now(timezone.utc);starts=(now-timedelta(seconds=5)).isoformat()
        labels={'alertname':'AC7LocalSMTP','test_id':self.alert_id,'severity':'info','instance':'isolated'}
        alert={'labels':labels,'annotations':{'summary':'isolated SMTP firing/resolved smoke'},'startsAt':starts,'endsAt':(now+timedelta(minutes=5)).isoformat(),'generatorURL':'http://prometheus:9090/graph'}
        (evidence/'ac7-firing-payload.json').write_text(json.dumps([alert],indent=2)+'\n')
        fetch('http://alertmanager:9093/api/v2/alerts','post-firing',json.dumps([alert]).encode())
        active,activefile=fetch('http://alertmanager:9093/api/v2/alerts','active-alert')
        require(any(a['labels'].get('test_id')==self.alert_id and a['status']['state']=='active' for a in active),'synthetic alert not active')
        def wait_mail(state):
            deadline=time.monotonic()+60
            while True:
                messages,file=fetch('http://smtp:8025/messages','mailbox-'+state)
                matches=[m for m in messages if self.alert_id in m['subject'] and 'status='+state in m['text']]
                if matches:
                    require(len(matches)==1,'duplicate mail: '+state)
                    message=matches[0]
                    require(message['mail_from']=='alerts@example.invalid' and message['rcpt_to']==['inbox@example.invalid'],'unexpected SMTP envelope')
                    (evidence/('ac7-'+state+'.eml')).write_bytes(base64.b64decode(message['raw_base64']))
                    return message,file
                require(time.monotonic()<deadline,'local SMTP mail not received: '+state);time.sleep(1)
        firing,firingfile=wait_mail('firing')
        alert['endsAt']=(datetime.now(timezone.utc)-timedelta(seconds=1)).isoformat()
        (evidence/'ac7-resolved-payload.json').write_text(json.dumps([alert],indent=2)+'\n')
        fetch('http://alertmanager:9093/api/v2/alerts','post-resolved',json.dumps([alert]).encode())
        resolved,resolvedfile=wait_mail('resolved')
        final,finalfile=fetch('http://alertmanager:9093/api/v2/alerts','after-resolve')
        for item in final:
            if item['labels'].get('test_id')==self.alert_id:
                require(datetime.fromisoformat(item['endsAt'].replace('Z','+00:00'))<=datetime.now(timezone.utc),'alert still firing after resolved mail')
        report['AC-7'].update(status='PASS (local SMTP firing + resolved)',firing_message_id=firing['id'],resolved_message_id=resolved['id'],
                              firing_received_at=firing['received_at'],resolved_received_at=resolved['received_at'],
                              firing_subject=firing['subject'],resolved_subject=resolved['subject'],raw_files=[activefile,firingfile,resolvedfile,finalfile])
        print('AC-7 PASS',json.dumps(report['AC-7']),flush=True)


if __name__=='__main__':sys.exit(smoke.main(extension=HostMailProbe()))
