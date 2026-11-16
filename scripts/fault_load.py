"""Incident detection delay and agent delivery reliability under ingestion load.

For each background load level, synthetic senders saturate ingestion while the
real node-1 agent keeps collecting. The script then triggers each incident
type in turn, timing detection and recovery by polling the API:

  slow release (v2-slow)   -> slow_responses
  error release (v2-errors) -> elevated_errors
  application stopped      -> application_unavailable
  agent stopped            -> agent_unreachable

Delays are measured from the moment the fault is in place (deployed container
healthy, or service stopped) until the API first shows the incident, and from
the repair until it shows resolution. For release faults it also reports the
database-clock delay from the first faulty sample's collection to opened_at.

Agent reliability is measured two independent ways: warning lines in the agent
log (retries, queue overflow drops, rejected batches, heartbeat failures) and
gaps in node-1's per-session sample sequence numbers in PostgreSQL, alongside
collection-to-storage delivery delay. Incidents opened outside the scenario
that should cause them are reported as unexpected.

Only for the local Compose demo: it deploys faulty releases and stops services,
and always restores the default healthy stack on exit.
"""
import argparse
import datetime as dt
import json
import os
import statistics
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

import load

ROOT = Path(__file__).resolve().parents[1]
API = 'http://127.0.0.1:8080'
APP = 'http://127.0.0.1:8081'
POLL = 0.25
LOG_EVENTS = {
    'retries': 'telemetry delivery failed; retry scheduled',
    'queue_drops': 'agent queue full; oldest queued sample dropped',
    'rejected_batches': 'backend rejected telemetry batch',
    'invalid_samples': 'invalid sample dropped',
    'heartbeat_failures': 'heartbeat failed',
    'collection_failures': 'sample collection failed',
}


def now():
    return dt.datetime.now(dt.timezone.utc)


def env(**overrides):
    values = dict(os.environ)
    values.pop('PULSE_BACKEND_IMAGE', None)
    values.pop('APP_VERSION', None)
    values.update(overrides)
    return values


