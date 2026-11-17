# ADR 0004 — Instrumentation approach

Status: accepted.

## Decision

Layered, automatic-first instrumentation (build prompt §5):

- **Layer A** — host/network discovery: process, cgroup, listening ports, TCP/DNS
  connection metadata, K8s enrichment.
- **Layer B** — protocol/framework: normalized HTTP route templates, gRPC method
  names, DB query fingerprints, Redis/queue operations.
- **Layer C** — runtime/library via eBPF uprobes (cilium/ebpf); initial targets
  Go `net/http`, Gin, gRPC, `database/sql`, pgx, Redis, Asynq.
- **Layer D** — optional OTLP SDK ingest for apps that already emit telemetry.

Every captured relationship carries a capability state:
`automatic/complete`, `automatic/partial`, `correlated`, `unsupported`.
Never fabricate a parent span or an error from a nearby event.

## Consequences

- eBPF uprobes are Linux-only → the agent cannot run on the macOS dev host;
  develop against the Jupler Ubuntu host / a Linux VM (see `docs/risk.md`).
- Apps that already emit OTLP work today via the ingest slice.
