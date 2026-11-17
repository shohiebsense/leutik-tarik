# Instrumentation compatibility matrix

Capability states: **complete** / **partial** / **correlated** / **unsupported**.

| Target | Layer | State | Notes |
|--------|-------|-------|-------|
| Go `net/http` server | B + C (eBPF uprobe) | planned | normalized route + method + status + duration |
| Gin handler | C (uprobe) | planned | route templates from Gin router |
| gRPC (client/server) | B | planned | method/service names, status codes |
| `database/sql` | C | planned | operation type, DB, query fingerprint, duration; **no bind values** |
| pgx driver | C | planned | same as above, driver-specific probe |
| Redis/cache client | B | planned | command family, duration, error |
| Asynq / queue workers | B + span links | planned | producer/consumer via trace context where safe |
| OTLP SDK apps | D (ingest) | **working** | accepts OTLP/HTTP JSON traces today |
| Python / other runtimes | — | unsupported | not in initial scope; matrix grows per phase |

## Current proof (slice 1)

The OTLP/HTTP-JSON ingest path is the only **working** row. The eBPF rows are
planned and require a Linux host; they are not claimed as complete.
