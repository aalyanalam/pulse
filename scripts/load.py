"""Repeatable, duration-based load test for the local Compose demo.

Each trial has an unrecorded warm-up phase followed by a fixed measurement
window. Throughput and latency use only requests completed inside the window;
every acknowledged sample, including warm-up traffic, is verified in PostgreSQL.
When the backend debug listener (127.0.0.1:6060) is available the trial also
records connection-pool waits and per-phase ingestion timings. PostgreSQL
session states and wait events are sampled, and commit/WAL counters are
diffed, so time can be attributed to the database, the backend or the client.
"""
import argparse
import concurrent.futures
import datetime as dt
import json
import math
import os
import platform
import statistics
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
API = 'http://127.0.0.1:8080'
DEBUG = 'http://127.0.0.1:6060/debug/vars'
PSQL = ['docker', 'compose', 'exec', '-T', 'postgres', 'psql', '-U', 'pulse', '-d', 'pulse', '-At']
WAIT_INTERVAL = 0.2
# Non-idle client sessions grouped by state and wait event. "active" without a
# wait event is executing (CPU); "idle in transaction" waits for the backend's
# next statement, i.e. a network round trip or Go-side work.
WAIT_QUERY = ("SELECT coalesce(json_object_agg(k, n), '{}') FROM (SELECT state || '/' || "
              "coalesce(wait_event_type, 'CPU') || '/' || coalesce(wait_event, '-') AS k, count(*) AS n "
              "FROM pg_stat_activity WHERE datname = 'pulse' AND backend_type = 'client backend' "
              "AND pid <> pg_backend_pid() AND state <> 'idle' GROUP BY 1) t")
COUNTER_QUERY = ("SELECT json_build_object('commits', d.xact_commit, 'rollbacks', d.xact_rollback, "
                 "'blks_read', d.blks_read, 'blks_hit', d.blks_hit, 'wal_records', w.wal_records, "
                 "'wal_bytes', w.wal_bytes, 'wal_writes', w.wal_write, 'wal_syncs', w.wal_sync, "
                 "'at', extract(epoch FROM clock_timestamp())) "
                 "FROM pg_stat_database d, pg_stat_wal w WHERE d.datname = 'pulse'")


def command(args, timeout=20):
    return subprocess.check_output(args, cwd=ROOT, text=True, timeout=timeout).strip()


def debug_vars():
    try:
        with urllib.request.urlopen(DEBUG, timeout=2) as response:
            data = json.load(response)
        return dict(db=data['db'], ingest=data['ingest'], memstats={k: data['memstats'][k] for k in (
            'TotalAlloc', 'Mallocs', 'NumGC', 'PauseTotalNs')}, at=time.perf_counter())
    except (OSError, ValueError, KeyError):
        return None


def pg_counters():
    try:
        return json.loads(command(PSQL + ['-c', COUNTER_QUERY]))
    except (subprocess.SubprocessError, ValueError):
        return None


def resources(stop, observations):
    while not stop.is_set():
        try:
            raw = command(['docker', 'stats', '--no-stream', '--format', '{{json .}}',
                           'pulse-backend-1', 'pulse-postgres-1'])
            observations.append({'at': dt.datetime.now(dt.timezone.utc).isoformat(),
                                 'containers': [json.loads(line) for line in raw.splitlines()]})
        except (subprocess.SubprocessError, ValueError) as error:
            observations.append({'error': str(error)})
        stop.wait(2)


def batch_request(node, session, last_sequence, size):
    """One synthetic batch continuing a node's cumulative counters after last_sequence."""
    samples = []
    for sequence in range(last_sequence + 1, last_sequence + size + 1):
        samples.append(dict(node_id=node, session_id=session, sequence=sequence,
                            collected_at=dt.datetime.now(dt.timezone.utc).isoformat(), version='v1',
                            app_process_id=session, requests=sequence*10, errors=sequence//10,
                            total_latency_ms=sequence*20, memory_bytes=1048576))
    return urllib.request.Request(API + '/api/v1/telemetry/batches', data=json.dumps({'samples': samples}).encode(),
                                  headers={'Content-Type': 'application/json'})


def percentile(values, p):
    return values[math.ceil(len(values) * p) - 1] if values else None


