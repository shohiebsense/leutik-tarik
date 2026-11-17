-- SUPERSEDED — do not apply. See docs/review-2026-11-17.md (B4, B5, M6, M7)
-- and the DDL v2 step (S4) in PLAN.md. This version has a sort key that makes
-- the single-trace lookup a full scan, stores duplicate spans, and hardcodes
-- one 14-day retention rule.
--
-- leutik-tarik — ClickHouse schema (next slice: clickhouse store).
-- The memory store satisfies the same store.Store interface today.
-- Trace/span IDs are hex strings in the OTLP/HTTP-JSON slice.

CREATE TABLE IF NOT EXISTS spans (
    trace_id        String  NOT NULL,
    span_id         String  NOT NULL,
    parent_span_id  String  DEFAULT '',
    service_name    LowCardinality(String),
    service_version LowCardinality(String),
    environment     LowCardinality(String),
    name            String,
    kind            LowCardinality(String),
    http_method     LowCardinality(String),
    http_route      String,
    status_code     Int32,
    start_time      DateTime64(9, 'UTC'),
    end_time        DateTime64(9, 'UTC'),
    duration_ms     Float64,
    error_type      LowCardinality(String),
    outcome         LowCardinality(String)
)
ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(start_time)
ORDER BY (service_name, http_route, outcome, start_time, trace_id)
TTL toDateTime(start_time) + INTERVAL 14 DAY;
