# ADR 0001 — Technology direction

Status: accepted (per build prompt §4, no deviation found).

## Decision

- Backend, ingest, agent, control plane: **Go**.
- Telemetry contract: **OpenTelemetry data model + OTLP** (JSON/HTTP first, gRPC/protobuf later).
- Primary analytical store: **ClickHouse**.
- Profile/evidence blobs: **local filesystem** now, S3 optional later.
- UI: **server-rendered Go HTML templates + CSS + server SVG, zero browser JS**.
- Deployment: static Go binaries + systemd; Kubernetes DaemonSet/Deployment later.
- eBPF: **C bytecode via cilium/ebpf**, isolated from the control plane.

## Rejected alternatives

- Elasticsearch/OpenSearch, Kafka, Redis, separate TSDB, any Node/JS toolchain —
  each adds a stateful dependency or violates the no-JavaScript rule without a
  measured reason.

## Consequences

- One language/toolchain for ops; small static binaries.
- eBPF layer is Linux-only → agent dev/test must happen on a Linux host.
