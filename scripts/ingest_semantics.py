"""Check ingestion semantics against a running backend.

Covers the behaviors any ingestion implementation must preserve: retry and
in-batch duplicates (first occurrence wins), delayed samples that must not
overwrite newer node metadata, equal timestamps (first sample wins), batches
spanning several nodes, out-of-order samples within one batch, and accurate
`inserted` counts. Run it against each benchmark variant to show equivalence.
"""
import json
import urllib.request
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode
from uuid import uuid4

BASE = 'http://127.0.0.1:8080'
NOW = datetime.now(timezone.utc).replace(microsecond=0)


def call(path, body=None):
    request = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(),
                                     headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


def sample(node, session, sequence, seconds, version):
    return dict(node_id=node, session_id=session, sequence=sequence,
                collected_at=(NOW + timedelta(seconds=seconds)).isoformat(), version=version,
                app_process_id='p', requests=sequence, errors=0, total_latency_ms=sequence, memory_bytes=1)


def ingest(*samples):
    return call('/api/v1/telemetry/batches', {'samples': list(samples)})['inserted']


def node(node_id):
    found = [n for n in call('/api/v1/nodes?' + urlencode({'after': node_id[:-1]})) if n['id'] == node_id]
    assert found, f'node {node_id} missing'
    return found[0]['version'], datetime.fromisoformat(found[0]['latest_collected_at'])


def stored(node_id):
    query = urlencode({'from': (NOW - timedelta(hours=1)).isoformat(), 'to': (NOW + timedelta(hours=1)).isoformat(), 'limit': 100})
    return [(s['sequence'], s['version']) for s in call(f'/api/v1/nodes/{node_id}/metrics?{query}')]


def fresh():
    return 'semantics-' + uuid4().hex, uuid4().hex


def main():
    checks = []

    n, s = fresh()  # Retry duplicates and in-batch duplicates: first occurrence wins.
    assert ingest(sample(n, s, 1, 0, 'a'), sample(n, s, 1, 5, 'b'), sample(n, s, 2, 1, 'c')) == 2
    assert ingest(sample(n, s, 1, 0, 'a'), sample(n, s, 2, 1, 'c')) == 0
    assert stored(n) == [(1, 'a'), (2, 'c')], stored(n)
    assert node(n) == ('c', NOW + timedelta(seconds=1)), node(n)
    checks.append('duplicates within and across batches')

    n, s = fresh()  # A delayed sample is stored but does not overwrite newer metadata.
    assert ingest(sample(n, s, 2, 10, 'new')) == 1
    assert ingest(sample(n, s, 1, 0, 'old'), sample(n, s, 3, 5, 'older')) == 2
    assert node(n) == ('new', NOW + timedelta(seconds=10)), node(n)
    assert sorted(stored(n)) == [(1, 'old'), (2, 'new'), (3, 'older')], stored(n)
    checks.append('delayed samples keep newer node metadata')

    n, s = fresh()  # Equal timestamps: the first sample in batch order defines metadata.
    assert ingest(sample(n, s, 1, 3, 'first'), sample(n, s, 2, 3, 'second')) == 2
    assert node(n) == ('first', NOW + timedelta(seconds=3)), node(n)
    assert ingest(sample(n, s, 3, 3, 'third')) == 1
    assert node(n)[0] == 'first', node(n)
    checks.append('equal timestamps keep the first observation')

    n, s = fresh()  # Out-of-order samples inside one batch.
    assert ingest(sample(n, s, 1, 7, 'later'), sample(n, s, 2, 2, 'earlier')) == 2
    assert node(n) == ('later', NOW + timedelta(seconds=7)), node(n)
    checks.append('out-of-order batch keeps latest metadata')

    (a, s), b = fresh(), fresh()[0]  # One batch spanning several nodes.
    assert ingest(sample(a, s, 1, 1, 'a1'), sample(b, s, 1, 2, 'b1'), sample(a, s, 2, 3, 'a2'),
                  sample(b, s, 2, 0, 'b0')) == 4
    assert node(a) == ('a2', NOW + timedelta(seconds=3)), node(a)
    assert node(b) == ('b1', NOW + timedelta(seconds=2)), node(b)
    checks.append('multi-node batches update each node independently')

    n, s = fresh()  # Mixed duplicate/new batch reports only new rows; duplicates do not move metadata.
    assert ingest(sample(n, s, 1, 1, 'v1')) == 1
    assert ingest(sample(n, s, 1, 9, 'dup-later'), sample(n, s, 2, 2, 'v2')) == 1
    assert node(n) == ('v2', NOW + timedelta(seconds=2)), node(n)
    checks.append('duplicates never update node metadata')

    for check in checks:
        print('PASS:', check)


if __name__ == '__main__':
    main()
