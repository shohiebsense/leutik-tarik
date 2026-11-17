# Risk list (Phase 0)

| # | Risk | Impact | Mitigation |
|---|------|--------|------------|
| 1 | **Dev host is macOS** — the host OS itself has no BTF/eBPF | Old assumption: agent cannot be tested locally | **Resolved by measurement**: Docker Desktop's LinuxKit kernel 6.10.14 has `/sys/kernel/btf/vmlinux` (6.6 MB) and a privileged container exposes `uprobe_events`/`kprobe_events`. Develop the agent in `--privileged` containers here. |
| 2 | Host kernel/BTF on deployment targets | A container cannot supply a kernel | Portability ladder in `docs/agent-portability.md`: L1 privileged DaemonSet, L2 embedded BTFHub blobs, L0 OTLP SDK where probes are impossible. `leutik agent doctor` self-diagnoses. |
| 3 | ClickHouse not installed locally | Can't integration-test the ClickHouse store without Docker | Run ClickHouse via Docker for the store slice; keep `Memory` store for green unit tests |
| 4 | No systemd on macOS | Can't test systemd units here | Write units; validate on Jupler |
| 5 | Scope is large (6 phases) | Risk of many empty modules | Vertical slices only; each phase ends buildable + tested |
| 6 | Ingest accepts JSON only; no protobuf, no gzip, no body cap | A stock OTel exporter cannot send to us | Blocking. See `docs/review-2026-11-17.md` B1–B3 |
