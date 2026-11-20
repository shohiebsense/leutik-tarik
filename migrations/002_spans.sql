-- leutik-tarik — spans table (migration 002).
--
-- Every non-obvious choice here was measured (docs/review-2026-11-17.md,
-- docs/adr/0005-trace-retrieval-without-projection.md, adr/0006):
--
-- * Sort key leads with the search predicate (service, route, time). A
--   trace-id-only lookup then needs help, which is what the bloom index is
--   for: 5/246 granules for +10.7% storage, versus +137% for a projection.
-- * ReplacingMergeTree(ingested_at) dedupes on the sort key. span_id and
--   start_time are part of it, and a redelivered span carries identical values
--   for both, so retries collapse (measured: FINAL 1 row, plain count 2 until
--   a merge). Single-trace reads therefore use FINAL; list reads use
--   `LIMIT 1 BY trace_id, span_id`, which is cheaper than FINAL (measured 1).
-- * non_replicated_deduplication_window makes insert_deduplication_token work
--   on a non-replicated table; without it the token is ignored (measured).
-- * RETENTION IS A COLUMN, NOT A RULE SET. Multiple `TTL ... DELETE WHERE`
--   expressions are OR-ed by ClickHouse: a catch-all expression deletes rows
--   that a more generous rule wanted to keep (measured: a 25-day-old
--   server_error was dropped by the 14-day catch-all although its own rule
--   said 30 days). `TTL toIntervalSecond(retention_seconds)` makes retention
--   per-row data decided at ingest, which is also what build prompt §10.2
--   asks for (policy decided at ingestion, persisted with the record).

CREATE TABLE IF NOT EXISTS spans
(
    trace_id            String,
    span_id             String,
    parent_span_id      String DEFAULT '',

    service_name        LowCardinality(String),
    service_version     LowCardinality(String),
    environment         LowCardinality(String),

    name                String,
    kind                LowCardinality(String),
    operation_type      LowCardinality(String),

    http_method         LowCardinality(String),
    http_route          String,
    status_code         Int32,

    rpc_system          LowCardinality(String),
    rpc_service         String,
    rpc_method          String,
    grpc_status_code    Int32,

    db_system           LowCardinality(String),
    db_operation        LowCardinality(String),
    db_query_summary    String,

    messaging_system       LowCardinality(String),
    messaging_destination  String,
    messaging_operation    LowCardinality(String),

    host_name           LowCardinality(String),
    pod_name            String,
    namespace           LowCardinality(String),
    container_name      String,
    peer_service        LowCardinality(String),

    start_time          DateTime64(9, 'UTC'),
    end_time            DateTime64(9, 'UTC'),
    duration_ms         Float64,

    error_type          LowCardinality(String),
    outcome             LowCardinality(String),
    status_message      String,

    policy_id           LowCardinality(String),
    policy_version      UInt32,
    sampled             UInt8,
    source              LowCardinality(String),
    completeness        LowCardinality(String),
    redactions          UInt32,
    ingested_at         DateTime64(9, 'UTC'),

    -- Default 14 days; overwritten per row by the policy decision.
    retention_seconds   UInt32 DEFAULT 1209600,

    attributes          Map(String, String),

    INDEX idx_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 1
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(start_time)
ORDER BY (service_name, http_route, start_time, span_id)
TTL toDateTime(start_time) + toIntervalSecond(retention_seconds)
SETTINGS non_replicated_deduplication_window = 1000;
