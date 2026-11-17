# Wiring fare-membership to leutik-tarik (OTel SDK path)

Goal: complete spans — **method, route, DB, Redis, status, duration** — from the
real fare-membership backend, without waiting for the uprobe offset work (S15).

The platform already accepts standard OTLP, so this is the shortest path to
evidence that is *complete* rather than partial.

Target verified on the actual repo (2026-11-18): Gin **1.10.0**, pgx **v5.7.4**,
go-redis **v9.7.0**, 158 routes, entry `cmd/server/main.go`, router built by
`setupRouter()` with `gin.New()` + custom middlewares. **No OpenTelemetry
dependency exists yet.**

## 1. Dependencies (versions verified to resolve)

Pinned so the repo keeps `go 1.25` — the latest releases do **not**:

| Module | Pin | Why this version |
|---|---|---|
| `go.opentelemetry.io/otel` (+ `sdk`, `otlptracehttp`) | **v1.44.0** | v1.47.0 requires Go **1.26**, which would break the repo's `go 1.25` pin and the Go 1.22 CI bootstrap |
| `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin` | **v0.60.0** | v0.65 wants gin 1.11, v0.72 wants gin 1.12; v0.60 keeps **gin 1.10.0** |
| `github.com/exaring/otelpgx` | v0.13.0 | requires only Go 1.25 |
| `github.com/redis/go-redis/extra/redisotel/v9` | **v9.7.0** | matches the repo's `go-redis` v9.7.0; v9.23 requires otel v1.47 (→ Go 1.26) |

**Three separate version traps, each of which silently upgrades something:**

1. `otel` ≥ v1.45 raises the `go` directive to 1.26.
2. `otelgin` is versioned independently (v0.x) and drags Gin forward — v0.72
   upgraded Gin from 1.10.0 to 1.12.0 without being asked.
3. `redisotel` is versioned with `go-redis`, so pin it to the same v9.x as the
   repo rather than "latest".

`go get` applies all three silently. Check `go.mod` after any `go get` here.

## 2. Tracer provider (`cmd/server/main.go`, before `setupRouter`)

```go
func initTracing(ctx context.Context, cfg *config.Config) (func(context.Context) error, error) {
    if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
        return func(context.Context) error { return nil }, nil // tracing opt-in
    }
    exp, err := otlptracehttp.New(ctx) // reads OTEL_EXPORTER_OTLP_* env vars
    if err != nil {
        return nil, err
    }
    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName("fare-membership"),
            semconv.ServiceVersion(cfg.AppVersion),      // whatever the config exposes
            semconv.DeploymentEnvironmentName(cfg.Env),  // dev/staging/prod
        ),
    )
    if err != nil {
        return nil, err
    }
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),            // batching keeps the request path free
        sdktrace.WithResource(res),
        sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rateFromEnv()))),
    )
    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{}, propagation.Baggage{},
    ))
    return tp.Shutdown, nil
}
```

Call it in `main` **before** the router is built, and call the returned shutdown
with a 5s timeout on the existing shutdown path so buffered spans are not lost.

## 3. Gin middleware — order matters

```go
r := gin.New()
r.Use(otelgin.Middleware("fare-membership")) // FIRST: so everything below is a child
r.Use(middleware.RequestLoggerWithSkip("/healthcheck"))
r.Use(gin.Recovery())
```

- `otelgin` **before** the request logger: the span must exist before anything
  else records into it; otherwise the log line cannot carry the trace id.
- `gin.Recovery()` **after** `otelgin`, so a panic still closes the span with an
  error status instead of leaving it unfinished.

## 4. Postgres (pgx v5)

```go
cfg, err := pgxpool.ParseConfig(dbURL)
cfg.ConnConfig.Tracer = otelpgx.NewTracer(
    // keep span names as the operation, not the whole statement
    otelpgx.WithTrimSQLInSpanName(),
)
pool, err := pgxpool.NewWithConfig(ctx, cfg)
```

## 5. Redis (go-redis v9)

```go
if err := redisotel.InstrumentTracing(rdb); err != nil { /* log, don't fail */ }
```

