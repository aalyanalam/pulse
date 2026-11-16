"""Verify comparison math through real ingestion and PostgreSQL queries."""
import datetime as dt
import json
import urllib.request
import urllib.parse
import uuid

BASE = 'http://127.0.0.1:8080'
def request(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    with urllib.request.urlopen(urllib.request.Request(BASE+path, data=data, headers={'Content-Type':'application/json'}), timeout=10) as r:
        return json.load(r)

node = 'compare-' + uuid.uuid4().hex[:12]
start = dt.datetime.now(dt.timezone.utc)-dt.timedelta(minutes=2)
def stamp(seconds):
    return (start+dt.timedelta(seconds=seconds)).isoformat()
samples = []
for version, process, offset, counts in [
    ('v1','a',0,[(0,0,0),(10,0,100),(40,0,400)]),
    ('v2-slow','b',12,[(0,0,0),(10,0,2000),(40,0,8000)]),
    ('v2-errors','c',24,[(0,0,0),(10,3,100),(40,12,400)]),
    ('v1','d',36,[(100,0,1000),(110,0,1100)]),
]:
    for i,(n,e,l) in enumerate(counts):
        samples.append(dict(node_id=node,session_id='test',sequence=len(samples)+1,collected_at=stamp(offset+3*i),version=version,app_process_id=process,requests=n,errors=e,total_latency_ms=l,memory_bytes=0))
assert request('/api/v1/telemetry/batches',{'samples':list(reversed(samples))})['inserted']==len(samples)
assert request('/api/v1/telemetry/batches',{'samples':samples})['inserted']==0

def compare(version, lo=0, hi=45):
    query=urllib.parse.urlencode(dict(baseline_version='v1',baseline_from=stamp(lo),baseline_to=stamp(hi),candidate_version=version,candidate_from=stamp(lo),candidate_to=stamp(hi)))
    return request(f'/api/v1/nodes/{node}/compare?'+query)
result=compare('v2-slow')
assert result['baseline']['requests']==50, result
assert result['baseline']['mean_latency_ms']==10, result
assert result['candidate']['requests']==40, result
assert result['candidate']['mean_latency_ms']==200, result
assert compare('v2-errors')['candidate']['error_rate']==0.3
assert compare('absent')['candidate']['mean_latency_ms'] is None
# Half-open bounds exclude the sample at second 6; no preceding sample is borrowed.
assert compare('v1',0,6)['baseline']['requests']==10
assert compare('v1',1,7)['baseline']['requests']==30
print('PASS: comparison weighting, versions, restarts, boundaries, empty evidence, delayed and duplicate ingestion')
