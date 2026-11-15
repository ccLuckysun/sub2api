#!/usr/bin/env python3
"""AC-2 fixed raw-byte fixture: abort, conversion, signature retry, oversize."""
import base64
import hashlib
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import json
import re
import threading
import time

RECORDS=[]
LOCK=threading.Lock()


class Mock(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def do_GET(self):
        with LOCK:payload=list(RECORDS) if self.path=='/requests' else {'ok':True}
        raw=json.dumps(payload).encode();self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
    def do_POST(self):
        raw=self.rfile.read(int(self.headers['Content-Length']));body=json.loads(raw)
        match=re.search(r'ac2-\d+-[a-f0-9]+-(abort|conversion|retry|truncate)',json.dumps(body));marker=match[0] if match else 'unmarked';case=match[1] if match else 'normal'
        headers=[]
        for key,value in self.headers.raw_items():
            if key.lower() in ('x-api-key','authorization'):value='sha256:'+hashlib.sha256(value.encode()).hexdigest()
            headers.append([key,value])
        with LOCK:
            number=1+sum(r['request_id']==marker for r in RECORDS)
            record={'request_id':marker,'case':case,'physical_call':number,'path':self.path,'headers':headers,'request_body_base64':base64.b64encode(raw).decode(),
                    'response_chunks_base64':[],'state':'sending'}
            RECORDS.append(record)
        error=case=='retry' and number==1
        record['status']=400 if error else 200
        self.send_response(record['status']);self.send_header('request-id','mock-ac2-fixed')
        message={'id':'msg_ac2','type':'message','role':'assistant','model':body['model'],
            'content':[{'type':'text','text':'x'*8192 if case=='truncate' else 'ac2-ok'}],'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':11,'output_tokens':7}}
        stream=body.get('stream') and not error
        if stream:
            start=dict(message,content=[],stop_reason=None,usage={'input_tokens':11,'output_tokens':0})
            events=[('message_start',{'type':'message_start','message':start}),
                    ('content_block_start',{'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}}),
                    ('content_block_delta',{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':'ac2-ok'}}),
                    ('content_block_stop',{'type':'content_block_stop','index':0}),
                    ('message_delta',{'type':'message_delta','delta':{'stop_reason':'end_turn','stop_sequence':None},'usage':{'output_tokens':7}})]
            if case=='abort':events += [('ping',{'type':'ping','sequence':index}) for index in range(6)]
            events += [('message_stop',{'type':'message_stop'})]
            chunks=[('event: '+kind+'\ndata: '+json.dumps(event,separators=(',',':'))+'\n\n').encode() for kind,event in events]
            self.send_header('Content-Type','text/event-stream');self.send_header('Transfer-Encoding','chunked')
        else:
            payload={'error':{'type':'invalid_request_error','message':'Invalid signature in thinking block'}} if error else message
            chunks=[json.dumps(payload,separators=(',',':')).encode()]
            self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(chunks[0])))
        record['declared_response_bytes']=sum(map(len,chunks))
        record['planned_response_base64']=base64.b64encode(b''.join(chunks)).decode()
        self.end_headers()
        try:
            for index,chunk in enumerate(chunks):
                if case=='abort' and index:time.sleep(.2)
                self.wfile.write(f'{len(chunk):x}\r\n'.encode()+chunk+b'\r\n' if stream else chunk);self.wfile.flush()
                record['response_chunks_base64'].append(base64.b64encode(chunk).decode())
            if stream:self.wfile.write(b'0\r\n\r\n');self.wfile.flush()
            record['state']='complete'
        except (BrokenPipeError,ConnectionResetError):record['state']='peer_closed'


if __name__=='__main__':ThreadingHTTPServer(('0.0.0.0',8000),Mock).serve_forever()
