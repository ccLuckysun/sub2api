#!/usr/bin/env python3
"""Send one valid Content-Length request body in seven writes over three seconds."""
import base64
import http.client
import json
import socket
import sys
import time

spec=json.load(sys.stdin);body=base64.b64decode(spec['body_base64'])
connection=socket.create_connection(('sub2api',8080),timeout=20);stream=connection.makefile('rb');started=time.monotonic();writes=[]
headers='POST /v1/messages HTTP/1.1\r\nHost: sub2api:8080\r\n'+''.join(key+': '+value+'\r\n' for key,value in spec['headers'].items())+'Content-Length: '+str(len(body))+'\r\nConnection: close\r\n\r\n'
connection.sendall(headers.encode())
for index in range(7):
    if index:time.sleep(.5)
    part=body[index*len(body)//7:(index+1)*len(body)//7];connection.sendall(part)
    writes.append({'bytes':len(part),'at_seconds':time.monotonic()-started})
status=stream.readline().decode().strip();response_headers=list(http.client.parse_headers(stream).items());lookup={k.lower():v for k,v in response_headers}
response=stream.read(int(lookup['content-length']));stream.close();connection.close()
print(json.dumps({'status':int(status.split()[1]),'headers':response_headers,'response_base64':base64.b64encode(response).decode(),
                  'writes':writes,'upload_seconds':writes[-1]['at_seconds'],'elapsed_seconds':time.monotonic()-started}))