def compose(*args, **overrides):
    subprocess.run(['docker', 'compose', *args], cwd=ROOT, env=env(**overrides), check=True, timeout=180,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def incidents():
    with urllib.request.urlopen(API + '/api/v1/nodes/node-1/incidents', timeout=5) as response:
        return json.load(response)


def parse_time(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def wait_for(condition, timeout):
    """Poll until condition returns a value; returns (value, seconds) or (None, None)."""
    started = time.perf_counter()
    while time.perf_counter() - started < timeout:
        try:
            value = condition()
            if value:
                return value, time.perf_counter() - started
        except (OSError, ValueError):
            pass
        time.sleep(POLL)
    return None, None


def psql_json(query):
    raw = load.command(load.PSQL + ['-c', query], timeout=30)
    return json.loads(raw) if raw else None


class BackgroundLoad:
    """Closed-loop synthetic senders that run until stopped."""

    def __init__(self, concurrency, batch_size):
        self.concurrency, self.batch_size = concurrency, batch_size
        self.stop = threading.Event()
        self.session = uuid.uuid4().hex
        self.results = []
        self.lock = threading.Lock()
        self.threads = [threading.Thread(target=self.worker, args=(i,), daemon=True) for i in range(concurrency)]

    def worker(self, worker_id):
        sequence = acknowledged = 0
        latencies, failures = [], 0
        while not self.stop.is_set():
            request = load.batch_request(f'faultload-{self.session}-{worker_id}', self.session, sequence, self.batch_size)
            sequence += self.batch_size
            started = time.perf_counter()
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    acknowledged += json.load(response)['inserted']
            except (OSError, ValueError):
                failures += 1
            latencies.append((time.perf_counter() - started) * 1000)
        with self.lock:
            self.results.append((acknowledged, latencies, failures))

    def __enter__(self):
        self.started = time.perf_counter()
        for thread in self.threads:
            thread.start()
        return self

    def __exit__(self, *exc):
        self.stop.set()
        for thread in self.threads:
            thread.join(timeout=30)
        elapsed = time.perf_counter() - self.started
        latencies = sorted(v for r in self.results for v in r[1])
        self.summary = dict(concurrency=self.concurrency, seconds=elapsed,
                            samples_per_second=sum(r[0] for r in self.results) / elapsed if self.concurrency else 0,
                            requests=len(latencies), failed_requests=sum(r[2] for r in self.results),
                            p95_ms=load.percentile(latencies, .95), p99_ms=load.percentile(latencies, .99))
        return False


class Workload(threading.Thread):
    """Steady requests to the sample application so release regressions are observable."""

    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()

    def run(self):
        while not self.stop.is_set():
            try:
                with urllib.request.urlopen(APP + '/work', timeout=3) as response:
                    response.read()
            except urllib.error.HTTPError as error:
                error.close()
            except OSError:
                time.sleep(0.2)
            time.sleep(0.03)


def opened(kind, known, version=None):
    def check():
        return next((i for i in incidents() if i['kind'] == kind and i['id'] not in known and i['resolved_at'] is None
                     and (version is None or i['version'] == version)), None)
    return check


def resolved(incident_id):
    def check():
        return next((i for i in incidents() if i['id'] == incident_id and i['resolved_at'] is not None), None)
    return check


def scenario(name, kind, inject, repair, timeout, version=None):
    known = {i['id'] for i in incidents()}
    fault_start = now()
    inject()
    in_place = now()
    incident, detect = wait_for(opened(kind, known, version), timeout)
    result = dict(scenario=name, kind=kind, fault_in_place_at=in_place.isoformat(),
                  detected=incident is not None, detection_seconds=detect, incident=incident)
    if version and incident:
        first = psql_json(f"SELECT json_build_object('first', min(collected_at)) FROM samples WHERE node_id='node-1' "
                          f"AND version='{version}' AND collected_at >= '{fault_start.isoformat()}'")
        if first and first['first']:
            result['first_faulty_sample_at'] = first['first']
            result['first_sample_to_opened_seconds'] = (parse_time(incident['opened_at']) - parse_time(first['first'])).total_seconds()
    repair()
    if incident:
        done, recover = wait_for(resolved(incident['id']), timeout)
        result.update(recovered=done is not None, recovery_seconds=recover)
    return result, (incident or {}).get('id')


def agent_reliability(start, end):
    logs = subprocess.run(['docker', 'compose', 'logs', 'agent', '--no-log-prefix', '--no-color',
                           '--since', start.isoformat(), '--until', end.isoformat()],
                          cwd=ROOT, capture_output=True, text=True, timeout=60, encoding='utf-8', errors='replace').stdout
    counts = {key: logs.count(message) for key, message in LOG_EVENTS.items()}
    shutdown_totals = []
    for line in logs.splitlines():
        if 'agent delivery totals' in line:
            try:
                record = json.loads(line)
                shutdown_totals.append(dict(retries=record.get('retries'), dropped_samples=record.get('dropped_samples')))
            except ValueError:
                pass
    sessions = psql_json(
        "SELECT coalesce(json_agg(t), '[]') FROM (SELECT session_id, count(*) AS stored, min(sequence) AS first, "
        "max(sequence) AS last, percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM received_at - collected_at)) AS delay_p50, "
        "percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM received_at - collected_at)) AS delay_p95, "
        "max(extract(epoch FROM received_at - collected_at)) AS delay_max FROM samples WHERE node_id = 'node-1' "
        f"AND collected_at >= '{start.isoformat()}' AND collected_at < '{end.isoformat()}' GROUP BY session_id) t") or []
    expected = sum(s['last'] - s['first'] + 1 for s in sessions)
    stored = sum(s['stored'] for s in sessions)
    return dict(log_events=counts, shutdown_totals=shutdown_totals, sessions=sessions,
                expected_samples=expected, stored_samples=stored, sequence_gaps=expected - stored,
                delivery_delay_seconds=dict(p50_max_over_sessions=max((s['delay_p50'] for s in sessions), default=None),
                                            p95_max_over_sessions=max((s['delay_p95'] for s in sessions), default=None),
                                            max=max((s['delay_max'] for s in sessions), default=None)))


def run_level(concurrency, batch_size, warmup, steady, timeout):
    compose('up', '-d', '--wait')
    before_vars = load.debug_vars()
    workload = Workload()
    workload.start()
    expected_ids = set()
    try:
        with BackgroundLoad(concurrency, batch_size) as background:
            time.sleep(warmup)
            window_start = now()
            known_before = {i['id'] for i in incidents()}
            time.sleep(steady)
            results = []
            for name, kind, inject, repair, version in [
                ('slow release', 'slow_responses', lambda: compose('up', '-d', '--wait', '--no-deps', 'sampleapp', APP_VERSION='v2-slow'),
                 lambda: compose('up', '-d', '--wait', '--no-deps', 'sampleapp'), 'v2-slow'),
                ('error release', 'elevated_errors', lambda: compose('up', '-d', '--wait', '--no-deps', 'sampleapp', APP_VERSION='v2-errors'),
                 lambda: compose('up', '-d', '--wait', '--no-deps', 'sampleapp'), 'v2-errors'),
                ('application stopped', 'application_unavailable', lambda: compose('stop', 'sampleapp'),
                 lambda: compose('start', 'sampleapp'), None),
                ('agent stopped', 'agent_unreachable', lambda: compose('stop', 'agent'),
                 lambda: compose('start', 'agent'), None),
            ]:
                result, incident_id = scenario(name, kind, inject, repair, timeout, version)
                expected_ids.add(incident_id)
                results.append(result)
                print(json.dumps(dict(concurrency=concurrency, scenario=name, detected=result['detected'],
                                      detection_seconds=result['detection_seconds'],
                                      recovery_seconds=result.get('recovery_seconds'),
                                      first_sample_to_opened_seconds=result.get('first_sample_to_opened_seconds'))), flush=True)
            time.sleep(5)  # Let the restarted agent deliver before the window closes.
            window_end = now()
    finally:
        workload.stop.set()
        compose('up', '-d', '--wait')
    after_vars = load.debug_vars()
    unexpected = [i for i in incidents() if i['id'] not in known_before and i['id'] not in expected_ids]
    pool = None
    if before_vars and after_vars:
        pool = dict(waits=after_vars['db']['WaitCount'] - before_vars['db']['WaitCount'],
                    wait_seconds=(after_vars['db']['WaitDuration'] - before_vars['db']['WaitDuration']) / 1e9)
    return dict(concurrency=concurrency, background=background.summary, scenarios=results,
                unexpected_incidents=unexpected, agent=agent_reliability(window_start, window_end),
                backend_pool=pool, window=[window_start.isoformat(), window_end.isoformat()])


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--concurrency', type=int, nargs='+', default=[0, 16, 64, 128])
    parser.add_argument('--repeats', type=int, default=2, help='later repeats reverse the level order')
    parser.add_argument('--batch-size', type=int, default=32)
    parser.add_argument('--warmup', type=float, default=10)
    parser.add_argument('--steady', type=float, default=15, help='seconds of load before the first fault')
    parser.add_argument('--timeout', type=float, default=60, help='maximum wait for each detection or recovery')
    parser.add_argument('--output', required=True, help='JSON report path (overwritten)')
    args = parser.parse_args()
    report = dict(load.environment(), configuration=vars(args), backend_image=load.backend_image(), runs=[])
    output = Path(args.output)
    try:
        for repeat in range(1, args.repeats + 1):
            levels = args.concurrency if repeat % 2 else list(reversed(args.concurrency))
            for level in levels:
                result = run_level(level, args.batch_size, args.warmup, args.steady, args.timeout)
                result['repeat'] = repeat
                report['runs'].append(result)
                output.write_text(json.dumps(report, indent=2), encoding='utf-8')
                agent = result['agent']
                print(json.dumps(dict(repeat=repeat, concurrency=level,
                                      background_samples_per_second=round(result['background']['samples_per_second']),
                                      background_failed=result['background']['failed_requests'],
                                      agent_log_events=agent['log_events'], sequence_gaps=agent['sequence_gaps'],
                                      delivery_delay=agent['delivery_delay_seconds'],
                                      unexpected_incidents=len(result['unexpected_incidents']))), flush=True)
    finally:
        compose('up', '-d', '--wait')


if __name__ == '__main__':
    main()
