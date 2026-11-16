# Pulse ingestion benchmark

## Summary

Instrumentation showed that ingestion was limited by **per-sample SQL round trips held inside a transaction**, not by commit fsyncs, the client, or Go CPU. A 32-sample batch issued about 65–97 sequential statements. lib/pq uses two network round trips per parameterized statement. Each pooled connection therefore stayed busy for the whole exchange. Above eight senders, requests queued for the ten-connection pool.

Replacing the per-sample loop with one set-based `INSERT … SELECT FROM unnest(…) ON CONFLICT DO NOTHING RETURNING` reduced a batch to about three statements. In an interleaved three-variant experiment, set-based ingestion delivered **2.5–3.3× the throughput** of the per-node implementation (median of per-round paired ratios at each concurrency level). It also cut median p95 latency from 291 ms to 92 ms at 16 senders. The earlier per-node change gave a smaller but consistent **1.27–1.53×** improvement over the original per-sample node insert.

All 45 trials completed with zero failed requests. All 2,989,376 acknowledged samples were found in PostgreSQL. The ingestion semantics check passed against every variant after every backend switch.

## Variants

| Name | Ingestion per 32-sample single-node batch | Reproduce from current tree |
| --- | --- | --- |
| `per-sample-node` | node INSERT + sample INSERT + node UPDATE for every sample (original) | `git apply benchmarks/variants/per-sample-node.patch` |
| `per-node` | node INSERT once per distinct node; per-sample INSERT + UPDATE | `git apply benchmarks/variants/per-node.patch` |
| `set-based` | node INSERT per node, one multi-row sample INSERT, one UPDATE per node (current) | current code |

All three include the same timing instrumentation; only the ingestion statements differ. Semantics are unchanged: in-batch and retry duplicates keep the first occurrence; inserted counts include only new rows; delayed samples never overwrite newer node metadata; equal timestamps keep the first observation. `scripts/ingest_semantics.py` checks these behaviors against a running backend.

## Method

- Driver: `scripts/ab_benchmark.py` runs `scripts/load.py` trials against prebuilt backend images, switching only the backend container.
- Each trial: 5 s unrecorded warm-up, then a 15 s measurement window. Throughput and latency count only requests completed inside the window. Closed loop: each sender is one simulated node submitting 32-sample batches, with no retries.
- Three rounds. Variant order rotates each round. Concurrency order (1, 4, 8, 16, 32) is shuffled per round with seed 8, identically for every variant within that round. Ratios are paired within a round.
- Diagnostics per trial:
  - **Backend (loopback-only `127.0.0.1:6060`):** expvar deltas for `database/sql` pool waits, and ingestion phase timings: transaction begin including pool acquisition, statements, commit.
  - **Go runtime:** allocation counters.
  - **PostgreSQL:** session state and wait events from `pg_stat_activity` (sampled every 200 ms), plus commit/WAL counters from `pg_stat_database` and `pg_stat_wal`.
  - **Client:** process CPU, plus payload build time.
  - **Docker:** container CPU/memory snapshots.
- Environment: measured 15–16 November 2026 (local time) on an Intel Core i7-8665U (4 cores / 8 logical processors). Docker Desktop 29.9.2 reported 8 CPUs and 15.48 GiB. Windows 11, Python 3.14 client on the same host. Existing data (about 0.5 M samples at the start) and normal agent traffic remained present.

```powershell
docker compose up --build -d --wait
docker build --build-arg SERVICE=backend -t pulse-backend:set-based .
# For each earlier variant: copy the tree, apply its patch in the copy, then
#   docker build --build-arg SERVICE=backend -t pulse-backend:<name> <copy>
python scripts/ab_benchmark.py --variant per-sample-node=pulse-backend:per-sample-node --variant per-node=pulse-backend:per-node --variant set-based=pulse-backend:set-based --rounds 3 --concurrency 1 4 8 16 32 --duration 15 --warmup 5 --seed 8 --output benchmarks/ab-ingestion.json
```