## 6. Environment (backend side)

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://<leutik-host>:8080   # SDK appends /v1/traces
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf              # the platform's default; verified
OTEL_EXPORTER_OTLP_COMPRESSION=gzip                    # verified end to end
OTEL_SERVICE_NAME=fare-membership
OTEL_RESOURCE_ATTRIBUTES=deployment.environment=dev
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=0.1                            # see §8 before choosing 1.0
```

Platform side: `leutik-tarik` binds `127.0.0.1:8080` by default (no auth yet), so
either co-locate it or set `LEUTIK_ADDR`.

## 7. What arrives — and the two things that will surprise you

**Route format is Gin's, not the platform's braces.** `otelgin` sets
`http.route` from `c.FullPath()`, so a route is stored as
`/v1/payments/:id`, **not** `/v1/payments/{id}`. Consequences:

- Policy globs with `**` work (`/v1/payments/**` matches).
- Policy rules written against `{id}` will **never match**.
- Dashboards grouping by route must expect `:param`.

**Full SQL is sent by default.** `otelpgx` emits `db.query.text` with the whole
statement. That is how member phone numbers and transaction ids would end up in
the evidence store. The platform now drops `db.query.text`, `db.statement`,
`db.query.parameter.*` and request/response bodies at ingest
(`internal/ingest/normalize.go`, test `TestSQLTextIsNeverStored`), keeping
`db.system`, `db.operation.name` and `db.query.summary`.

**Do not** treat the SDK as the place to enforce this: an app-side mistake or a
different SDK version could reintroduce it. Redacting at ingest is the single
guarantee. If you also want defence in depth, add a `SpanProcessor` in the app
that deletes the attribute, but the platform is what must hold.

## 8. Sampling and volume

The platform does **not** sample yet (documented in `docs/policies.md`): every
span sent is stored, bounded only by the retention policy. At 158 routes,
`fare-membership` traffic `POST /v1/payments/*` at 10% sampling is a sane start;
set it with `OTEL_TRACES_SAMPLER_ARG`.

Retention then applies per span through the policy engine. With the shipped
example policy, a payment write error is kept 90 days and a successful GET 2
days — so sampling is what protects storage, not retention.

Rough sizing: a measured span compresses to ~11 bytes on disk
(20.82 MiB / 2M spans, ClickHouse 26.10). 1M spans/day ≈ 11 MB/day before
retention; a payment-heavy endpoint at 10% sampling will sit far below that.

## 9. Verification (do this, not "it compiles")

1. Start the platform with ClickHouse and a policy; start the backend with
   `OTEL_EXPORTER_OTLP_ENDPOINT` pointing at it.
2. Hit a real endpoint, e.g. `POST /v1/period-pass/orders`.
3. On `/operations` you must see:
   - `service_name = fare-membership`
   - `http_route = /v1/period-pass/orders` (Gin form)
   - `completeness = complete` (SDK spans carry real parentage)
   - a child `db.query …` span with `db.system=postgresql`
   - a child Redis span for the cache/deny-list path
4. Open the trace: the waterfall must nest HTTP → DB/Redis, and the summary
   must show the worst outcome across spans — a 200 root with a failed child
   reads as an error, which is the whole point.
5. Check that `db.query.text` is `[REDACTED]` in the stored attributes:
   `SELECT attributes FROM spans WHERE db_system != '' LIMIT 1`.

## 10. Risks, stated plainly

| Risk | Why it matters |
|---|---|
| `go.mod` pins `go 1.25` + `toolchain go1.25.0` | The OTel deps do not raise this, but any dependency bump must be built on the CI runner and this box, not just locally. |
| Raw SQL text in spans | Mitigated at ingest; do not remove that blocklist. |
| 158 routes × unthrottled spans | Sampling is the only volume control until the platform implements it. |
| Platform has no auth | Do not expose port 8080 beyond a trusted network. |
| `otelgin` v0.x | Contrib modules move independently; pin versions in `go.mod`. |

## 11. What this buys over the uprobe path

| | SDK (this guide) | uprobes (S14) |
|---|---|---|
| method + route | **yes** | no (offset work pending) |
| DB/Redis children | **yes** | no |
| app change | dependency + ~40 lines | none |
| completeness | `complete` | `partial` |

The uprobe path stays valuable for services that *cannot* be changed (third
party, legacy, rollback-averse). They are complementary, not competing.
