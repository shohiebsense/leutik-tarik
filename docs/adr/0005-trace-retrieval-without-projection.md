# ADR 0005 — Trace retrieval without a projection

Status: accepted (supersedes the projection proposal in review B4).
Date: 2026-11-17. Measured on ClickHouse 26.10.4, 2M spans, same data in each table.

## Problem

The main sort key is `(service_name, http_route, start_time)` so the operations
list prunes. A single-trace fetch (`WHERE trace_id = …`) constrains the *last*
kind of column and therefore cannot prune: **247/247 granules read — a full
scan**. That query is the operation-detail page, the product's centrepiece.

## Options measured

| Option | Storage | Granules for one trace | Verdict |
|---|---|---|---|
| baseline | 20.82 MiB | 247/247 | full scan |
| projection `by_trace (trace_id, start_time)` | 49.35 MiB (**+137%**) | pruned (`ReadFromPreparedSource (by_trace)`) | works, but more than doubles storage |
| `INDEX trace_id TYPE bloom_filter(0.01) GRANULARITY 4` | 23.03 MiB (+10.6%) | 8/246 | good |
| `INDEX trace_id TYPE bloom_filter(0.01) GRANULARITY 1` | 23.04 MiB (**+10.7%**) | **5/246 (98% pruned)** | **chosen** |

At 2M rows wall-clock is 4–15 ms for every variant, so latency cannot
discriminate here — the scale-invariant metric is **scan scope**, which decides
behaviour at production volume (a full scan of 247/247 granules is seconds to
minutes once the table is hundreds of GB).

## Decision

Use a **data-skipping bloom filter on `trace_id`, granularity 1**, and do not
add a projection.

- ~98% of the pruning benefit for ~11% storage instead of ~137%.
- No second copy of the data to keep consistent on merge, no projection
  maintenance after `ALTER`, no doubled insert cost.
- `bloom_filter` is the right structure because the access pattern is
  high-cardinality **equality** (`trace_id = '…'`), not range.

## Consequences and limits

- The index is consulted per granule, so the first query after a merge is the
  expensive one; acceptable for a detail page.
- `trace_id` values must be normalised (lowercase hex) before insert, or
  equal-but-differently-cased IDs will not match each other. Enforced in the
  normaliser, not the schema.
- If a future query needs to fetch a trace by Id **and** a wide time range is
  unknown, this remains ~5/246, not 1/246. Revisit with a projection only if
  measurements at real volume demand it; do not add it pre-emptively.