Single-variant runs use `python scripts/load.py --duration 15 --warmup 5 --repeats 3 --output <new-file>.json`. `--output` is required and is overwritten. Choose a new file to preserve earlier results.

## Results

Median of three trials per cell (range of throughput in brackets). Latency percentiles are per-trial nearest-rank values, then the median across trials.

| Senders | Variant | Samples/s | p50 ms | p95 ms | p99 ms | Paired ratio vs per-sample-node |
| ---: | --- | ---: | ---: | ---: | ---: | --- |
| 1 | per-sample-node | 354 [337–384] | 78 | 161 | 186 | — |
| 1 | per-node | 576 [474–587] | 50 | 79 | 95 | 1.53 [1.41–1.63] |
| 1 | set-based | 1,175 [1,060–1,882] | 23 | 46 | 56 | 3.49 [2.99–4.90] |
| 4 | per-sample-node | 1,380 [1,318–1,425] | 91 | 110 | 122 | — |
| 4 | per-node | 1,771 [1,771–1,815] | 69 | 86 | 95 | 1.32 [1.24–1.34] |
| 4 | set-based | 5,828 [5,786–6,782] | 19 | 36 | 44 | 4.39 [4.22–4.76] |
| 8 | per-sample-node | 2,027 [1,956–2,063] | 122 | 151 | 166 | — |
| 8 | per-node | 2,594 [2,543–2,667] | 95 | 121 | 138 | 1.29 [1.25–1.33] |
| 8 | set-based | 6,609 [5,694–7,087] | 35 | 61 | 87 | 3.26 [2.91–3.44] |
| 16 | per-sample-node | 2,221 [2,095–2,319] | 211 | 383 | 500 | — |
| 16 | per-node | 2,818 [2,726–2,995] | 170 | 291 | 371 | 1.29 [1.27–1.30] |
| 16 | set-based | 8,000 [6,805–9,188] | 62 | 92 | 112 | 3.82 [3.06–3.96] |
| 32 | per-sample-node | 2,236 [1,790–2,259] | 364 | 1,063 | 1,611 | — |
| 32 | per-node | 2,641 [2,624–2,833] | 304 | 896 | 1,327 | 1.27 [1.16–1.48] |
| 32 | set-based | 7,548 [7,386–7,836] | 112 | 294 | 446 | 3.38 [3.27–4.38] |

Set-based vs per-node, paired by round (median [min–max]):

| Senders | 1 | 4 | 8 | 16 | 32 |
| --- | --- | --- | --- | --- | --- |
| Ratio | 2.48 [1.84–3.21] | 3.27 [3.21–3.83] | 2.60 [2.19–2.66] | 2.93 [2.41–3.07] | 2.81 [2.66–2.97] |

Peak sampled container CPU (100% = one logical CPU):

| Variant | Backend | PostgreSQL |
| --- | ---: | ---: |
| per-sample-node | 211% | 428% |
| per-node | 197% | 418% |
| set-based | 152% | 466% |

Peak sampled backend memory remained in the tens of MiB.

## Where the time went

Median per-request values at 16 senders (32 senders in parentheses):

| Measure | per-sample-node | per-node | set-based |
| --- | ---: | ---: | ---: |
| Statements per request | ~95 | ~65 | 3 |
| Begin incl. pool wait, ms | 80 (309) | 61 (259) | 12 (79) |
| Statements, ms | 140 (139) | 109 (117) | 31 (34) |
| Commit, ms | 2.2 | 2.3 | 5.4 |
| Server handler total, ms | 223 (451) | 174 (379) | 50 (121) |
| PostgreSQL sessions "idle in transaction" (waiting for backend) | 7.0 | 6.3 | 3.0 |
| PostgreSQL sessions executing (CPU) | 2.3 | 2.7 | 4.7 |
| WAL syncs per commit | 0.96 | 0.96 | 0.76 |
| Go allocations per request | 2,287 | 1,735 | 588 |
| Client CPU (cores) | 0.17 | 0.20 | 0.63 |
| Client latency minus server handler, ms | 7.3 | 7.3 | 13.0 |

