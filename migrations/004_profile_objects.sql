-- leutik-tarik — profile artefacts (migration 004).
--
-- The profile itself is a blob on the filesystem; ClickHouse holds only what is
-- needed to find and judge it (build prompt §4: keep bulky payloads out of hot
-- analytical tables).
--
-- retention_seconds is per row for the same reason spans are (ADR 0006).
CREATE TABLE IF NOT EXISTS profile_objects
(
    id           String,
    service_name LowCardinality(String),
    profile_type LowCardinality(String),
    started_at   DateTime64(9, 'UTC'),
    bytes        UInt64,
    path         String,
    ingested_at  DateTime64(9, 'UTC'),
    retention_seconds UInt32 DEFAULT 1209600
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(started_at)
ORDER BY (service_name, started_at, id)
TTL toDateTime(started_at) + toIntervalSecond(retention_seconds);
