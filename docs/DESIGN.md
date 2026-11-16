# Pulse design notes

Three decisions shape how Pulse behaves when things go wrong. Each note covers the problem, the choice, the alternatives considered, and the consequences, with pointers to the code and the checks that exercise it.

## 1. Stable sample identities across retries

**Problem.** An agent cannot always tell whether a batch reached the database. A connection can fail after the backend commits but before the response arrives. If the agent retries, the backend sees the same samples twice. If it gives up, data is lost.

**Decision.** Every sample carries an identity chosen by the agent: `(node_id, session_id, sequence)`.

- `session_id` is 128 random bits generated when the agent starts.
- `sequence` increments once per successful collection within that session.
- The identity is the primary key of `samples`. Ingestion uses `INSERT … ON CONFLICT DO NOTHING RETURNING`, so a repeated identity is ignored and the response reports only newly inserted rows.
- A retry resends the identical batch. The backend acknowledges (HTTP 202) only after its transaction commits.
- Within one batch, a repeated identity keeps its first occurrence. A delayed sample is stored but never overwrites newer node metadata. Ties on collection time keep the first observation.

Code: `cmd/agent/delivery.go` (retry loop), `internal/backend/ingest.go` (set-based insert, first-occurrence and metadata rules).

**Alternatives considered.**

- *A batch-level idempotency key.* This deduplicates whole batches only. A key per sample also covers overlapping or reassembled batches.
- *Server-assigned IDs.* These cannot recognize a retry at all.
- *Collection timestamp as the key.* Clocks can repeat or move backwards, and timestamps say nothing about missing samples.
- *A random UUID per sample.* This deduplicates correctly, but loses gap detection: consecutive sequence numbers let anyone check completeness. `fault_load.py` and the outage test rely on this.

**Consequences.**

- Retries are always safe, so the agent can retry aggressively.
- Loss is measurable: a gap in a session's sequence numbers is a lost sample.
- An agent restart begins a new session, so sequence numbers never collide. Each session is checked for gaps separately.
- First write wins, so a corrected payload cannot replace a stored sample.

**Evidence.**

- `smoke.py` and `ingest_semantics.py` resend batches and check that `inserted` is 0 and stored rows are unchanged.
- The unit test `TestDeliveryRecoversAndDrainsQueue` checks that a failed send is retried with the identical batch.

## 2. Bounded memory versus telemetry loss

**Problem.** While the backend is unreachable, samples keep arriving. Buffering everything risks exhausting memory on the monitored host. Buffering nothing loses data during every brief outage.

**Decision.** Collection and delivery are independent goroutines connected by a fixed-capacity in-memory queue.

- **Limits:** the queue holds 256 samples by default, about 12.8 minutes at one sample every 3 s. One in-flight batch of up to 32 samples is held separately.
- **Overflow:** the oldest *waiting* sample is dropped, and a loss counter is incremented.
- **Retries:**
  - temporary failures (network errors, HTTP 408, 429 and 5xx) retry with capped exponential backoff and jitter (0.5 s to 10 s);
  - other HTTP errors discard the batch and count the loss.
- **Shutdown:** the agent gets up to 5 s to flush, then logs its retry and loss totals.
- Collection never blocks on delivery, so collection timestamps stay accurate during outages.

Code: `cmd/agent/delivery.go` (`enqueue`, `deliver`, `flushBatch`), `cmd/agent/main.go` (configuration, shutdown).

**Alternatives considered.**

- *Unbounded buffering.* Memory grows without limit during a long outage.
- *A disk-backed buffer.* It survives crashes, but needs disk-space policy, corruption handling and replay ordering. It is listed as an optional extension.
- *Blocking collection when the queue is full.* Collection timestamps would drift, and samples would describe the wrong moments.
- *Dropping the newest sample instead.* Incident detection needs the most recent intervals; the evaluator ignores evidence more than 10 s old. Keeping old samples and discarding new ones would delay detection exactly when it matters.

**Consequences.**

- Agent memory is bounded no matter how long an outage lasts.
- Outages shorter than the queue's span lose nothing.
- Longer outages lose the oldest history first, and the loss is counted.
- An agent crash loses whatever was queued; delivery is at-least-once only while the process lives.
- Duplicates from retries are harmless because of design note 1.

**Evidence.**

- The pytest outage scenario stops the backend for 18 s and requires every outage sample to arrive with contiguous sequence numbers.
- A unit test checks the drop-oldest policy.
- `fault_load.py` recorded no retries, drops or sequence gaps for node-1 with up to 128 concurrent synthetic senders (BENCHMARK.md).

## 3. Current heartbeat status versus historical measurements

**Problem.** Telemetry can arrive late. Buffered samples delivered after an outage, or a slow retry, describe the past. If Pulse judged reachability from the newest telemetry it received, a burst of old samples could make a disconnected node look healthy. A late sample could also hide that the agent stopped.

**Decision.** Reachability comes from heartbeats only.

- The agent posts a heartbeat every 3 s, independently of collection and delivery. Each heartbeat carries a fresh application health probe (1 s deadline).
- Heartbeats update a separate `heartbeats` table. Telemetry ingestion never touches it. `nodes.latest_collected_at` is historical metadata, not a liveness signal.
- **Agent unreachable:**
  - Opens when no heartbeat has arrived for 15 s; three successive heartbeats resolve it.
  - Staleness is measured from the later of the last heartbeat and the database time at which evaluation started. An outage of the backend or its database is therefore not blamed on agents.
- **Application unavailable:** opens after three consecutive failed probes; three successful probes resolve it.
- **Release regressions** are evaluated from telemetry, but only for nodes with a fresh heartbeat and recent samples.
- Each incident stores the evidence that opened it.

Code: `internal/backend/heartbeat.go` (heartbeat handling, evaluator, application health), `internal/backend/regression.go`, `internal/backend/fleet.go`.

**Alternatives considered.**

- *Derive liveness from the newest received telemetry.* Simple, but wrong whenever telemetry is delayed.
- *Derive liveness from the newest collection timestamp.* Delayed samples can still arrive with recent-looking timestamps after an outage, and the result depends on the agent's clock.
- *A grace period after backend start-up only.* This would not cover database outages. Re-reading the start time after any failed evaluation covers both.

**Consequences.**

- The dashboard can show a node as reachable with stale history, or unreachable with recent history, and both are reported honestly.
- An agent that loses contact is detected within about 15–17 s.
- A heartbeat proves contact, not cause: an unreachable incident means "no contact", which can be a network problem as well as a dead agent.
- Each node sends one extra small request every 3 s.

**Evidence.**

- The pytest scenarios distinguish an application failure from an agent disconnect.
- `fleet.py` shows that a node sending only telemetry stays `unknown`.
- The outage scenario asserts that no agent-unreachable incident opens after an 18 s backend outage. It failed before the fix and passes after it.
- `fault_load.py` measured unchanged detection delay under load.
