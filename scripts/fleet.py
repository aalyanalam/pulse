"""Check fleet status without confusing historical telemetry with heartbeats."""
import datetime as dt
import json
import urllib.request
import uuid

base='http://127.0.0.1:8080/api/v1/'
def call(path,body=None):
    data=None if body is None else json.dumps(body).encode()
    with urllib.request.urlopen(urllib.request.Request(base+path,data=data,headers={'Content-Type':'application/json'}),timeout=10) as r:
        return json.load(r)
node='fleet-'+uuid.uuid4().hex[:12]
sample=dict(node_id=node,session_id='test',sequence=1,collected_at=dt.datetime.now(dt.timezone.utc).isoformat(),version='v1',app_process_id='test',requests=0,errors=0,total_latency_ms=0,memory_bytes=1024)
call('telemetry/batches',{'samples':[sample]})
def row():
    return next(n for n in call('fleet?after='+node[:-1]) if n['id']==node)
assert row()['status']=='unknown' and row()['last_seen'] is None
call('nodes/'+node+'/heartbeat',{'application_healthy':True})
assert row()['status']=='reporting' and row()['memory_bytes']==1024
for _ in range(3): call('nodes/'+node+'/heartbeat',{'application_healthy':False})
assert row()['status']=='incident'
for _ in range(3): call('nodes/'+node+'/heartbeat',{'application_healthy':True})
assert row()['status']=='reporting'
print('PASS: fleet unknown identity, heartbeat reporting, application failure, and recovery')
