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
