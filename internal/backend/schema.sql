CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    latest_collected_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS samples (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    session_id TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    collected_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    version TEXT NOT NULL,
    requests BIGINT NOT NULL CHECK (requests >= 0),
    errors BIGINT NOT NULL CHECK (errors >= 0 AND errors <= requests),
    total_latency_ms DOUBLE PRECISION NOT NULL CHECK (total_latency_ms >= 0),
    memory_bytes BIGINT NOT NULL CHECK (memory_bytes >= 0),
    PRIMARY KEY (node_id, session_id, sequence)
);
CREATE INDEX IF NOT EXISTS samples_node_time ON samples(node_id, collected_at);
