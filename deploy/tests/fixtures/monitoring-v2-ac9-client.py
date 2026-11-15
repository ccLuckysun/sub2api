#!/usr/bin/env python3
"""Record raw HTTP entity/chunks and SSE frame arrival; RST after usage event."""
import base64
import http.client
import json
import socket
import struct
import sys
import time

spec=json.load(sys.stdin);body=base64.b64decode(spec['body_base64']);headers=spec['headers']
sock=socket.create_connection((spec['host'],8080),timeout=15);stream=sock.makefile('rb');started=time.monotonic()
wire=('POST /v1/messages HTTP/1.1\r\n'+''.join(k+': '+v+'\r\n' for k,v in headers.items())+'Content-Length: '+str(len(body))+'\r\n\r\n').encode()+body
sock.sendall(wire)
status=stream.readline().decode().strip();response_headers=list(http.client.parse_headers(stream).items())
header_done=time.monotonic()-started;lookup={k.lower():v for k,v in response_headers};chunks=[];entity=b'';aborted=False;frame_times=[]
if lookup.get('transfer-encoding','').lower()=='chunked':
    while True:
        line=stream.readline();size=int(line.split(b';')[0].strip(),16)
        if size==0:
            while stream.readline() not in (b'\r\n',b'\n',b''):pass
            break
        chunk=stream.read(size);assert len(chunk)==size and stream.read(2)==b'\r\n'
        chunks.append({'base64':base64.b64encode(chunk).decode(),'at_seconds':time.monotonic()-started});entity+=chunk
        complete=entity.split(b'\n\n')[:-1]
        while len(frame_times)<len(complete):frame_times.append(time.monotonic()-started)
        if spec['case']=='abort' and any(b'event: message_delta\n' in frame for frame in complete):
            aborted=True;sock.setsockopt(socket.SOL_SOCKET,socket.SO_LINGER,struct.pack('ii',1,0));break
else:
    length=int(lookup['content-length']);entity=stream.read(length);assert len(entity)==length
stream.close();sock.close()
frames=entity.split(b'\n\n')[:-1] if 'text/event-stream' in lookup.get('content-type','') else []
print(json.dumps({'status_line':status,'status':int(status.split()[1]),'headers':response_headers,'body_base64':base64.b64encode(entity).decode(),
                  'chunks':chunks,'sse_frames_base64':[base64.b64encode(frame+b'\n\n').decode() for frame in frames],
                  'frame_arrival_seconds':frame_times,'headers_seconds':header_done,'elapsed_seconds':time.monotonic()-started,'client_rst':aborted}))