Interpretation:

- **Round trips dominated.** With the per-sample variants, about 66–72% of in-transaction PostgreSQL sessions were idle, waiting for the backend's next statement, and statement time was roughly 1.5–1.8 ms per statement. The ten pooled connections stayed busy in these exchanges. From 16 senders onward, the time spent acquiring a connection exceeded the SQL time itself.
- **Not fsync-bound.** Commit took about 2 ms, with roughly one WAL sync per commit. WAL bytes per commit (~27 KiB) were the same across variants.
- **Not client-bound.** Before the change, the Python client used 0.2 cores and added about 7 ms per request.
- **After the change, PostgreSQL does more useful work.** Executing sessions doubled, Docker CPU for PostgreSQL reached 466%, and some WAL write/sync waits appeared. Throughput plateaus between 8 and 16 senders, and pool waits return at 32 senders (78 ms).
- **Remaining limit: likely PostgreSQL CPU on this shared 8-thread host, plus the ten-connection pool.** That is an inference, not a proven bottleneck. The Python client now uses 0.65 cores, so client overhead is no longer negligible at high rates.

### Go CPU profile (set-based, 16 senders)

A 10-second CPU profile was taken from `http://127.0.0.1:6060/debug/pprof/profile` during a separate 16-sender run at about 7,990 samples/s.

- **Usage:** the backend used about 1.1 cores of 8.
- **Breakdown:**

  | Share of backend CPU | Source |
  | ---: | --- |
  | ~30% | network syscalls (lib/pq socket writes and reads) |
  | ~16% (cumulative) | `insertSamples`, including array encoding |
  | ~9% | JSON request decoding |
  | ~12% | GC background marking |

