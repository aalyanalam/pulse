# Pulse

Pulse is a machine telemetry and diagnostics platform for investigating whether a software release made monitored applications slower or less reliable. Its local demo represents each machine as a sample Go application and a companion agent; release detection and the dashboard are planned milestones.

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

Compose supplies service hostnames instead of localhost. The backend creates its initial tables on startup. This bootstrap is intentionally simple; introduce versioned migrations before changing deployed schemas.

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

- `/work` performs real SHA-256 operations. Request count and total elapsed time cover `/work` only; `/stats` and health probes do not inflate workload counters. Errors currently count response-write failures; deterministic HTTP faults come in the regression milestone.
- Counters are cumulative since application start. `total_latency_ms / requests` is a lifetime mean, not a percentile or rolling-window statistic. Counters reset when the application restarts; an application process identity is needed before calculating robust interval deltas.
- `memory_bytes` is Go's currently allocated heap (`runtime.MemStats.Alloc`), not process RSS, container usage or host memory.
- Node IDs and application version labels are configured simulation inputs. No physical device or host CPU is monitored.
- The first agent collects and sends sequentially. Failed collections or sends lose that sample, and a slow send delays collection. Bounded buffering, stable retry batches, backoff and final shutdown flush belong to milestone 4.
- Latest collection time is historical metadata, **not a heartbeat or health status**. Clock synchronization is assumed; timestamps over five minutes in the future are rejected. Independent heartbeat freshness and incident evaluation are not implemented yet.
- Local demo credentials, no authentication or TLS, no retention policy. This setup is intended for local development.

## Development and checks

Use Go 1.23 or later (the container and CI use Go 1.27.3). The only external Go dependency is the PostgreSQL driver.

```sh
go test ./...
go vet ./...
go build ./...
go test -race ./...
```

The race detector requires a supported C compiler and CGO. GitHub Actions is configured to run unit/race checks, the Compose smoke check, and a backend-restart persistence check.

### Verification recorded on 2026-10-21

Go build and vet succeeded locally. Unit test results are recorded in the roadmap working notes. Docker is not installed in the current environment, so Compose, PostgreSQL integration and persistence checks still need to run. Local race checking is unavailable without a C compiler; the Linux CI job includes it. CI has been authored but has not run on GitHub.

## Next milestone

Run the container integration checks, then expand the model with application process identity and incidents. Follow with the bounded agent queue and retry tests before implementing regression detection or a dashboard.
