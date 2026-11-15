#!/usr/bin/env python3
"""Local-only SMTP sink + read-only mailbox API, no relay or outbound client.

Accepts only alerts@example.invalid -> inbox@example.invalid; stores raw SMTP
DATA bytes and decoded MIME for Alertmanager firing/resolved assertions.
"""
import base64
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import json
import re
import socketserver
import threading
from datetime import datetime,timezone

MESSAGES=[]
LOCK=threading.Lock()


class SMTP(socketserver.StreamRequestHandler):
    def reply(self,line):self.wfile.write((line+'\r\n').encode());self.wfile.flush()

    def handle(self):
        self.connection.settimeout(30);sender=None;recipients=[]
        self.reply('220 local-sink ESMTP')
        while True:
            raw=self.rfile.readline(65536)
            if not raw:return
            line=raw.decode(errors='replace').strip();command=line.split(' ',1)[0].upper()
            if command in ('EHLO','HELO'):
                self.reply('250-local-sink');self.reply('250 8BITMIME')
            elif command=='MAIL':
                address=re.search(r'<([^>]+)>',line)
                if not address or address[1].lower()!='alerts@example.invalid':self.reply('550 local test sender only');continue
                sender=address[1].lower();recipients=[];self.reply('250 sender accepted')
            elif command=='RCPT':
                address=re.search(r'<([^>]+)>',line)
                if sender is None or not address or address[1].lower()!='inbox@example.invalid':self.reply('550 local test recipient only');continue
                recipients.append(address[1].lower());self.reply('250 recipient accepted')
            elif command=='DATA':
                if not sender or not recipients:self.reply('503 need MAIL and RCPT');continue
                self.reply('354 end with dot');lines=[];length=0
                while True:
                    chunk=self.rfile.readline(65536)
                    if not chunk:return
                    if chunk in (b'.\r\n',b'.\n'):break
                    if chunk.startswith(b'..'):chunk=chunk[1:]
                    length+=len(chunk)
                    if length>1024*1024:self.reply('552 message too large');return
                    lines.append(chunk)
                data=b''.join(lines);message=BytesParser(policy=policy.default).parsebytes(data)
                text=message.get_body(preferencelist=('plain',)) if message.is_multipart() else message
                with LOCK:
                    MESSAGES.append({'id':len(MESSAGES)+1,'received_at':datetime.now(timezone.utc).isoformat(),'mail_from':sender,'rcpt_to':list(recipients),
                                     'subject':str(message.get('Subject','')),'text':text.get_content() if text else '',
                                     'raw_base64':base64.b64encode(data).decode()})
                self.reply('250 queued locally');sender=None;recipients=[]
            elif command=='RSET':sender=None;recipients=[];self.reply('250 reset')
            elif command=='NOOP':self.reply('250 ok')
            elif command=='QUIT':self.reply('221 bye');return
            else:self.reply('502 unsupported command')


class Mailbox(BaseHTTPRequestHandler):
    def log_message(self,*args):pass

    def do_GET(self):
        if self.path=='/health':payload={'ok':True}
        elif self.path=='/messages':
            with LOCK:payload=list(MESSAGES)
        else:self.send_error(404);return
        raw=json.dumps(payload).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)))
        self.end_headers();self.wfile.write(raw)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address=True
    daemon_threads=True


if __name__=='__main__':
    smtp=Server(('0.0.0.0',1025),SMTP)
    threading.Thread(target=smtp.serve_forever,daemon=True).start()
    ThreadingHTTPServer(('0.0.0.0',8025),Mailbox).serve_forever()
