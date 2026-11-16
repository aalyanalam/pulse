# Pulse

Pulse is a machine telemetry and diagnostics platform for investigating whether a software release made monitored applications slower or less reliable. Its local demo represents each machine as a sample Go application and a companion agent; release detection and version comparisons are implemented; an investigation dashboard is available locally.

## Obsidian vault and handoff

Open this repository folder as an Obsidian vault. Project notes and portable vault settings are versioned with the code; device-specific workspace layouts stay local. Read [HANDOFF.md](HANDOFF.md) for current state, verification gaps, and next actions. Ask to update the handoff whenever you want the latest work recorded; [AGENTS.md](AGENTS.md) defines that workflow for future coding sessions.

## Current implementation

The first vertical slice includes a sample application, polling agent, HTTP ingestion and query API, PostgreSQL storage, and Docker Compose setup. Samples retain their original version and collection timestamp; repeated sample identities are stored once. The full roadmap is in [Pulse - Project Roadmap.md](Pulse%20-%20Project%20Roadmap.md).

```mermaid
flowchart LR
    Workload[HTTP workload] --> Application[Sample Go application]
    Application -->|GET /stats| Agent[Go agent]
    Agent -->|POST telemetry batch| Backend[Go API]
    Backend --> PostgreSQL
    Client[API client] -->|Query node measurements| Backend
```

## Run locally

Install Docker with Compose support, then run from this directory:

```sh
docker compose up --build -d --wait
```

The backend listens on `http://localhost:8080`; the sample application listens on `http://localhost:8081`. PostgreSQL uses a named volume, and is not exposed on the host. Both HTTP ports bind only to loopback.

With Python 3 available, exercise the complete measurement path:

```sh
python scripts/smoke.py
```

The smoke check generates 20 real requests, waits for their counters to reach the API, and checks duplicate ingestion, collection-time ordering and historical versions. Each run leaves a uniquely named `smoke-*` node for inspection.

```sh
curl http://localhost:8081/work
curl http://localhost:8081/stats
curl http://localhost:8080/api/v1/nodes
```

To retrieve samples, supply RFC3339 `from` and `to` timestamps spanning no more than 24 hours. The interval is inclusive at `from` and exclusive at `to`; `limit` defaults to 100 and cannot exceed 1000. For example, replace these timestamps with the period when your stack was running:

```sh
curl 'http://localhost:8080/api/v1/nodes/node-1/metrics?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z&limit=100'
```

Nodes are ordered by ID with at most 100 per response. Request the next page with `?after=<last-id>`. Metrics return the earliest samples in the requested interval up to the limit; narrow the time interval if it contains more results.

Stop the stack with `docker compose down`. Data remains in the named volume. `docker compose down -v` intentionally deletes that data.

## Configuration

| Service | Variable | Default |
| --- | --- | --- |
| Application | `LISTEN_ADDR` | `:8081` |
| Application | `APP_VERSION` | `v1` |
| Backend | `LISTEN_ADDR` | `:8080` |
| Backend | `DATABASE_URL` | `postgres://pulse:pulse@localhost:5432/pulse?sslmode=disable` |
| Agent | `NODE_ID` | `node-1` |
| Agent | `APP_URL` | `http://localhost:8081` |
| Agent | `BACKEND_URL` | `http://localhost:8080` |
| Agent | `COLLECT_INTERVAL` | `3s` |
| Agent | `QUEUE_CAPACITY` | `256` |
| Agent | `BATCH_SIZE` | `32` |
| Agent | `FLUSH_INTERVAL` | `3s` |

Compose supplies service hostnames instead of localhost. The backend applies numbered migrations on startup, recording versions in schema_migrations. A transaction and advisory lock serialize upgrades. Migration 1 adopts the original tables; migration 2 adds app_process_id without deleting historical samples. Older samples and older clients use an empty ID, meaning unknown process identity. Migrations are forward-only.

## API

- `POST /api/v1/telemetry/batches`: accept 1–100 samples in a JSON `samples` array, with a 1 MiB request limit; return `202` and the number newly inserted after transaction commit.
- `GET /api/v1/nodes`: current version and latest collection timestamp.
- `GET /api/v1/nodes/{id}/metrics`: bounded historical samples, including collection and receipt timestamps.
- `GET /healthz`: process liveness.
- `GET /readyz`: backend database connectivity.

Sample body:

```json
{"samples":[{"node_id":"node-1","session_id":"unique-agent-session","sequence":1,"collected_at":"2026-10-21T12:00:00Z","version":"v1","requests":10,"errors":0,"total_latency_ms":15.2,"memory_bytes":1048576}]}
```

