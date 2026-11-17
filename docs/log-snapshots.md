# Log snapshots (not a log store)

This is the deliberate difference from Kibana-style products: leutik-tarik does
**not** keep a log stream. Log lines live in a bounded in-memory ring, and only a
**trigger** turns them into durable rows.

## Flow

```
POST /v1/logs ──► bounded ring per service ──(overwritten continuously)
                                   │
   span arrives with an error ─────┤ trigger fires
   or slower than 2s               ▼
                        snapshot the window around the span
                                   ▼
                        log_snapshots (ClickHouse, per-row TTL)
                                   ▼
                        trace detail page: direct vs correlated
```

## Bounds

Per service, in memory, all three enforced independently:

| Bound | Default |
|---|---|
| events | 2,000 |
| bytes | 4 MiB |
| age | 30 min |

Plus per-request `MaxRecords` (5,000) and the usual 64 MiB body cap. A single
oversized line is truncated to 1/8 of the byte budget so it cannot evict
everything else. Evictions are counted (`Ring.Stats`), not hidden.

## Triggers

| Condition | Reason label |
|---|---|
| outcome `error` (OTLP status error) | `error` |
| outcome `server_error` (5xx) | `server_error` |
| duration ≥ `SlowThreshold` (default 2s) | `slow` |

No trigger, no row. A healthy fast request captures nothing, and there is a test
asserting exactly that.

## Direct vs correlated — the honesty bit

Each row stores `exact`:

- **exact = 1** — the log line carried this trace id. Direct evidence.
- **exact = 0** — the line carried no trace id; it was matched by service and
  time window. Stored under the triggering trace so the page can find it, but
  labelled a *correlation*.

The UI renders these as two separate tables. Merging them would be the easiest
way to make this product lie.

## Retention

Per-row `retention_seconds`, default 7 days, enforced by
`TTL toIntervalSecond(retention_seconds)`. Same reason as spans: TTL rule sets
OR together (ADR 0006).

## Two traps that cost a debugging round each

- **`ORDER BY` must include a content discriminator.** With
  `ORDER BY (service_name, trace_id, ts)` two different lines logged in the same
  nanosecond collapse into one under `ReplacingMergeTree` — a snapshot silently
  lost a row. Fixed with a materialised `body_hash`.
- **Correlated lines need the trace id.** They arrive with none, so a lookup by
  trace id found nothing and the UI showed only direct evidence. They are now
  written under the triggering trace with `exact = 0`.

## Not implemented

- Journald/file sources — logs arrive over OTLP only. A host-side tailer belongs
  with the agent, which is still Linux-only.
- Deduplication of repeated identical lines within one snapshot (the ring keeps
  them; the page shows them).
- Redaction of PII inside log bodies beyond credential patterns.
