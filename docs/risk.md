# Risk list (Phase 0)

| # | Risk | Impact | Mitigation |
|---|------|--------|------------|
| 1 | **Dev host is macOS** — no `/sys/kernel/btf`, no eBPF uprobes | Automatic-instrumentation agent cannot be run/tested locally | Develop/test agent on the Jupler Ubuntu host or a Linux VM; keep ingest/store/UI macOS-testable |
| 2 | Jupler kernel version/eBPF capability unknown yet | uprobes/CO-RE may need a specific kernel | Verify `uname -r` + BTF presence on Jupler before writing uprobe code (Phase 1 gate) |
| 3 | ClickHouse not installed locally | Can't integration-test the ClickHouse store without Docker | Run ClickHouse via Docker for the store slice; keep `Memory` store for green unit tests |
| 4 | No systemd on macOS | Can't test systemd units here | Write units; validate on Jupler |
| 5 | Scope is large (6 phases) | Risk of many empty modules | Vertical slices only; each phase ends buildable + tested |
| 6 | Ingest accepts JSON only; no protobuf, no gzip, no body cap | A stock OTel exporter cannot send to us | Blocking. See `docs/review-2026-11-17.md` B1–B3 |
