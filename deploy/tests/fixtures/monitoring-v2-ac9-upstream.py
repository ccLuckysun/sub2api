#!/usr/bin/env python3
"""Fixed JSON/SSE/abort replay source, exact body + hashed credential headers."""
import base64
import hashlib
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import json
import threading
import time

RECORDS=[]
LOCK=threading.Lock()


class Mock(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass

    def do_GET(self):
        payload={'ok':True} if self.path=='/health' else RECORDS if self.path=='/requests' else None
        if payload is None:self.send_error(404);return
        with LOCK:raw=json.dumps(payload).encode()
        self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)

    def do_POST(self):
        raw=self.rfile.read(int(self.headers['Content-Length']));body=json.loads(raw)
        marker=body['messages'][0]['content'];case=marker.split('-')[-1]
        headers=[]
        for name,value in self.headers.raw_items():
            if name.lower() in ('authorization','x-api-key'):
                value='sha256:'+hashlib.sha256(value.encode()).hexdigest()+':length='+str(len(value))
            headers.append([name,value])
        record={'path':self.path,'method':self.command,'case':case,'headers':headers,'body_base64':base64.b64encode(raw).decode(),'state':'sending','writes':[]}
        with LOCK:RECORDS.append(record)
        message={'id':'msg_ac9','type':'message','role':'assistant','model':body['model'],'content':[{'type':'text','text':'ac9-ok'}],
                 'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':11,'output_tokens':7}}
        self.send_response(200);self.send_header('request-id','mock-ac9-fixed')
        try:
            if not body.get('stream'):
                payload=json.dumps(message,separators=(',',':')).encode()
                self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers()
                self.wfile.write(payload);self.wfile.flush();record['writes'].append('json')
            else:
                self.send_header('Content-Type','text/event-stream');self.send_header('Transfer-Encoding','chunked');self.end_headers()
                start=dict(message,content=[],stop_reason=None,usage={'input_tokens':11,'output_tokens':0})
                events=[('message_start',{'type':'message_start','message':start}),
                        ('content_block_start',{'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}}),
                        ('content_block_delta',{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':'ac9-ok'}}),
                        ('content_block_stop',{'type':'content_block_stop','index':0}),
                        ('message_delta',{'type':'message_delta','delta':{'stop_reason':'end_turn','stop_sequence':None},'usage':{'output_tokens':7}})]
                if case=='abort':events += [('ping',{'type':'ping','sequence':i}) for i in range(6)]
                events.append(('message_stop',{'type':'message_stop'}))
                for index,(kind,event) in enumerate(events):
                    if index:time.sleep(.2)
                    payload=('event: '+kind+'\ndata: '+json.dumps(event,separators=(',',':'))+'\n\n').encode()
                    self.wfile.write(f'{len(payload):x}\r\n'.encode()+payload+b'\r\n');self.wfile.flush()
                    record['writes'].append(kind)
                self.wfile.write(b'0\r\n\r\n');self.wfile.flush()
            record['state']='complete'
        except (BrokenPipeError,ConnectionResetError):record['state']='client_closed'


if __name__=='__main__':ThreadingHTTPServer(('0.0.0.0',8000),Mock).serve_forever()
