"""Exercise a running Compose stack using only Python's standard library."""
import json
import time
import urllib.request
import urllib.error
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode
from uuid import uuid4


def request(path, body=None, port=8080):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as response:
        payload = response.read()
        return json.loads(payload) if payload else None


def wait_for(check, seconds=30):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (urllib.error.URLError, TimeoutError) as error:
            last = error
        time.sleep(0.25)
    raise AssertionError(f"condition not met within {seconds}s; last error: {last}")


wait_for(lambda: request("/readyz"))
for _ in range(20):
    with urllib.request.urlopen("http://127.0.0.1:8081/work", timeout=5) as response:
        assert response.status == 200

now = datetime.now(timezone.utc)
query = urlencode({"from": (now - timedelta(hours=1)).isoformat(),
                   "to": (now + timedelta(minutes=1)).isoformat(), "limit": 1000})
wait_for(lambda: any(s["requests"] >= 20 for s in request(f"/api/v1/nodes/node-1/metrics?{query}")))

node = "smoke-" + uuid4().hex
sample = {"node_id": node, "session_id": uuid4().hex, "sequence": 1,
          "collected_at": now.isoformat(), "version": "v2", "requests": 10,
          "errors": 1, "total_latency_ms": 25, "memory_bytes": 1024}
batch = {"samples": [sample]}
assert request("/api/v1/telemetry/batches", batch)["inserted"] == 1
assert request("/api/v1/telemetry/batches", batch)["inserted"] == 0
older = dict(sample, sequence=2, version="v1",
             collected_at=(now - timedelta(minutes=1)).isoformat())
assert request("/api/v1/telemetry/batches", {"samples": [older]})["inserted"] == 1
samples = request(f"/api/v1/nodes/{node}/metrics?{query}")
assert [s["version"] for s in samples] == ["v1", "v2"], samples
nodes = request("/api/v1/nodes?" + urlencode({"after": node[:-1]}))
assert next(n for n in nodes if n["id"] == node)["version"] == "v2"
print("PASS: application → agent → PostgreSQL → API, duplicate ingestion, event ordering and version history")
