# leutik-tarik — plan

Corrected 2026-11-17 after the scaffolding review (`docs/review-2026-11-17.md`).
The earlier version of this file marked Phase 1 done; it was not.

## Where we actually are

| Phase | Requirement | State |
|---|---|---|
| 0 | repo scan, eBPF capability, dependency licences, ADRs, matrix, risk | partial — ADRs/matrix/risk + repo initialised; eBPF capability + dependency licence check still missing |
| 1 | agent discovers Go/Gin service; OTLP ingest; ClickHouse; search; detail page | **~75%** — protobuf/gzip ingest, ClickHouse store, dedup, per-row retention, time-range search, summary rollup, detail page. **Agent 0%** (needs Linux). |
| 2–6 | dependencies/errors, log snapshots, policy, profiling, hardening | not started |

Slice 1 is a real vertical slice. It is not Phase 1.

## Next: ClickHouse store (chosen path — Docker)

Ordered so the schema is written once. Each step leaves build + tests green.

- [x] **S1 repo hygiene** — `git init`, `.gitignore`, `LICENSE`,
      `docker-compose.yml` (ClickHouse pinned), `Makefile` (`dev-db`/`test`/`migrate`),
      CI build+vet+test. Move `pilot-projects.md` into `docs/`.
- [x] **S2 model + contract** — expand `model.Span` (rpc/db/queue/attributes/links/
      host+pod/status message/confidence); add `model.Decision` (policy id+version,
      sampled, redaction state); trace-level outcome rollup (M2); change
      `Store.InsertSpans` → `Insert(ctx, []Record)` (M9); `Query.From/To/Cursor` (M8);
      fix the false ADR/risk claims (B6).
- [x] **S3 ingest conformance** — body cap, gzip, protobuf, redaction before
      store, malformed/drop counting, `demo-sender -proto -gzip`. Verified over
      real HTTP in all four combinations (B1–B3, M3, M4, P7).
- [x] **S4 DDL v2** — `ReplacingMergeTree(ingested_at)`
      + `PARTITION BY toYYYYMMDD(start_time)`
      + `ORDER BY (service_name, http_route, start_time, span_id)`
      + `INDEX idx_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 1`
      (**not** a projection — ADR 0005; measured on the real table: 1/63 granules)
      + per-row `TTL toIntervalSecond(retention_seconds)` (**not** TTL rule sets —
      ADR 0006, measured: a catch-all rule evicted a 25-day-old error early)
      + `non_replicated_deduplication_window=1000`.
      Pending: `schema_migrations` + an ALTER path (see "Found while implementing").
- [x] **S5 `store.ClickHouse`** — HTTP interface (no driver dependency);
      payload-derived `insert_deduplication_token` for retry idempotency;
      `FINAL` on `GetTrace` only, `LIMIT 1 BY` on the list path.
      3 integration tests gated by `LEUTIK_CLICKHOUSE_URL`, all passing against
      ClickHouse 26.10.5.
- [x] **S6 lock the review's numbers as assertions** — 249/249 vs projection
      lookup, duplicate-insert behaviour, conditional TTL acceptance.
- [x] **S8 agent discovery + doctor (1c-1)** — `cmd/leutik-agent`: reads procfs
      for listening services (port/PID/container attribution), and diagnoses
      kernel/BTF/sysctls/capabilities. Verified on a real kernel inside a
      privileged container (found nginx + dockerd, correct container id).
- [x] **S9 migrations with an ALTER path** — `schema_migrations` + checksum
      (re-runs are no-ops, an edited applied migration is refused), `-migrate`
      flag, `make migrate`. Removing the superseded 001 also fixed the bug where
      `CREATE IF NOT EXISTS` left the table at 16 columns instead of 42.
- [ ] **S7 agent (Phase 1c)** — **unblocked**: Docker Desktop's LinuxKit kernel
      (6.10.14) has BTF and a privileged container can attach uprobes (measured).
      Develop inside `--privileged` containers here; Jupler only for the final
      on-host check. See `docs/agent-portability.md`.

## Found while implementing (must not be forgotten)

- **Migrations** now have an ALTER path (S9). The trap that caused it is worth
  remembering: `CREATE TABLE IF NOT EXISTS` keeps an existing table's OLD
  columns, so a changed CREATE silently does nothing. Add a NEW numbered
  migration with `ALTER TABLE ... ADD COLUMN`; never edit an applied one (the
  runner refuses it by checksum).
- **Superseded migrations must be deleted, not annotated.** `001_spans.sql` had
  a "do not apply" header and the runner applied it anyway, recreating the old
  schema. The runner cannot read intent; the file has to go.
- **TTL only fires on merge**, so expired-but-unmerged rows stay readable.
  Queries must filter `start_time`; never treat TTL as correctness (ADR 0006).
- **Dedup requires identical timestamps.** ReplacingMergeTree dedupes on the
  sort key, which includes `start_time`; a genuine retry carries the same span
  timestamps so it collapses (measured), but re-sent data with regenerated
  timestamps will not.

## Deliberately out of this step

Sampling engine, log ring buffers, profiling, K8s, auth/RBAC — they stay in
phases 3–6. No stubs for them (prompt §20: no empty modules).

## Toolchain note

`go.opentelemetry.io/proto/otlp v1.11.1` requires **Go 1.26**, which raises the
module's `go` directive to 1.26.0. A box with older Go will auto-download a
toolchain (`GOTOOLCHAIN=auto`, the default); an offline box needs Go 1.26
installed explicitly. Verify `go version` on Jupler before deploying.

## Measured facts this plan relies on

Run on ClickHouse 26.10.4 (Docker), 2M rows — re-run them in S6.

- `WHERE trace_id=…` on the old sort key: **247/247 granules** (full scan).
  `bloom_filter(0.01) GRANULARITY 1` on `trace_id`: **5/246 granules** for
  **+10.7% storage**; a `by_trace` projection reached the same pruning for
  **+137% storage**, so the projection was rejected (ADR 0005).
- Wire size: the same two-span trace is **509 B in protobuf vs 1779 B in JSON**
  (3.5x) — real traffic will be protobuf.
- `WHERE service_name=… AND start_time>…`: 172/249 granules.
- Duplicate `(trace_id, span_id)` on `MergeTree`: 2 rows. `ReplacingMergeTree`:
  2 rows until merged, 1 with `FINAL`.
- `insert_deduplication_token` dedupes only with
  `non_replicated_deduplication_window` set (measured: off → 2 rows, on → 1).
- Conditional TTL (`TTL ts + 2 DAY DELETE WHERE …`) is **accepted**.
