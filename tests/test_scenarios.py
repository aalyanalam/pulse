"""Compose fault scenarios. Run only against the local Pulse demo stack."""
import datetime as dt
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

from conftest import compose_env

ROOT = Path(__file__).resolve().parents[1]
BASE = "http://127.0.0.1:8080"


def compose(*args):
    subprocess.run(["docker", "compose", *args], cwd=ROOT, env=compose_env(), check=True, timeout=90)


def get(path):
    with urllib.request.urlopen(BASE + path, timeout=3) as response:
        return json.load(response)


def wait_for(label, condition, timeout=45):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = condition()
            if last:
                return last
        except (OSError, urllib.error.URLError) as error:
            last = repr(error)
        time.sleep(0.5)
    raise AssertionError(f"{label} timed out after {timeout}s; last result: {last}")


def incidents():
    return get('/api/v1/nodes/node-1/incidents')


def opened(kind, previous):
    return next((item for item in incidents() if item['kind'] == kind and item['id'] not in previous and item['resolved_at'] is None), None)


def resolved(incident_id):
    return next((item for item in incidents() if item['id'] == incident_id and item['resolved_at'] is not None), None)


def test_existing_smoke_and_duplicate_ingestion():
    subprocess.run([sys.executable, '-X', 'utf8', 'scripts/smoke.py'], cwd=ROOT, check=True, timeout=60)


def test_application_failure_and_recovery():
    previous = {item['id'] for item in incidents()}
    try:
        compose('stop', 'sampleapp')
        incident = wait_for('application failure', lambda: opened('application_unavailable', previous))
        assert not opened('agent_unreachable', previous), incidents()
    finally:
        compose('start', 'sampleapp')
    wait_for('application recovery', lambda: resolved(incident['id']))


def test_agent_disconnect_and_recovery():
    previous = {item['id'] for item in incidents()}
    try:
        compose('stop', 'agent')
        incident = wait_for('agent unreachable', lambda: opened('agent_unreachable', previous), 35)
    finally:
        compose('start', 'agent')
    wait_for('agent recovery', lambda: resolved(incident['id']))


def test_backend_outage_buffering():
    start = dt.datetime.now(dt.timezone.utc)
    try:
        compose('stop', 'backend')
        start = dt.datetime.now(dt.timezone.utc)
        time.sleep(12)  # Allow several scheduled collections while delivery is unavailable.
        end = dt.datetime.now(dt.timezone.utc)
    finally:
        compose('start', 'backend')
    wait_for('backend readiness', lambda: get('/readyz'))
    from urllib.parse import urlencode
    query = urlencode({'from': start.isoformat(), 'to': end.isoformat(), 'limit': 1000})
    recovered = wait_for('buffered samples', lambda: get('/api/v1/nodes/node-1/metrics?' + query))
    assert len(recovered) >= 2, recovered


def test_release_regressions_and_rollback():
    subprocess.run([sys.executable, '-X', 'utf8', 'scripts/regression.py'], cwd=ROOT, env=compose_env(), check=True, timeout=240)
