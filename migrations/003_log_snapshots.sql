-- leutik-tarik — log snapshots (migration 003).
--
-- This table is NOT a log store. Rows arrive only when a trigger fires
-- (an error span, or a span slower than the threshold), and only for a bounded
-- window around it. Nothing here is captured "just in case" (build prompt §8).
--
-- Retention is per row for the same reason spans are: TTL rule sets OR together
-- and a catch-all overrides them (ADR 0006).
--
-- `exact` records whether the log line carried the trace id itself (direct
-- evidence) or was matched by service+time only (correlation). Storing the
-- difference is what lets the UI stop pretending the two are the same.
CREATE TABLE IF NOT EXISTS log_snapshots
(
    trace_id     String,
    span_id      String DEFAULT '',
    service_name LowCardinality(String),
    severity     LowCardinality(String),
    body         String,
    ts           DateTime64(9, 'UTC'),
    source       LowCardinality(String),
    exact        UInt8,
    -- Distinguishes two different lines that share a timestamp; without it the
    -- ReplacingMergeTree sort key collapses them and a snapshot loses rows.
    body_hash    UInt64 MATERIALIZED cityHash64(body),
    ingested_at  DateTime64(9, 'UTC'),
    retention_seconds UInt32 DEFAULT 604800,
    INDEX idx_log_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 1
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(ts)
ORDER BY (service_name, trace_id, ts, body_hash)
TTL toDateTime(ts) + toIntervalSecond(retention_seconds)
SETTINGS non_replicated_deduplication_window = 1000;
