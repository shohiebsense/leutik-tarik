# ADR 0003 — Data model and store

Status: accepted.

## Decision

- Canonical execution model is the **OpenTelemetry span**, normalized into a
  platform `model.Span` (the operation evidence record). No competing format.
- One `store.Store` interface (insert spans, list operations, fetch trace) with
  two implementations: `Memory` (tests/demos) and `ClickHouse` (production).
- ClickHouse holds spans, rollups, error fingerprints, evidence index; pprof and
  large blobs live on the filesystem with metadata references in ClickHouse.
- An operation = the **root span** of a trace (ParentSpanID empty). Operation
  identity is searchable by service, route, method, outcome, time — never
  requiring a trace ID up front.

## Consequences

- Trace/span IDs are hex strings. This is not a deviation: OTLP JSON uses hex
  for `traceId`/`spanId` in all encodings (base64 applies to other bytes
  fields). Verified against the OTLP specification; no conformance work is
  owed here.
- The real transport gaps are protobuf and gzip, tracked in
  `docs/review-2026-11-17.md` (B1, B2).
- High-cardinality values stay on span/evidence records, never in metric labels.