def server_summary(before, after, requests):
    if not before or not after:
        return None
    i0, i1, d0, d1 = before['ingest'], after['ingest'], before['db'], after['db']
    n = i1['requests'] - i0['requests']
    if n <= 0:
        return None
    per = lambda key: (i1[key] - i0[key]) * 1000 / n
    phases = {key: per(key + '_seconds') for key in ('begin', 'statement', 'commit', 'handler')}
    return dict(
        ingest_requests=n, client_requests=requests,
        statements_per_request=(i1['statements'] - i0['statements']) / n,
        mean_ms={**phases, 'outside_transaction': phases['handler'] - phases['begin'] - phases['statement'] - phases['commit']},
        mean_statement_ms=(i1['statement_seconds'] - i0['statement_seconds']) * 1000 / max(1, i1['statements'] - i0['statements']),
        pool_waits=d1['WaitCount'] - d0['WaitCount'],
        pool_wait_ms_per_request=(d1['WaitDuration'] - d0['WaitDuration']) / 1e6 / n,
        pool_max_open=d1['MaxOpenConnections'],
        allocated_kib_per_request=(after['memstats']['TotalAlloc'] - before['memstats']['TotalAlloc']) / 1024 / n,
        mallocs_per_request=(after['memstats']['Mallocs'] - before['memstats']['Mallocs']) / n,
        gc_cycles=after['memstats']['NumGC'] - before['memstats']['NumGC'])


def database_summary(before, after, wait_lines):
    snapshots = []
    for line in wait_lines:
        line = line.strip()
        if line.startswith('{'):
            try:
                snapshots.append(json.loads(line))
            except ValueError:
                pass
    keys = sorted({key for snapshot in snapshots for key in snapshot})
    result = dict(wait_samples=len(snapshots),
                  mean_sessions={key: sum(s.get(key, 0) for s in snapshots) / len(snapshots) for key in keys} if snapshots else {})
    if before and after:
        delta = {key: float(after[key]) - float(before[key]) for key in before}
        seconds = delta.pop('at')
        commits = max(1, delta['commits'])
        result.update(counter_seconds=seconds, commits_per_second=delta['commits'] / seconds,
                      wal_syncs_per_commit=delta['wal_syncs'] / commits,
                      wal_bytes_per_commit=delta['wal_bytes'] / commits,
                      wal_records_per_commit=delta['wal_records'] / commits,
                      blocks_read=delta['blks_read'])
    return result


def experiment(concurrency, duration, batch_size, warmup=5.0, diagnostics=True):
    session = uuid.uuid4().hex
    barrier = threading.Barrier(concurrency + 1)
    stop = threading.Event()
    window = {}
    observations = []
    monitor = threading.Thread(target=resources, args=(stop, observations))
    monitor.start()

    def worker(worker_id):
        latencies, failures = [], []
        acknowledged = measured = submitted = sequence = 0
        build_seconds = 0.0
        barrier.wait()
        start, end = window['start'], window['end']
        while time.perf_counter() < end:
            built = time.perf_counter()
            request = batch_request(f'load-{session}-{worker_id}', session, sequence, batch_size)
            sequence += batch_size
            submitted += batch_size
            started = time.perf_counter()
            if started >= start:
                build_seconds += started - built
            count, error = 0, None
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    count = json.load(response)['inserted']
            except (OSError, ValueError) as caught:
                error = str(caught)
            finished = time.perf_counter()
            acknowledged += count
            if start <= finished <= end:
                latencies.append((finished - started) * 1000)
                measured += count
                if error:
                    failures.append(error)
            elif error:
                failures.append('outside window: ' + error)
        return dict(submitted=submitted, acknowledged=acknowledged, measured=measured,
                    latencies=latencies, failures=failures, build_seconds=build_seconds)

    wait_lines, pg_before, pg_after, watcher = [], None, None, None
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = [pool.submit(worker, i) for i in range(concurrency)]
            now = time.perf_counter()
            window.update(start=now + warmup, end=now + warmup + duration)
            barrier.wait()
            if diagnostics:
                pg_before = pg_counters()
                count = max(1, int(duration / WAIT_INTERVAL))
                time.sleep(max(0.0, window['start'] - time.perf_counter()))
                # \watch needs the query in the same input buffer, so feed both on stdin.
                watcher = subprocess.Popen(PSQL, cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                           stderr=subprocess.DEVNULL, text=True)
                watcher.stdin.write(f'{WAIT_QUERY}\n\\watch i={WAIT_INTERVAL} c={count}\n')
                watcher.stdin.close()
            time.sleep(max(0.0, window['start'] - time.perf_counter()))
            debug_before, cpu_before = debug_vars() if diagnostics else None, time.process_time()
            time.sleep(max(0.0, window['end'] - time.perf_counter()))
            debug_after, cpu_after = debug_vars() if diagnostics else None, time.process_time()
            results = [future.result() for future in futures]
        if watcher:
            # The watch ends by itself after `count` samples; stdin is already closed.
            wait_lines = watcher.stdout.read().splitlines()
            watcher.wait(timeout=30)
            pg_after = pg_counters()
    finally:
        stop.set()
        monitor.join()
    latencies = sorted(value for result in results for value in result['latencies'])
    acknowledged = sum(result['acknowledged'] for result in results)
    measured = sum(result['measured'] for result in results)
    # The session is a generated hexadecimal UUID, safe to interpolate.
    stored = int(command(PSQL + ['-c', f"SELECT count(*) FROM samples WHERE session_id='{session}'"]))
    failures = [message for result in results for message in result['failures']]
    mean_latency = statistics.fmean(latencies) if latencies else None
    server = server_summary(debug_before, debug_after, len(latencies))
    client = dict(cpu_cores=(cpu_after - cpu_before) / duration,
                  build_ms_per_request=sum(r['build_seconds'] for r in results) * 1000 / max(1, len(latencies)))
    if server and mean_latency is not None:
        client['latency_minus_server_handler_ms'] = mean_latency - server['mean_ms']['handler']
    return dict(session=session, concurrency=concurrency, warmup_seconds=warmup, duration_seconds=duration,
                batch_size=batch_size, submitted=sum(result['submitted'] for result in results),
                acknowledged=acknowledged, stored=stored, measured_acknowledged=measured,
                samples_per_second=measured / duration, requests=len(latencies),
                failed_requests=len(failures), failure_examples=failures[:5],
                latency_ms={'mean': mean_latency, **{str(p): percentile(latencies, p) for p in (.5, .95, .99)}},
                server=server, client=client,
                database=database_summary(pg_before, pg_after, wait_lines) if diagnostics else None,
                resources=observations)


