"""Interleaved comparison of prebuilt backend images under identical load.

Each round runs every variant once. Variant order rotates between rounds and
concurrency order is shuffled per round (seeded, identical for every variant in
that round), so database growth, thermal drift and background activity are
spread across variants instead of favouring whichever runs first. After each
backend switch the ingestion semantics check runs against that variant. Every
trial uses scripts/load.py with warm-up and diagnostics. The default backend
image is restored when the run ends, including on failure.

Build variants first, for example:
  docker build --build-arg SERVICE=backend -t pulse-backend:set-based .
"""
import argparse
import json
import os
import random
import statistics
import subprocess
import sys
from pathlib import Path

import load

ROOT = Path(__file__).resolve().parents[1]


def switch(image):
    env = dict(os.environ)
    if image:
        env['PULSE_BACKEND_IMAGE'] = image
    else:
        env.pop('PULSE_BACKEND_IMAGE', None)
    subprocess.run(['docker', 'compose', 'up', '-d', '--no-deps', '--no-build', '--wait', 'backend'],
                   cwd=ROOT, env=env, check=True, timeout=120, stdout=subprocess.DEVNULL)


def image_id(image):
    return load.command(['docker', 'image', 'inspect', '--format', '{{.Id}}', image])


def summarize(runs, variants, levels):
    summary = {}
    reference = variants[0][0]
    for name, _ in variants:
        summary[name] = {}
        for level in levels:
            trials = [r for r in runs if r['variant'] == name and r['concurrency'] == level]
            if not trials:
                continue
            values = lambda f: [f(r) for r in trials if f(r) is not None]
            median = lambda f: statistics.median(values(f)) if values(f) else None
            throughput = values(lambda r: r['samples_per_second'])
            entry = dict(trials=len(trials), samples_per_second_median=median(lambda r: r['samples_per_second']),
                         samples_per_second_range=[min(throughput), max(throughput)],
                         p50_ms_median=median(lambda r: r['latency_ms']['0.5']),
                         p95_ms_median=median(lambda r: r['latency_ms']['0.95']),
                         p99_ms_median=median(lambda r: r['latency_ms']['0.99']),
                         pool_wait_ms_median=median(lambda r: (r['server'] or {}).get('pool_wait_ms_per_request')),
                         statements_per_request_median=median(lambda r: (r['server'] or {}).get('statements_per_request')),
                         statement_ms_per_request_median=median(lambda r: ((r['server'] or {}).get('mean_ms') or {}).get('statement')),
                         client_cpu_cores_median=median(lambda r: r['client']['cpu_cores']))
            if name != reference:
                # Ratios are paired within a round so both trials share conditions.
                ratios = []
                for trial in trials:
                    base = next((r for r in runs if r['variant'] == reference and r['concurrency'] == level
                                 and r['round'] == trial['round']), None)
                    if base and base['samples_per_second']:
                        ratios.append(trial['samples_per_second'] / base['samples_per_second'])
                if ratios:
                    entry[f'throughput_ratio_vs_{reference}'] = dict(median=statistics.median(ratios), min=min(ratios), max=max(ratios))
            summary[name][str(level)] = entry
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--variant', action='append', required=True, metavar='NAME=IMAGE',
                        help='backend image to compare; the first is the reference (repeat for each variant)')
    parser.add_argument('--rounds', type=int, default=3)
    parser.add_argument('--concurrency', type=int, nargs='+', default=[1, 4, 8, 16, 32])
    parser.add_argument('--duration', type=float, default=15)
    parser.add_argument('--warmup', type=float, default=5)
    parser.add_argument('--batch-size', type=int, default=32)
    parser.add_argument('--seed', type=int, default=8)
    parser.add_argument('--output', required=True, help='JSON report path (overwritten)')
    args = parser.parse_args()
    variants = [tuple(v.split('=', 1)) for v in args.variant]
    if len(variants) < 2 or any(len(v) != 2 or not all(v) for v in variants):
        parser.error('provide at least two --variant NAME=IMAGE values')
    rng = random.Random(args.seed)
    report = dict(load.environment(), configuration=vars(args),
                  variants={name: dict(image=image, id=image_id(image)) for name, image in variants}, runs=[], semantics={})
    output = Path(args.output)
    try:
        for round_number in range(1, args.rounds + 1):
            shift = (round_number - 1) % len(variants)
            order = variants[shift:] + variants[:shift]
            levels = list(args.concurrency)
            rng.shuffle(levels)
            for name, image in order:
                switch(image)
                check = subprocess.run([sys.executable, '-X', 'utf8', str(ROOT / 'scripts' / 'ingest_semantics.py')],
                                       capture_output=True, text=True, timeout=60)
                report['semantics'].setdefault(name, []).append(check.returncode == 0)
                if check.returncode:
                    raise SystemExit(f'{name}: ingestion semantics failed\n{check.stdout}{check.stderr}')
                for level in levels:
                    result = load.experiment(level, args.duration, args.batch_size, args.warmup)
                    result.update(variant=name, round=round_number)
                    report['runs'].append(result)
                    report['summary'] = summarize(report['runs'], variants, args.concurrency)
                    output.write_text(json.dumps(report, indent=2), encoding='utf-8')
                    print(json.dumps(dict(round=round_number, variant=name, **load.brief(result))), flush=True)
    finally:
        switch(None)
    for name, levels in report['summary'].items():
        for level, entry in levels.items():
            ratio = next((v for k, v in entry.items() if k.startswith('throughput_ratio')), None)
            print(f"{name:>16} c={level:>3}: {entry['samples_per_second_median']:8.0f} samples/s, "
                  f"p95 {entry['p95_ms_median']:7.1f} ms" + (f", ratio {ratio['median']:.2f} [{ratio['min']:.2f}-{ratio['max']:.2f}]" if ratio else ''))
    if load.problems(report['runs']):
        raise SystemExit('Load failures or acknowledgement mismatch recorded; inspect the report.')


if __name__ == '__main__':
    main()
