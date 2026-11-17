# ADR 0006 — Retention is a per-row column, not a TTL rule set

Status: accepted.
Date: 2026-11-17. Measured on ClickHouse 26.10.5.

## Context

Build prompt §10 asks for per-rule retention selected by method, normalized
route, outcome, and operation class. The obvious ClickHouse shape is several
conditional TTL expressions:

```sql
TTL ts + INTERVAL 2 DAY  DELETE WHERE http_method = 'GET' AND outcome = 'success',
    ts + INTERVAL 30 DAY DELETE WHERE outcome IN ('server_error','error'),
    ts + INTERVAL 14 DAY                              -- catch-all
```

## What the measurement showed

That shape is **wrong**. ClickHouse deletes a row when **any** TTL expression
matches, so the catch-all applies to every row and silently overrides the more
generous rules. Measured, with the rules above:

| Row | Expected | Observed |
|---|---|---|
| GET + success, 1d old | keep (2d) | kept |
| GET + success, 3d old | drop | dropped |
| POST + success, 5d old | keep (14d) | kept |
| POST + success, 20d old | drop | dropped |
| **POST + server_error, 25d old** | **keep (30d)** | **dropped** ← wrong |
| POST + server_error, 40d old | drop | dropped |

A failure being evicted 5 days early is the opposite of what an evidence
platform should do.

## Decision

Store the retention on the row and keep **one** TTL expression:

```sql
retention_seconds UInt32 DEFAULT 1209600,
TTL toDateTime(start_time) + toIntervalSecond(retention_seconds)
```

The policy decides the number at ingest, the decision is persisted with the row
(`policy_id`, `policy_version`, `retention_seconds`), and the engine enforces it.

Measured, same fixtures, per-row values 2d/14d/30d: `r1, r3, r5` kept and
`r2, r4, r6` dropped — including the 25-day-old error that the rule-set shape
evicted.

## Why this is also the better design

- No precedence puzzle: one expression cannot override another.
- Retention is **data**, so changing a policy affects future rows without
  rewriting DDL, and past rows keep the decision they were given.
- It matches prompt §10.2 literally: "evaluate policy at ingestion time and
  persist the selected policy ID/version with the record".
- The Phase 4 policy engine only has to compute a duration; it does not have to
  generate or reconcile TTL clauses.

## Consequences

- TTL only fires on merge (measured: rows expired after `OPTIMIZE … FINAL`).
  Expired-but-unmerged rows can still be read until the merge happens; queries
  must therefore also filter `start_time` and never rely on TTL for correctness.
- Retention can no longer be changed retroactively by editing the table. A
  policy change is forward-only — the correct behaviour for an audit trail, and
  worth stating so nobody expects otherwise.
- The default tiers currently live in `internal/ingest` as constants. Phase 4
  replaces them with configuration; the column does not change.
