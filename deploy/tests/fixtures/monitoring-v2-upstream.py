#!/usr/bin/env python3
"""Isolated Anthropic mock. No outbound requests or real credentials."""
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import json
import re
import threading
import time
import urllib.parse

RESPONSE_TEXT='v2smoke-ok'
REQUESTS=[]
LOCK=threading.Lock()


class Mock(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'

    def log_message(self,*args):
        pass

    def send(self,status,payload,content_type='application/json'):
        raw=payload if isinstance(payload,bytes) else json.dumps(payload,separators=(',',':')).encode()
        self.send_response(status)
        self.send_header('Content-Type',content_type)
        self.send_header('Content-Length',str(len(raw)))
        self.send_header('request-id','mock-v2smoke-upstream')
        self.end_headers()
        self.wfile.write(raw)
        self.wfile.flush()

    def do_GET(self):
        if self.path=='/health':self.send(200,{'ok':True})
        elif self.path=='/requests':
            with LOCK:requests=list(REQUESTS)
            self.send(200,requests)
        else:self.send(404,{'error':'fixture route not found'})

    def do_POST(self):
        try:
            size=int(self.headers.get('Content-Length','0'))
            if size<=0 or size>1024*1024:raise ValueError('invalid request length')
            body=json.loads(self.rfile.read(size))
        except (ValueError,json.JSONDecodeError):
            self.send(400,{'type':'error','error':{'type':'invalid_request_error','message':'invalid fixture input'}})
            return
        with LOCK:
            REQUESTS.append({'path':self.path,'body':body,'request_id':self.headers.get('x-request-id'),
                             'api_key_present':bool(self.headers.get('x-api-key'))})
        if urllib.parse.urlsplit(self.path).path!='/v1/messages':
            self.send(404,{'type':'error','error':{'type':'not_found_error','message':'fixture supports only /v1/messages'}})
            return
        # Explicit test marker only; normal W6b requests keep zero injected delay.
        messages=body.get('messages',[])
        content=messages[0].get('content','') if messages else ''
        delay=re.fullmatch(r'v2smoke-delay-(\d{1,4})-[a-zA-Z0-9-]+',content) if isinstance(content,str) else None
        if delay:time.sleep(min(int(delay[1]),5000)/1000)
        ac5=re.fullmatch(r'ac5-(smoke_account|acct\d\d)-(ok|fail)-[a-zA-Z0-9-]+',content) if isinstance(content,str) else None
        if ac5:
            number=1 if ac5[1]=='smoke_account' else int(ac5[1][-2:])
            time.sleep(number*.05)
            if ac5[2]=='fail':
                self.send(400,{'type':'error','error':{'type':'invalid_request_error','message':'ac5 controlled rejection'}})
                return
        message={'id':'msg_v2smoke','type':'message','role':'assistant','model':body.get('model'),
                 'content':[{'type':'text','text':RESPONSE_TEXT}], 'stop_reason':'end_turn','stop_sequence':None,
                 'usage':{'input_tokens':11,'output_tokens':7}}
        if body.get('stream'):
            start=dict(message,content=[],stop_reason=None,usage={'input_tokens':11,'output_tokens':0})
            events=[('message_start',{'type':'message_start','message':start}),
                    ('content_block_start',{'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}}),
                    ('content_block_delta',{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':RESPONSE_TEXT}}),
                    ('content_block_stop',{'type':'content_block_stop','index':0}),
                    ('message_delta',{'type':'message_delta','delta':{'stop_reason':'end_turn','stop_sequence':None},'usage':{'output_tokens':7}}),
                    ('message_stop',{'type':'message_stop'})]
            raw=''.join('event: '+name+'\ndata: '+json.dumps(payload)+'\n\n' for name,payload in events).encode()
            self.send(200,raw,'text/event-stream')
        else:self.send(200,message)


if __name__=='__main__':ThreadingHTTPServer(('0.0.0.0',8000),Mock).serve_forever()
