# leutik-tarik

Self-hosted observability, performance, monitoring, and tracing platform.
Start from an operational symptom and follow the evidence through one
operation's lifecycle — no agent required to answer "what happened?".

Working name per the build prompt is **TraceStory**; the name is configurable.

## Status

Slice 1 (OTLP/HTTP JSON ingest → store → server-rendered operations list +
trace detail) is implemented and tested. It is **not** Phase 1: protobuf/gzip
ingest, the ClickHouse store, and the instrumentation agent are still missing.
See `PLAN.md` for the corrected status and `docs/review-2026-11-17.md` for the
full findings list.

## Log snapshots

Logs are not stored as a stream. `/v1/logs` fills a bounded in-memory ring; a
snapshot is written only when a span errors or exceeds 2s. The trace page shows
lines that carried the trace id separately from lines that were merely
time-correlated. See `docs/log-snapshots.md`. Disable with
`LEUTIK_LOG_CAPTURE=off`.

## The verdict needs no setup

`/services` opens with **"N endpoint(s) worth checking"**. It is derived from
ordinary traffic — no upload, no profile, no configuration:

- every hit in the window is counted, and the **median** and **p95** are computed
  across all of them (median, not mean, so one 30s outlier cannot condemn a fast
  endpoint)
- an endpoint is flagged when median > **1s**, or p95 > **2s**, or ≥5% failures
- it needs at least **5 hits** before judging; below that it says
  *"too few hits"* rather than guessing

Thresholds: `LEUTIK_SLOW_MEDIAN` (default `1s`), `LEUTIK_SLOW_P95` (default
`2s`), `LEUTIK_MIN_SAMPLES` (default `5`).

That is the difference between statistics and anecdote: a 9s request seen once
ranks high by total time but is labelled *too few hits*, while a 1.4s endpoint
seen twenty times is flagged.

## What you can see

| Page | Answers |
|---|---|
| `/operations` | what ran, how long, did it fail |
| `/traces/{id}` | **the story of one request** — SVG waterfall with the critical path outlined, span table, log snippets split into direct evidence vs time-correlated |
| `/services` | which service is broken — operations, errors, error rate, p50/p95, routes |
| `/incidents` | how often — failures grouped into fingerprints with trace counts |
| `/profiles` | stored pprof profiles, rendered as an SVG flamegraph |
| `/policies` | effective retention config, 24h preview, dead rules flagged |

Everything is server-rendered: the waterfall and flamegraph are plain SVG, so the
browser runs no JavaScript.

## Profiling

```sh
# from any Go service with net/http/pprof enabled
curl -s "http://SERVICE/debug/pprof/profile?seconds=30" -o cpu.pb.gz
curl -s -X POST --data-binary @cpu.pb.gz \
  "http://<platform>:8080/v1/profiles?service=fare-membership&type=cpu"
```

The blob is stored on disk, its metadata in ClickHouse, and `/profiles/{id}`
renders it. Profiles are a **time window**, not a single request — the page says
so, because claiming per-request attribution from a 30s profile would be a lie.

## Retention policy

```sh
LEUTIK_POLICY_PATH=config/policy.example.yaml go run ./cmd/leutik-tarik
```

Rules set retention per span at ingest (failures longest, high-volume reads
shortest). `/policies` shows the effective config and what each rule would claim
in the last 24h. See `docs/policies.md`.

## Run against ClickHouse (S4/S5 — dedupe, per-row retention, bloom index)

```sh
make db-up                                  # ClickHouse 26.10 on 127.0.0.1:8123
LEUTIK_CLICKHOUSE_URL=http://127.0.0.1:8123 make migrate
LEUTIK_CLICKHOUSE_URL=http://127.0.0.1:8123 go run ./cmd/leutik-tarik
go run ./cmd/demo-sender -proto -gzip       # what a real exporter sends
```

Integration tests skip themselves unless the endpoint is set:

```sh
LEUTIK_CLICKHOUSE_URL=http://127.0.0.1:8123 go test ./internal/store/ -v
```

## Run with no dependencies (memory store)

```sh
# Go 1.26+ is required (the OTLP proto module requires it).
go run ./cmd/leutik-tarik            # listens on 127.0.0.1:8080

# send a synthetic trace (fixture, clearly labeled) in any of four modes:
go run ./cmd/demo-sender                    # JSON, plain
go run ./cmd/demo-sender -gzip              # JSON, gzip
go run ./cmd/demo-sender -proto             # protobuf (OTLP default)
go run ./cmd/demo-sender -proto -gzip       # protobuf, gzip

open http://127.0.0.1:8080/operations
```

Ingest is OTLP/HTTP conformant for the trace signal: protobuf and JSON, plain
or gzip, with a 64 MiB decompressed cap and credentials redacted before storage.
Metrics and logs endpoints are not implemented yet.

## Test

```sh
go test ./...
```

## Build prompt

`observability-platform-build-prompt.md` is the implementation brief.
Architecture, ADRs, matrix, and risks live in `docs/`.