- **Allocation profile (cumulative since process start):** dominated by JSON decoding buffers and lib/pq array/text encoding.
- **Conclusion:** Go CPU is not the current limit. Further backend gains would come from fewer protocol round trips (for example, lib/pq's two-step parse/execute or a pgx batch/COPY path), not from Go computation.

Capture one with:

```powershell
curl.exe -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=10"
go tool pprof -top cpu.pprof
```

## Agent reliability and incident detection under load

`scripts/fault_load.py` measured the set-based backend at 0, 16, 64 and 128 synthetic senders. There were two passes, the second in reverse order, on 16 November 2026 (UTC).

At each level:

- **Baseline load:** the real node-1 agent kept collecting every 3 s, and a client drove about 30 requests/s to the sample application.
- **Faults, one at a time:** slow release (v2-slow), error release (v2-errors), application stop, and agent stop. After each fault, the script repaired it and waited for the incident to resolve.
- **Detection and recovery timing:** measured from the moment the fault was in place (deployed container healthy, or service stopped) until the incident API first showed the change, polled every 0.25 s.
- **Agent retries and drops, counted two ways:**
  - agent log warnings: retries, queue-overflow drops, rejected batches, heartbeat failures;
  - gaps in node-1's per-session sequence numbers in PostgreSQL.
- **Delivery delay:** `received_at − collected_at` for node-1 samples.

```powershell
python scripts/fault_load.py --concurrency 0 16 64 128 --repeats 2 --output benchmarks/fault-load.json
```

| Background senders | Ingest samples/s | Synthetic p95 ms | Mean pool wait ms | Slow release detect / recover s | Error release detect / recover s | App stopped detect / recover s | Agent stopped detect / recover s | Agent delivery delay p95 / max s |
| ---: | ---: | ---: | ---: | --- | --- | --- | --- | --- |
| 0 | — | — | 0 | 10.5–14.0 / 11.5–13.1 | 9.7–13.6 / 11.6–13.1 | 6.9–7.5 / 7.3–7.5 | 14.0–14.4 / 5.8–5.9 | 3.0 / 6.0 |
| 16 | 7,439 | 107 | 12 | 11.0–13.4 / 12.6–13.9 | 9.5–11.3 / 12.0 | 7.1–7.3 / 9.2–9.5 | 14.0–14.3 / 6.1 | 4.2 / 6.0 |
| 64 | 7,490 | 703 | 214 | 10.3–11.8 / 12.0–14.3 | 12.5–13.3 / 11.7–12.5 | 7.1–7.8 / 9.0–9.3 | 14.4–15.1 / 6.4–6.5 | 4.7 / 6.2 |
| 128 | 7,518 | 1,524 | 485 | 12.3–13.3 / 11.9–13.5 | 9.5–10.2 / 12.2–15.2 | 6.7–7.3 / 9.5–10.8 | 12.4–14.8 / 6.1–6.4 | 5.6 / 6.4 |

Ranges span the two passes. Ingest throughput and p95 are medians of the synthetic senders' two runs. Mean pool wait covers only requests that waited.

Findings:

- **No agent retries, drops or lost samples at any level.** The agent logged no retry, queue-drop, rejected-batch or heartbeat-failure events. PostgreSQL showed no sequence gaps: 290 node-1 samples expected and stored across eight runs.
  - The 2–6 "sample collection failed" warnings per run happened at every level, including 0. They come from the deliberate sample-application restarts and stops, not from load.
- **No false incidents.** No incident opened outside the scenario meant to cause it, including at 128 senders.
- **Detection delay did not degrade with load.** It stayed within the same ranges as the unloaded runs:
  - about 10–14 s for release regressions (three agreeing 3 s intervals plus flush and evaluation),
  - about 7 s for an unavailable application (three failed 3 s health checks),
  - about 12–15 s for an unreachable agent (15 s heartbeat timeout counted from the last heartbeat, which precedes the stop).
- **Application recovery slowed slightly under load:** 7.3–7.5 s unloaded versus 9.0–10.8 s at 64–128 senders.
- **Delivery delay grew modestly.** The p95 rose from 3.0 s, which is the agent's flush interval, to 5.6 s at 128 senders, as the agent's requests queued for the same saturated connection pool.
- **Saturation shows on synthetic senders first.** At 128 senders, throughput stayed flat at about 7,500 samples/s, mean pool wait reached about 0.5 s, and 5 synthetic requests failed with the backend's 5-second request timeout ("context deadline exceeded", HTTP 503).
  - The agent did not hit that limit during these windows. If it had, it would retry with backoff.

Limits of this measurement:

- One real agent, sending one sample every 3 s, is a very small sample for loss statistics. About 36 samples per run cannot reveal rare losses.
- Retry and drop counts come from log lines; the agent exposes no live counters.
- Each scenario ran twice per level.
- Detection delay is measured from the client's view of when the fault was in place. Container start and stop times add a few hundred milliseconds of uncertainty.

## Limits

- Client, backend and database share one laptop; Docker CPU is shared, and thermal or background activity can shift results. Interleaving spreads drift across variants but does not remove it.
- Three rounds of 15-second windows. The set-based single-sender cell varies widely (1,060–1,882). Long-duration, sustained-capacity behavior is not established.
- Closed-loop, synthetic batches of 32 samples from one node each. Real agents send smaller, slower batches. Results describe ingestion capacity, not end-to-end agent behavior.
- The database grew by about 3 M rows during the experiment; earlier and later trials saw different index sizes.
- Docker stats and wait events are periodic samples. `pg_stat_statements` is not enabled, so per-query server execution time is inferred from client-side statement timing and wait sampling.
- Agent reliability and detection delay under load use a single agent and two repetitions (see the section above).
- Raw per-trial data, including every diagnostic, is in `benchmarks/ab-ingestion.json`. It stores percentile summaries, not every individual latency.

## History

The earlier repeated benchmark (`benchmark-results.json`, per-node implementation, 15 s trials, no warm-up, fixed ascending order, no diagnostics) measured median samples/s of 393/1,876/2,507/2,737/2,726 at 1/4/8/16/32 senders. The interleaved per-node results above are consistent with it. An earlier short eight-sender before/after trial suggested about 21% improvement for the per-node change. The controlled experiment measured 1.29× [1.25–1.33] at eight senders.