The primary key is `(node_id, session_id, sequence)`. Duplicate keys preserve the first accepted payload. A delayed sample can add history but cannot replace a node's newer version. The agent generates a random session ID at startup and increments its sequence within that session.

## Measurement meaning and limitations

- `/work` performs real SHA-256 operations. Request count and total elapsed time cover `/work` only; `/stats` and health probes do not inflate workload counters. Errors count response-write failures and the deterministic HTTP 500 responses in v2-errors.
- Counters are cumulative since application start. `total_latency_ms / requests` is a lifetime mean, not a percentile or rolling-window statistic. Counters reset when the application restarts; app_process_id changes each time the sample application starts. Only compare counters within the same nonempty process ID; comparison queries calculate interval deltas within each process.
- `memory_bytes` is Go's currently allocated heap (`runtime.MemStats.Alloc`), not process RSS, container usage or host memory.
- Node IDs and application version labels are configured simulation inputs. No physical device or host CPU is monitored.
- The agent collects independently of delivery. It sends batches when full or at the flush interval, and retries network failures, HTTP 408/429, and server errors with capped exponential backoff and jitter. Retries keep the original sample identities. When the in-memory queue fills, the oldest waiting sample is dropped. The in-flight batch occupies up to one extra batch of memory. Other HTTP responses discard the batch. On shutdown, the agent allows up to five seconds to flush and logs retry and loss totals. A crash can lose unsent samples.
- Latest collection time is historical metadata, **not a heartbeat or health status**. Clock synchronization is assumed; timestamps over five minutes in the future are rejected. Independent heartbeat freshness and incident evaluation are described below.
- Local demo credentials, no authentication or TLS, no retention policy. This setup is intended for local development.

## Development and checks

Use Go 1.23 or later (the container and CI use Go 1.27.3). The only external Go dependency is the PostgreSQL driver.

```sh
go test ./...
go vet ./...
go build ./...
go test -race ./...
```

The race detector requires a supported C compiler and CGO. On Windows, run it in Linux with `docker run --rm -v "C:\code\pulse:/app:ro" -w /app golang:1.27.3 go test -race ./...`.

GitHub Actions is configured to run vet and race tests, build and start Compose (including the dashboard), then run the smoke, ingestion-semantics, comparison and fleet checks, a backend-restart persistence check, and the full pytest fault-scenario suite. A successful run on GitHub has not yet been verified. See HANDOFF.md for the latest locally recorded validation.

## Next milestone

Review the dashboard in a browser, then expand the Python fault-scenario validation harness.


## Agent reachability

The agent posts a fresh heartbeat every three seconds independently of collection and queued delivery. Migration 3 adds heartbeat and incident tables. The backend checks every two seconds and opens an agent_unreachable incident after 15 seconds without contact. Three successive heartbeats with gaps below 15 seconds resolve it. Only nodes that have sent a heartbeat participate. Backend outages also cause missing contact, so an incident establishes loss of contact rather than its cause.

GET /api/v1/nodes/{id}/incidents returns the latest 100 incidents with opened_at and nullable resolved_at. Historical telemetry never updates heartbeat freshness. These endpoints are unauthenticated and intended for the local demo. Application failure detection and latency/error rules are described below.


### Application availability

Each heartbeat now includes a fresh /healthz probe result with a one-second deadline. Three consecutive failed probes open application_unavailable, and three successful probes resolve it. Missing reports and heartbeat gaps over 15 seconds reset the observation streaks. Migration 4 permits separate open incidents for agent reachability and application availability. Application incidents remain open during agent disconnection until healthy probes confirm recovery. The probe establishes HTTP availability only.


## Controlled release regression demo

Set APP_VERSION to v2-slow for a 200 ms workload delay, v2-errors for every third workload request to return HTTP 500, or v1 for healthy behavior. Recreate sampleapp through Docker Compose after changing the environment variable.

The evaluator uses the latest four samples to calculate three adjacent intervals within the same application process and version. Each interval needs at least five requests and a gap no longer than ten seconds. Three intervals above 10% errors or 100 ms mean latency open elevated_errors or slow_responses. Three intervals below 5% errors or 50 ms resolve the corresponding incident. Recent telemetry and a fresh agent heartbeat are required. Low traffic leaves incidents open until sufficient recovery evidence exists. Incident responses include the version that triggered detection. These rules identify threshold violations associated with a version. Version-to-version comparison queries are described below.

