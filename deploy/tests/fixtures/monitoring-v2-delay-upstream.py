#!/usr/bin/env python3
"""AC-1 response-body delay and account-holder fixture; local-only."""
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import json
import threading
import time

REQUESTS=[]
LOCK=threading.Lock()


class Mock(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def do_GET(self):
        with LOCK:raw=json.dumps(REQUESTS if self.path=='/requests' else {'ok':True}).encode()
        self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
    def do_POST(self):
        raw=self.rfile.read(int(self.headers['Content-Length']));body=json.loads(raw);marker=body['messages'][0]['content']
        record={'request_id':marker,'body':body,'api_key_present':bool(self.headers.get('x-api-key')),'received_at_unix':time.time(),'state':'active','body_chunks':[]}
        with LOCK:REQUESTS.append(record)
        if marker.startswith('ac1-holder-'):time.sleep(4)
        payload=json.dumps({'id':'msg_v2smoke','type':'message','role':'assistant','model':body['model'],
            'content':[{'type':'text','text':'v2smoke-ok'}],'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':11,'output_tokens':7}},separators=(',',':')).encode()
        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)))
        self.send_header('request-id','mock-delay-fixed');self.end_headers();self.wfile.flush();record['headers_sent_at_unix']=time.time()
        pieces=7 if marker.startswith('ac1-response-') else 1
        try:
            for index in range(pieces):
                if index:time.sleep(.5)
                part=payload[index*len(payload)//pieces:(index+1)*len(payload)//pieces]
                self.wfile.write(part);self.wfile.flush();record['body_chunks'].append({'bytes':len(part),'at_unix':time.time()})
            record['state']='complete';record['completed_at_unix']=time.time()
        except (BrokenPipeError,ConnectionResetError):record['state']='client_closed'


if __name__=='__main__':ThreadingHTTPServer(('0.0.0.0',8000),Mock).serve_forever()
