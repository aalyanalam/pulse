"""Shared fixtures: stack restoration and failure diagnostics for fault scenarios."""
import json
import os
import re
import subprocess
import urllib.request
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]


def compose_env():
    # Scenarios always target the default build: no benchmark variant, healthy v1 release.
    env = dict(os.environ)
    env.pop('PULSE_BACKEND_IMAGE', None)
    env.pop('APP_VERSION', None)
    return env


def restore():
    """Start every service with the default configuration (undoes stops and faulty releases)."""
    subprocess.run(['docker', 'compose', 'up', '-d', '--wait'], cwd=ROOT, env=compose_env(), check=True, timeout=180)


def capture(args, timeout=30):
    try:
        result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout, encoding='utf-8', errors='replace')
        return (result.stdout + result.stderr).strip()
    except (OSError, subprocess.SubprocessError) as error:
        return f'unavailable: {error}'


def fetch(url):
    try:
        with urllib.request.urlopen(url, timeout=3) as response:
            return json.dumps(json.load(response), indent=1)
    except (OSError, ValueError) as error:
        return f'unavailable: {error}'


def diagnostics():
    pool = fetch('http://127.0.0.1:6060/debug/vars')
    try:
        pool = json.dumps({key: json.loads(pool)[key] for key in ('db', 'ingest')}, indent=1)
    except (ValueError, KeyError):
        pass
    return {
        'compose ps': capture(['docker', 'compose', 'ps', '-a']),
        'node-1 incidents': fetch('http://127.0.0.1:8080/api/v1/nodes/node-1/incidents'),
        'backend pool and ingestion counters': pool,
        'service logs (last 80 lines each)': capture(['docker', 'compose', 'logs', '--no-color', '--tail', '80',
                                                      'backend', 'agent', 'sampleapp']),
    }


@pytest.hookimpl(wrapper=True)
def pytest_runtest_makereport(item, call):
    report = yield
    if report.failed:
        details = diagnostics()
        for title, text in details.items():
            report.sections.append((f'Pulse diagnostics: {title}', text))
        directory = os.environ.get('PULSE_DIAGNOSTICS_DIR')
        if directory:
            path = Path(directory)
            path.mkdir(parents=True, exist_ok=True)
            name = re.sub(r'[^A-Za-z0-9_.-]+', '_', f'{item.nodeid}-{report.when}')
            (path / f'{name}.txt').write_text('\n\n'.join(f'== {k} ==\n{v}' for k, v in details.items()), encoding='utf-8')
    return report


@pytest.fixture(scope='session', autouse=True)
def restore_stack():
    restore()
    yield
    restore()


@pytest.fixture(autouse=True)
def restore_after_test():
    """Keep failures independent: a test that dies mid-fault must not break the next one."""
    yield
    restore()