def environment():
    info = json.loads(command(['docker', 'info', '--format', '{{json .}}']))
    return dict(created_at=dt.datetime.now(dt.timezone.utc).isoformat(), platform=platform.platform(),
                logical_cpus=os.cpu_count(), python=platform.python_version(),
                docker={key: info.get(key) for key in ('NCPU', 'MemTotal', 'ServerVersion', 'OperatingSystem')})


def backend_image():
    try:
        return command(['docker', 'inspect', '--format', '{{.Config.Image}} {{.Image}}', 'pulse-backend-1'])
    except subprocess.SubprocessError:
        return None


def brief(result):
    server, database = result['server'] or {}, result['database'] or {}
    return dict(concurrency=result['concurrency'], samples_per_second=round(result['samples_per_second']),
                p95_ms=round(result['latency_ms']['0.95'] or 0, 1), failed=result['failed_requests'],
                stored_matches=result['stored'] == result['acknowledged'],
                pool_wait_ms=round(server.get('pool_wait_ms_per_request', 0), 1),
                phases_ms={k: round(v, 1) for k, v in server.get('mean_ms', {}).items()},
                pg_sessions={k: round(v, 2) for k, v in database.get('mean_sessions', {}).items()},
                client_cpu=round(result['client']['cpu_cores'], 2))


def problems(runs):
    return [run for run in runs if run['failed_requests'] or run['stored'] != run['acknowledged']]


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--duration', type=float, default=20, help='measured seconds per trial')
    parser.add_argument('--warmup', type=float, default=5, help='unrecorded seconds before each measurement')
    parser.add_argument('--repeats', type=int, default=3)
    parser.add_argument('--concurrency', type=int, nargs='+', default=[1, 4, 8, 16, 32])
    parser.add_argument('--batch-size', type=int, default=32)
    parser.add_argument('--no-diagnostics', action='store_true', help='skip debug-listener and PostgreSQL sampling')
    parser.add_argument('--output', required=True, help='JSON report path (overwritten)')
    args = parser.parse_args()
    if args.duration <= 0 or args.warmup < 0 or args.repeats < 1 or min(args.concurrency) < 1 or not 1 <= args.batch_size <= 100:
        parser.error('duration, repeats and concurrency must be positive; batch size must be 1–100')
    report = dict(environment(), configuration=vars(args), backend_image=backend_image(), runs=[])
    for repeat in range(args.repeats):
        for concurrency in args.concurrency:
            result = experiment(concurrency, args.duration, args.batch_size, args.warmup, not args.no_diagnostics)
            result['repeat'] = repeat+1
            report['runs'].append(result)
            Path(args.output).write_text(json.dumps(report, indent=2), encoding='utf-8')
            print(json.dumps(brief(result)), flush=True)
    print('Median samples/sec by concurrency:', flush=True)
    for concurrency in args.concurrency:
        print(concurrency, round(statistics.median(run['samples_per_second'] for run in report['runs'] if run['concurrency'] == concurrency)))
    if problems(report['runs']):
        raise SystemExit('Load failures or acknowledgement mismatch recorded; inspect the report.')


if __name__ == '__main__':
    main()
