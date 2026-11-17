# Retention policies

A policy decides, **at ingest**, how long each stored span may live. The answer
is written onto the row (`policy_id`, `policy_version`, `retention_seconds`) and
the storage engine enforces it via `TTL toIntervalSecond(retention_seconds)`.

Why a column and not TTL rules: ClickHouse ORs TTL expressions, so a catch-all
rule silently deletes rows a more generous rule meant to keep — measured, and a
25-day-old `server_error` was evicted early. See ADR 0006.

## File

`config/policy.example.yaml` is the reference. Point at your own with
`LEUTIK_POLICY_PATH`; the loader defaults to `config/policy.yaml` if present.

A malformed policy is **fatal at startup**. Falling back to defaults would
quietly change how long evidence is kept.

## Precedence

1. **Highest `priority`** wins.
2. Ties broken by **most specific match** (number of populated selectors).
3. Remaining ties broken by **file order** — and the loader rejects two rules
   that tie on both priority and specificity, because then file order would
   silently decide retention.

A span that matches no rule gets `defaults.trace`, recorded as `policy_id=default`.

## Selectors

| Selector | Semantics |
|---|---|
| `service`, `environment` | glob, `*` = any run of characters |
| `route` | glob per path segment; `*` = one segment, `**` = any number |
| `http_method`, `outcome`, `operation_type`, `error_type` | case-insensitive list membership |
| `min_duration` | span duration ≥ value (a single latency bucket) |

`{id}` in a route is literal, because stored routes are already normalized to
templates — a rule written as `/v1/payments/{id}` matches exactly that span.

## Preview

`/policies` shows the effective configuration (what the engine actually uses,
not the file text) and, for the last 24 hours, how many operations each rule
would claim plus an estimated bytes/day.

The estimate uses **11 bytes/span**, measured on ClickHouse 26.10 (20.82 MiB for
2M spans). It is an estimate and the page says so.

A rule that never wins while another rule always claims its spans is marked
**"cannot take effect"** with the winning rule named — so a dead rule is visible
rather than silently useless.

## Route format depends on the sender

Routes are stored exactly as the instrumentation reports them. `otelgin` uses
Gin's own form, `/v1/payments/:id`, **not** `/v1/payments/{id}`. A glob with
`**` matches either, but a rule written with `{id}` will never match a Gin
route. Check `/operations` for the real format on your service before writing
rules.

## Not implemented (deliberate)

- **Sampling.** The `sampled` flag is stored, but nothing drops spans yet.
  Dropping without keeping counts would make "how often does this happen?"
  unanswerable, and counts need the rollup tables (Phase 4 remainder). Until
  then every span is kept, and retention is what bounds storage.
- **Per-signal retention** (log snapshots, profiles, evidence index). Those
  signals have no storage yet; the policy accepts only `trace` rather than
  silently ignoring keys it cannot honour.
- **Live reload and edit auditing.** The policy is read at startup; the active
  version is logged and shown on the page. Editing requires a restart, which
  leaves an audit trail in the service log.