Run python scripts/regression.py against the running stack to exercise healthy traffic, both faulty releases, and rollback recovery. The script restores sampleapp to v1 when it exits.

## Version comparison

`GET /api/v1/nodes/{id}/compare` accepts six required query parameters:
`baseline_version`, `baseline_from`, `baseline_to`, `candidate_version`,
`candidate_from`, and `candidate_to`. Each RFC3339 window spans at most 24 hours,
includes its start, and excludes its end. Windows may overlap.

The response contains `node_id`, `baseline`, and `candidate`. Each side reports
its version/window, requests, errors, error_rate (fraction), mean_latency_ms,
intervals, observed_seconds, and observed_nodes (0 or 1 for this node endpoint).
Latency is total interval latency divided by total interval requests, not an
average of sample averages. Rates are null when no requests can be measured.
Unknown nodes or absent versions return empty evidence, not healthy zero rates.

Only adjacent samples inside each window with the same known application process
and version contribute. Counter resets, invalid deltas, equal timestamps, and
gaps over ten seconds are excluded. No boundary interpolation or pre-window
sample is used; totals describe observed intervals, not all traffic in the window.
Up to 20,000 samples across both windows are evaluated; denser queries return
422 and must be narrowed. A five-second database deadline bounds query execution.
Version differences show correlation, not proof of causation.

Run `python scripts/comparison.py` for deterministic database-backed edge cases.
`python scripts/regression.py` also verifies comparisons for actual slow/error
releases and restores v1 after the experiment.


## Investigation dashboard

Open http://localhost:3000 after `docker compose up --build -d --wait`.
The React/TypeScript dashboard includes a paginated fleet overview, node latency
and error charts, version observations, incident history, and two-window release
comparisons. Data polls every five seconds. Unknown heartbeat status, stale
telemetry, failed requests, and missing interval evidence are shown separately.
Fleet status uses the new `GET /api/v1/fleet?after=<id>` endpoint (100 nodes per
page), including heartbeat-only nodes, last contact, latest version and Go heap.
Node charts cover the last 15 or 30 minutes, at most 1,000 samples, with an
explicit limit notice. Incident history shows the latest 100 records.

For frontend development use `npm ci --prefix web` then `npm run dev --prefix web`.
Vite proxies API calls to localhost:8080; Compose serves the production build via
Nginx with a same-origin API proxy. `npm run build --prefix web` checks TypeScript
and produces the production bundle. `python scripts/fleet.py` checks fleet status
against PostgreSQL. Browser layout and interaction verification remain pending.

## Fault scenario validation

Install test dependencies with `python -m pip install -r requirements-test.txt`,
then run `python -m pytest -v -s tests/test_scenarios.py` from the repository root.
The suite uses the local Compose stack, stops and restarts services, generates
workload, deploys faulty versions, and restores v1. It exercises ingestion and
deduplication, application failure, agent disconnection, backend outage buffering,
and release regression detection and rollback. It leaves test telemetry and
incident history in the database. The outage test holds the backend offline for
12 seconds to allow multiple agent collection intervals.

After every test, and at the start and end of the session, the harness runs
`docker compose up -d --wait` with the default configuration (healthy v1, default
backend image), so one failed scenario does not break the next. A failed test
attaches Compose state, recent service logs, node-1 incidents and backend pool
counters to the pytest report; set `PULSE_DIAGNOSTICS_DIR` to also save them as
files (CI uploads them as an artifact). If a run is killed, restore the stack with
`docker compose up -d --wait`. `python scripts/ingest_semantics.py` separately checks
duplicate handling, delayed samples and node metadata ordering.

## Load benchmark

Run `python scripts/load.py --output <new-file>.json` for three repetitions at
1, 4, 8, 16, and 32 concurrent senders. Each trial has a warm-up (`--warmup`)
before its measurement window (`--duration`). Results include latency
percentiles, failures, acknowledged and independently counted stored samples,
connection-pool waits and per-phase ingestion timings from the backend's
loopback-only diagnostics listener (`127.0.0.1:6060`, which also serves Go
`pprof`), PostgreSQL wait events and WAL counters, client CPU, and Docker
resource snapshots. `scripts/ab_benchmark.py` compares prebuilt backend images
with interleaved, repeated trials. `scripts/fault_load.py` triggers each incident
type under background ingestion load and reports detection/recovery delay, agent
retries and drops, sequence gaps and delivery delay. The tests add persistent load-test data. See
BENCHMARK.md for method, results and limits.
