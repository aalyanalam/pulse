"""Run controlled healthy, faulty-release, and rollback checks against Compose."""
import datetime as dt
import urllib.parse
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

def incidents():
    with urllib.request.urlopen("http://127.0.0.1:8080/api/v1/nodes/node-1/incidents", timeout=3) as response:
        return json.load(response)

def deploy(version):
    env = dict(os.environ, APP_VERSION=version)
    subprocess.run(["docker", "compose", "up", "-d", "--wait", "--no-deps", "sampleapp"], env=env, check=True)

def workload(seconds, expected_kind=None, baseline=None, recovery_id=None):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen("http://127.0.0.1:8081/work", timeout=3) as response:
                response.read()
        except urllib.error.HTTPError as error:
            if error.code != 500:
                raise
            error.close()
        time.sleep(0.03)
        rows = incidents()
        if expected_kind:
            found = next((r for r in rows if r["kind"] == expected_kind and r["id"] not in baseline and r["resolved_at"] is None), None)
            if found:
                return found
        if recovery_id:
            found = next((r for r in rows if r["id"] == recovery_id and r["resolved_at"] is not None), None)
            if found:
                return found
    if expected_kind or recovery_id:
        raise AssertionError(f"Expected transition missing: {incidents()}")

try:
    comparison_start = dt.datetime.now(dt.timezone.utc).isoformat()
    deploy("v1")
    baseline = {r["id"] for r in incidents()}
    workload(15)
    assert not any(r["id"] not in baseline and r["kind"] in ("slow_responses", "elevated_errors") for r in incidents())
    for version, kind in [("v2-slow", "slow_responses"), ("v2-errors", "elevated_errors")]:
        baseline = {r["id"] for r in incidents()}
        deploy(version)
        opened = workload(45, expected_kind=kind, baseline=baseline)
        assert opened["version"] == version, opened
        # The incident records the three intervals that opened it, each above the threshold.
        intervals = opened["evidence"]["intervals"]
        assert opened["evidence"]["rule"] and len(intervals) == 3, opened
        assert all(i["requests"] >= 5 for i in intervals), opened
        if version == "v2-slow":
            assert all(i["mean_latency_ms"] > 100 for i in intervals), opened
        else:
            assert all(i["error_rate"] > 0.1 for i in intervals), opened
        print(f"PASS {version} opened {opened}", flush=True)
        deploy("v1")
        resolved = workload(45, recovery_id=opened["id"])
        print(f"PASS rollback resolved {resolved}", flush=True)
        comparison_end = dt.datetime.now(dt.timezone.utc).isoformat()
        query = urllib.parse.urlencode(dict(baseline_version="v1", baseline_from=comparison_start, baseline_to=comparison_end, candidate_version=version, candidate_from=comparison_start, candidate_to=comparison_end))
        with urllib.request.urlopen("http://127.0.0.1:8080/api/v1/nodes/node-1/compare?"+query, timeout=10) as response:
            comparison = json.load(response)
        healthy, faulty = comparison["baseline"], comparison["candidate"]
        assert healthy["requests"] > 0 and faulty["requests"] > 0, comparison
        if version == "v2-slow":
            assert faulty["mean_latency_ms"] > 100 and faulty["mean_latency_ms"] > healthy["mean_latency_ms"], comparison
        else:
            assert faulty["error_rate"] > 0.1 and faulty["error_rate"] > healthy["error_rate"], comparison
        print(f"PASS release comparison: {comparison}", flush=True)
finally:
    deploy("v1")



