"""One-command Pulse demo: a faulty release, its detection, and recovery.

  python scripts/demo.py                 # slow release (v2-slow)
  python scripts/demo.py --fault errors  # error release (v2-errors)
  python scripts/demo.py --pause         # wait for Enter between steps (for recording)

Starts the Compose stack, drives traffic to the sample application, then:
  1. shows the healthy baseline,
  2. deploys the faulty version to node-1,
  3. waits for Pulse to open an incident and prints its evidence,
  4. compares the healthy and faulty versions,
  5. rolls back to v1 and waits for the incident to resolve.
The sample application is always restored to v1 on exit. Uses only the
Python standard library; requires Docker with Compose.
"""
import argparse
import datetime as dt
import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
API = 'http://127.0.0.1:8080/api/v1'
APP = 'http://127.0.0.1:8081'
DASHBOARD = 'http://localhost:3000'
FAULTS = {'slow': ('v2-slow', 'slow_responses'), 'errors': ('v2-errors', 'elevated_errors')}


def say(text=''):
    print(text, flush=True)


def step(number, title, pause):
    say()
    say(f'── Step {number}: {title} ' + '─' * max(0, 60 - len(title)))
    if pause:
        input('   Press Enter to continue… ')


def compose(*args, version=None):
    env = dict(os.environ)
    env.pop('PULSE_BACKEND_IMAGE', None)
    env.pop('APP_VERSION', None)
    if version:
        env['APP_VERSION'] = version
    subprocess.run(['docker', 'compose', *args], cwd=ROOT, env=env, check=True, timeout=600)


def get(path):
    with urllib.request.urlopen(API + path, timeout=5) as response:
        return json.load(response)


def wait_for(label, condition, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = condition()
            if result:
                return result
        except (OSError, ValueError):
            pass
        time.sleep(0.5)
    raise SystemExit(f'Timed out after {timeout:.0f}s waiting for {label}.')


class Traffic(threading.Thread):
    """About 30 requests per second to the sample application's /work endpoint."""

    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()
        self.requests = self.errors = 0

    def run(self):
        while not self.stop.is_set():
            try:
                with urllib.request.urlopen(APP + '/work', timeout=3) as response:
                    response.read()
            except urllib.error.HTTPError as error:
                error.close()
                self.errors += 1
            except OSError:
                time.sleep(0.2)
                continue
            self.requests += 1
            time.sleep(0.03)


def incident_line(incident):
    evidence = incident.get('evidence') or {}
    line = f"{incident['kind'].replace('_', ' ')} on node-1, version {incident['version'] or 'unknown'}"
    intervals = evidence.get('intervals') or []
    if intervals:
        values = [f"{v['mean_latency_ms']:.0f} ms" if incident['kind'] == 'slow_responses'
                  else f"{v['error_rate'] * 100:.1f}% errors" for v in intervals]
        line += f"\n   Evidence: {evidence.get('rule')}\n   Measured intervals: {', '.join(values)}"
    return line


def summary(side):
    if side['requests'] == 0 or side['mean_latency_ms'] is None:
        return f"{side['version']:>10}: no measured traffic"
    return (f"{side['version']:>10}: {side['requests']:>5} requests, {side['error_rate'] * 100:5.1f}% errors, "
            f"{side['mean_latency_ms']:7.1f} ms mean latency")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--fault', choices=sorted(FAULTS), default='slow')
    parser.add_argument('--pause', action='store_true', help='wait for Enter before each step')
    parser.add_argument('--no-start', action='store_true', help='assume the stack is already running')
    args = parser.parse_args()
    version, kind = FAULTS[args.fault]

    if not args.no_start:
        say('Starting Pulse (first build can take a few minutes)…')
        compose('up', '--build', '-d', '--wait')
    traffic = Traffic()
    traffic.start()
    try:
        step(1, 'healthy baseline (v1)', args.pause)
        compose('up', '-d', '--wait', '--no-deps', 'sampleapp')
        baseline_start = dt.datetime.now(dt.timezone.utc)
        say('   Sending traffic to node-1 running v1 for 20 seconds…')
        time.sleep(20)
        node = wait_for('node-1 heartbeat', lambda: next((n for n in get('/fleet?after=node-0')
                                                          if n['id'] == 'node-1' and n['last_seen']), None), 30)
        say(f"   node-1 status: {node['status']}, version {node['version']}")
        say(f'   Dashboard: {DASHBOARD}/#/node/node-1')

        step(2, f'deploy faulty release {version} to node-1', args.pause)
        known = {i['id'] for i in get('/nodes/node-1/incidents')}
        compose('up', '-d', '--wait', '--no-deps', 'sampleapp', version=version)
        deployed = time.monotonic()
        say(f'   {version} is running. Pulse needs three agreeing 3-second intervals to open an incident.')

        step(3, 'Pulse detects the regression', args.pause)
        incident = wait_for(f'{kind} incident', lambda: next(
            (i for i in get('/nodes/node-1/incidents') if i['kind'] == kind and i['id'] not in known
             and i['resolved_at'] is None), None), 90)
        say(f'   Incident opened {time.monotonic() - deployed:.0f} s after deployment: {incident_line(incident)}')
        say(f'   Dashboard: {DASHBOARD}/#/node/node-1 (incident timeline)')

        step(4, f'compare v1 with {version}', args.pause)
        time.sleep(6)  # A few more faulty intervals for the comparison.
        now = dt.datetime.now(dt.timezone.utc)
        query = urllib.parse.urlencode(dict(baseline_version='v1', baseline_from=baseline_start.isoformat(),
                                            baseline_to=now.isoformat(), candidate_version=version,
                                            candidate_from=baseline_start.isoformat(), candidate_to=now.isoformat()))
        comparison = get('/nodes/node-1/compare?' + query)
        say('   ' + summary(comparison['baseline']))
        say('   ' + summary(comparison['candidate']))
        say(f'   Dashboard: {DASHBOARD}/#/compare/node-1')

        step(5, 'roll back to v1 and wait for recovery', args.pause)
        compose('up', '-d', '--wait', '--no-deps', 'sampleapp')
        rolled_back = time.monotonic()
        wait_for('incident resolution', lambda: next(
            (i for i in get('/nodes/node-1/incidents') if i['id'] == incident['id'] and i['resolved_at']), None), 90)
        say(f'   Incident resolved {time.monotonic() - rolled_back:.0f} s after rollback (three healthy intervals).')
        say()
        say(f'Demo complete. Traffic sent: {traffic.requests} requests, {traffic.errors} HTTP errors.')
        say(f'Explore: {DASHBOARD}  ·  stop with: docker compose down  (data persists in a volume)')
    finally:
        traffic.stop.set()
        compose('up', '-d', '--wait', '--no-deps', 'sampleapp')


if __name__ == '__main__':
    main()
