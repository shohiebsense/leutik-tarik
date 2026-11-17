# leutik-tarik — Architecture

## Goal

Self-hosted observability + tracing platform. Start from an operational symptom
(slow endpoint, failed payment, 500, stalled DB, backed-up queue) and follow the
evidence through **one operation's** lifecycle — without asking an agent.

## Component diagram

```
                        ┌─────────────────────────────────────────────┐
                        │  host agent (Go)  [Linux only for eBPF]      │
                        │  process/container discovery                │
                        │  eBPF uprobes (Layer C) + protocol (Layer B) │
                        │  bounded ring buffer + log snapshots         │
                        └───────────────┬─────────────────────────────┘
                                        │ OTLP (HTTP JSON now; gRPC later)
                                        ▼
  apps (OTel SDK) ──── OTLP ────►  ingest (Go) ──validate/normalize/redact──►  store
                                        │                                    │
                                        │                                    ├─ ClickHouse (spans, rollups, errors)
                                        │                                    └─ filesystem (pprof / evidence blobs)
                                        ▼
                              query + UI (Go, server-rendered HTML/CSS/SVG, zero browser JS)
```

## Component inventory

| Component | Tech | Role |
|-----------|------|------|
| host agent | Go + cilium/ebpf (Linux) | auto discovery + instrumentation; bounded ring buffer; log snapshots |
| ingest | Go | OTLP receiver; validation, redaction, normalization, policy evaluation |
| store | ClickHouse + local filesystem | spans, rollups, error fingerprints, evidence index; pprof blobs |
| query/UI | Go `net/http` + `html/template` + server SVG | operations, operation detail, services, incidents, profiles, policies |
| config | YAML + validation | sampling, retention, capture, redaction, limits |

## Data flow

```
OTLP span → model.Span (normalized operation evidence record) → store
        → operation detail page (summary, waterfall, errors, log snippets, profiles)
```

## Current status

- **Done:** memory store + OTLP/HTTP JSON ingest + server-rendered operations
  list + trace-detail waterfall. Tested end-to-end (ingest → list → detail).
- **Not done:** protobuf + gzip ingest, ClickHouse store, the eBPF agent.
- Corrected status and the ordered next steps: `PLAN.md`. Full findings:
  `docs/review-2026-11-17.md`.

## Key constraint

The automatic-instrumentation agent needs eBPF uprobes, which only exist on
Linux. This dev machine is macOS, so the agent must be developed/tested on the
Jupler Ubuntu host (or a Linux VM). The ingest/store/UI slices run on macOS.
