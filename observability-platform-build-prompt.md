# Build Prompt: A Lean, Evidence-Driven Observability, Tracing, and Profiling Platform

> **Use this as the primary implementation brief for the engineering agent.** Build an original, production-minded observability platform inspired by the strongest workflows in [Kibana](https://github.com/elastic/kibana) and [Jaeger](https://github.com/jaegertracing/jaeger), while optimizing for low resource consumption, low storage cost, automatic instrumentation, operational clarity, and fast root-cause analysis.
>
> Working name: **TraceStory**. Treat the name as configurable; do not spend time on branding.

---

## 1. Mission

Build a self-hosted platform that lets an engineer start from an operational symptom—an endpoint is slow, a payment failed, a queue is backed up, a service returned 500, or a database operation stalled—and follow the evidence through the complete request or transaction lifecycle.

The platform must combine:

- Distributed tracing and transaction-level operation timelines.
- Service, endpoint, and dependency performance monitoring.
- Error and exception visibility.
- Lightweight log evidence snapshots, not indiscriminate permanent log collection.
- Continuous and incident-triggered profiling.
- A clear, explainable process-flow view for operational investigation and root-cause analysis (RCA).
- Simple, explicit configuration for sampling, retention, capture rules, exclusions, and resource limits.

The product should answer, with evidence:

1. **What happened?**
2. **Where did it happen?**
3. **Which operation and dependency were involved?**
4. **What happened immediately before and after the failure or slowdown?**
5. **Which service, endpoint, database, queue, process, or deployment is implicated?**
6. **What evidence supports the suspected cause, and what remains unknown?**
7. **What is the operational impact and how frequently is it happening?**

This is not a generic metrics dashboard. The central product object is an **operation / transaction evidence story**. Metrics, traces, logs, profiles, service topology, and deployment metadata should converge around that object.

## 2. Product principles

Treat these as non-negotiable design constraints:

1. **Automatic first.** Engineers should not need to manually create spans, add trace IDs to every log statement, or annotate every function. Manual instrumentation may exist as an optional escape hatch, never as the default integration requirement.
2. **Evidence over decoration.** Every conclusion shown in the UI must link to the underlying span, event, log excerpt, metric interval, profile, or deployment record that supports it.
3. **Tell the operational story.** Present a request as a sequence of understandable events and dependencies, not as a wall of telemetry fields.
4. **Collect less, explain more.** Prefer summaries, fingerprints, bounded buffers, adaptive sampling, and event-triggered evidence capture over retaining every raw event forever.
5. **Low overhead by design.** Bound memory, queues, buffers, cardinality, payload size, and concurrent work. Instrumentation must not become an incident of its own.
6. **Configuration should be direct.** Use explicit rules with predictable precedence, previewable impact, validation, and safe defaults. Avoid hidden heuristics that operators cannot inspect.
7. **Useful without Kubernetes.** Support a straightforward single-host Ubuntu deployment first. Kubernetes support should be an additional deployment mode, not a prerequisite.
8. **No JavaScript or Node.js.** Do not introduce JavaScript, TypeScript, Node.js, npm, pnpm, yarn, Bun, Vite, React, Vue, Angular, or browser-side JavaScript. This applies to application code, UI, build tooling, scripts, and runtime dependencies.
9. **Do not pretend correlation is causation.** Distinguish directly observed relationships from time-based or heuristic correlations. Display confidence and missing telemetry explicitly.
10. **Prefer boring, mature technology.** Choose proven components with active maintenance, clear licenses, predictable operations, and measurable resource costs. Avoid novelty for its own sake.

## 3. Reference projects and what to learn from them

Study the following projects and their current documentation before implementation. Reuse compatible standards and mature components where sensible; do not blindly copy their architecture or reproduce their entire scope.

- **Kibana** — information discovery, filtering, saved views, dashboards, navigation, and investigation workflows: <https://github.com/elastic/kibana>
- **Jaeger** — trace model, span relationships, service dependency views, trace search, and trace-detail investigation: <https://github.com/jaegertracing/jaeger>
- **OpenTelemetry** — common telemetry data model, semantic conventions, OTLP ingestion, context propagation, and collector processing: <https://opentelemetry.io/>
- **OpenTelemetry eBPF Instrumentation / OBI** — zero-code network and application observability possibilities: <https://opentelemetry.io/docs/zero-code/obi/>
- **OpenTelemetry Go automatic instrumentation** — feasibility and current coverage for Go runtime/library instrumentation: <https://github.com/open-telemetry/opentelemetry-go-instrumentation>
- **Grafana Alloy / OpenTelemetry Collector** — collector pipelines, service discovery, batching, filtering, memory limiting, and signal processing: <https://grafana.com/docs/alloy/latest/>
- **Pyroscope** — continuous profiling concepts and profile types: <https://grafana.com/docs/pyroscope/latest/>

Before selecting a dependency, check its current release activity, supported OS/kernel/runtime versions, maintenance status, license, and known limitations. Record important decisions in short Architecture Decision Records (ADRs).

## 4. Recommended technology direction

Use this as the default architecture unless repository inspection or a measured technical constraint gives a strong reason to change it. Explain every deviation in an ADR.

| Area | Preferred technology | Reason |
|---|---|---|
| Main backend and query API | **Go** | Mature networking/concurrency ecosystem, efficient deployment, strong fit for OTLP and infrastructure services |
| Host agent and control utilities | **Go** | One language/toolchain for operations and easier deployment; keep the agent small and bounded |
| eBPF programs | **C eBPF bytecode**, managed by Go using **cilium/ebpf** | Mature Linux eBPF workflow and Go integration; isolate kernel-specific code from the control plane |
| Telemetry contract | **OpenTelemetry data model + OTLP** | Interoperability, semantic conventions, and compatibility with existing instrumentation |
| Ingestion and processing | Go-based OTLP receiver / OpenTelemetry Collector components where useful | Reuse mature batching, filtering, redaction, memory limiting, and tail-sampling behavior rather than rebuilding them without reason |
| Primary analytical store | **ClickHouse** | Columnar compression and efficient analytical queries for spans, compact log events, metric samples, and evidence indexes |
| Profile and large evidence blobs | Local filesystem initially; optional **S3-compatible object storage** for production/scale | Keep bulky profile payloads out of hot analytical tables; make object storage optional for a small installation |
| UI | **Go server-rendered HTML templates + CSS + server-generated SVG** | No browser JavaScript, no Node toolchain, low operational complexity, directly deployable as a Go service |
| Deployment | Static Go binaries, systemd units, and Kubernetes DaemonSet/Deployment manifests | Works on a plain Ubuntu VM and can expand to Kubernetes |
| Configuration | YAML with schema validation and a read-only effective-config view | Explicit, reviewable, versionable operational behavior |

### Storage strategy

Start with **ClickHouse plus filesystem storage**. Add S3-compatible object storage only when profile size, retention, replication, or multi-node operation justifies it. Do not introduce Elasticsearch/OpenSearch, Kafka, Redis, or a separate time-series database by default. Each additional stateful dependency must have a documented benefit and a measured reason to exist.

Use ClickHouse for:

- Normalized spans and trace indexes.
- Bounded structured log events and log-snapshot indexes.
- RED and service-level metric samples/rollups.
- Error fingerprints, dependency summaries, and evidence metadata.
- Retention and sampling decision metadata.

Store large pprof/profile artifacts and optional evidence bundles as compressed objects, with searchable metadata and object references in ClickHouse. Evaluate whether metrics volume justifies a dedicated metrics store later; do not add one pre-emptively.

### Frontend constraint

The browser must execute **zero JavaScript**. Use normal HTML forms, links, query parameters, CSS, native `<details>`/`<summary>`, and server-rendered pages. Render charts, timelines, service maps, waterfalls, and flamegraphs as SVG generated on the server. Interactions may submit a GET/POST request and render a new page. No SPA, no hydration, no client-side charting package, and no JavaScript fallback.

If a requested visual interaction cannot be delivered under this constraint, implement the clearest server-rendered alternative and document the trade-off rather than quietly adding JavaScript.

## 5. Automatic instrumentation: the core technical challenge

The default onboarding experience should be:

1. Install the host agent or Kubernetes DaemonSet.
2. Discover processes, containers, services, and listening ports automatically.
3. Detect supported runtimes, frameworks, protocols, and dependencies.
4. Begin collecting useful service-level telemetry without modifying application source code.
5. Show exactly which instrumentation capabilities are active, partial, unsupported, or blocked.

### 5.1 Layered instrumentation model

Use a layered design rather than claiming that one technique can observe everything:

**Layer A — host and network discovery**

- Process discovery: PID, executable, command line with secret-safe handling, user, container/pod, namespace, cgroup, host, and lifecycle.
- TCP connection and socket-level observations.
- DNS and connection metadata where available.
- HTTP request/response metadata for supported clear-text and runtime-aware paths.
- Kubernetes metadata enrichment when running in Kubernetes.
- Service relationship discovery from observed connections.

**Layer B — protocol and framework instrumentation**

- Automatically identify HTTP methods and normalized route templates, not only raw URL paths.
- Capture status code, duration, request/response size where safe, protocol, peer service, and error state.
- Support gRPC method/service names and status codes where possible.
- Observe supported database client calls and record operation type, database, query fingerprint, duration, and error; do not store raw parameter values by default.
- Observe supported Redis/cache operations and queue publish/consume/retry behavior.
- Capture framework handler boundaries and supported runtime errors/panics through maintained probes or adapters.

**Layer C — runtime/library instrumentation**

- Use supported eBPF uprobes/runtime probes or maintained zero-code instrumentation for deeper spans.
- Prioritize Go services and the frameworks/libraries actually present in the target environment.
- Initial Go integration targets should include `net/http`, Gin, gRPC, `database/sql`, common PostgreSQL drivers (including pgx where supported), Redis clients, and Asynq or equivalent queue workers.
- Add further runtimes only after establishing a compatibility matrix and repeatable tests.

**Layer D — optional SDK and adapter fallback**

- Accept standard OTLP from applications that already emit OpenTelemetry.
- Provide small optional adapters for gaps that cannot be safely observed through eBPF or protocol inspection.
- Do not require developers to add manual spans in the normal case.
- If an asynchronous boundary cannot propagate trace context automatically, supply a narrowly scoped integration that injects/extracts W3C trace context in the message metadata. Explain exactly where this is necessary.

### 5.2 Be precise about what “automatic” means

Do not promise universal zero-code capture. An eBPF agent can observe many network and supported runtime events, but it cannot reliably infer every arbitrary function call, application exception, business operation, or async parent-child relationship across every language and framework.

Implement an instrumentation capability matrix with these states:

- **Automatic / complete** — direct trace/span relationship is captured.
- **Automatic / partial** — useful metadata is captured, but some internal boundaries or attributes are missing.
- **Correlated** — related by request ID, process, endpoint, or time window; not a proven parent-child relationship.
- **Unsupported** — explicitly not instrumented.

Never manufacture a parent span or claim an error was thrown by a function when only a nearby log line or network event was observed. Preserve the original telemetry and attach correlation method/confidence metadata.

### 5.3 Trace propagation and span linking

- Support W3C Trace Context (`traceparent`, `tracestate`) and OpenTelemetry semantic conventions.
- Maintain correct parent-child relationships for supported synchronous calls.
- Propagate context across supported HTTP/gRPC boundaries.
- For queues and async jobs, preserve producer/consumer linkage where instrumentation permits. If parent-child semantics are not correct for the operation, use span links rather than fabricating a direct parent.
- Link database/cache/remote-call spans to the active request span when directly observed.
- Preserve existing trace IDs and span IDs from applications that already use OpenTelemetry.
- Detect missing context, duplicate spans, clock skew, orphan spans, and partial traces. Show these conditions in the UI.

### 5.4 Endpoint and operation normalization

Normalize dynamic paths into route templates, for example:

- `/v1/users/83921` → `/v1/users/{id}`
- `/payment/transaction/ab12cd34` → `/payment/transaction/{transaction_id}`

Do not group everything under a raw URL or use a unique identifier as an endpoint. Support route templates exposed by frameworks first, then conservative normalization rules. Preserve the original path only when explicitly enabled and safely redacted.

Each operation should have a stable, searchable identity with fields such as:

- `service.name`, `service.version`, `deployment.environment`
- `operation.name`, `operation.kind`, `operation.type`
- `http.request.method`, `http.route`, `http.response.status_code`
- `rpc.system`, `rpc.service`, `rpc.method`, `rpc.grpc.status_code`
- `db.system`, `db.operation.name`, `db.query.summary` or normalized fingerprint
- queue system/name, job type, delivery/retry state
- trace ID, span ID, parent span ID, span links
- start/end timestamps, duration, outcome, error type
- host, process, container, pod, namespace, and peer service

Keep high-cardinality values out of metric labels. Store identifiers on trace/event records where necessary, with privacy and indexing controls.

## 6. Operation-centric data model

Use OpenTelemetry-compatible spans as the canonical execution model, enriched with a platform-specific **operation evidence record**. Do not invent a competing trace format.

An operation evidence record should connect:

- Root request/transaction identity.
- Child spans and linked asynchronous work.
- Service-to-service transitions.
- Database/cache/queue calls.
- Error and timeout events.
- Relevant log snippets.
- Service metrics around the event.
- Profile samples for the relevant process/time interval.
- Deployment/release metadata when available.
- Instrumentation completeness and correlation confidence.

Support both a trace ID and useful human-readable search keys such as service, normalized endpoint, HTTP method, operation type, error fingerprint, request ID, deployment version, and time range. Never require a user to know a trace ID before they can investigate an incident.

## 7. Automatic error capture and classification

Detect and classify errors without requiring application teams to add custom error spans.

Capture where supported:

- HTTP 4xx/5xx responses, with 5xx distinguished from expected client errors.
- gRPC error statuses.
- Runtime panics/exceptions and recovered panics where runtime/framework probes can observe them.
- Database errors, connection failures, query timeouts, pool exhaustion, and transaction failures.
- Cache failures/timeouts.
- Queue delivery errors, retries, retry exhaustion, and dead-letter transitions.
- Request timeouts, cancellations, context deadline exceeded, and connection resets.
- Process restarts, crashes, OOM kills, and container restarts when host/orchestrator data is available.

Requirements:

- Normalize errors into stable fingerprints while preserving the original safe message/stack excerpt when permitted.
- Deduplicate repeated errors and group them by service, endpoint/operation, error class, and fingerprint.
- Link an error to the active span when directly observed; otherwise show it as a correlated event with confidence metadata.
- Capture first occurrence, latest occurrence, frequency, affected operations, and temporal trend.
- Redact credentials, tokens, cookies, secrets, payment data, and configured PII before persistence.
- Never assume every HTTP 4xx is an application failure. Make classification configurable.

## 8. Event-triggered log snapshots — not full log retention

This is a key differentiator. Do not build a conventional “store every log forever” product.

### 8.1 Bounded local capture

Implement a bounded in-memory ring buffer per service/process (or per agent with fair per-process limits) for recent structured log events and selected diagnostic events.

Configuration must include:

- Maximum memory bytes per process and per host.
- Maximum event count and maximum individual event size.
- Time window to retain in memory before overwrite.
- Allowed log sources: stdout/stderr, journald, configured files, supported logger integrations.
- Include/exclude rules by service, process, namespace, and log level.
- Redaction rules applied before data leaves the host.
- Rate limits and drop counters.

Do not retain unbounded raw log buffers. When limits are reached, overwrite/drop according to a documented policy and expose drop counts.

### 8.2 Snapshot triggers

Create a durable log snapshot only when a configurable event trigger occurs, such as:

- A request returns a configured failure status.
- A supported runtime panic/exception is detected.
- A database/cache operation fails or exceeds a threshold.
- An endpoint exceeds its latency threshold.
- A queue job fails, retries excessively, or enters a dead-letter state.
- A service/process health rule fires.
- An operator manually marks an operation for investigation.

On trigger, flush a bounded time window before and after the event (for example, a configurable number of seconds on each side). Do not recollect or persist the entire service log stream.

### 8.3 Two log views

Provide two complementary views:

1. **Log snippet view** — a compact, timestamped excerpt around the incident, with source, severity, process, trace/span IDs when available, and a clear indication of whether the link is direct or heuristic.
2. **Process-flow view** — a chronological sequence of relevant events across the request and involved processes/services. Show gaps and missing context rather than hiding them.

Deduplicate repeated lines/events, cap snapshot size, compress persisted snippets, and keep the searchable index small. Preserve provenance and a content hash for each evidence item. Exact trace-linked logs must be visually distinguishable from time-correlated candidates.

## 9. Performance monitoring and profiling

### 9.1 RED and service health

Automatically derive and display:

- Request rate / throughput.
- Error rate and error count.
- Duration percentiles (p50, p90, p95, p99 where sample volume supports them).
- Saturation indicators: CPU, memory, goroutines/threads, file descriptors, network, connection pools, queue depth, and database pool pressure where available.
- Dependency latency and failure rates.
- Endpoint-level comparisons over time.

Make aggregation windows, cardinality controls, and metric retention configurable. Explain low sample counts and avoid misleading percentile charts.

### 9.2 Continuous profiling

Start with low-overhead CPU profiling using eBPF where supported. Add other profile types only when the target runtime provides a safe, reliable mechanism:

- CPU flamegraph/profile.
- Go heap/allocations and goroutine profiles where safely obtainable and explicitly enabled.
- Lock/block profiles where available and the overhead is acceptable.
- Process-level memory and runtime statistics.

Use sampling, not per-function event collection. Capture profiles at service/process/time-window level. Do not imply that a time-window profile proves the cost of one individual request unless the profiler can genuinely attribute it.

### 9.3 Incident-triggered profile snapshots

Allow rules such as:

- CPU above threshold for a sustained duration.
- Memory growth or RSS threshold.
- Endpoint latency regression.
- Error rate spike.
- Operator-triggered capture.

When triggered, keep a bounded profile window and link it to the incident/operation evidence record. Avoid triggering one full profile for every failed request. Apply cooldown, per-process limits, and capture budgets.

### 9.4 Flamegraphs

Render flamegraphs as server-generated SVG. Support:

- Hot path identification.
- Function/package grouping.
- Before/after comparison.
- Profile time range and process identity.
- Download of the original pprof artifact where authorized.

## 10. Configurable retention and sampling policy engine

Retention must be automatic, deterministic, and based on operation characteristics—especially **method type and normalized relative endpoint path**.

Support selectors for:

- Service name / service group.
- Environment and deployment.
- HTTP method (`GET`, `POST`, `PUT`, `PATCH`, `DELETE`, etc.).
- Normalized route path (exact, prefix, or safe pattern).
- Semantic operation kind/type (read, write, payment, settlement, authentication, calculation, background job, etc.).
- gRPC service/method.
- Database operation type.
- Outcome (success, client error, server error, timeout, exception, cancelled).
- Latency bucket or threshold.
- Error fingerprint/severity.
- Explicit business-critical tag when provided by configuration or trusted metadata.

### 10.1 Example policy

The following is illustrative only. Make all values configurable and do not hard-code this policy as the only behavior.

```yaml
retention:
  defaults:
    traces_success: 3d
    traces_error: 30d
    log_snapshots: 14d
    metric_raw: 7d
    metric_rollups: 90d
    profiles: 14d
    evidence_index: 90d

  rules:
    - name: payment-write-errors
      priority: 100
      match:
        service: "payment*"
        http_method: [POST, PUT, PATCH]
        route: "/v1/payments/**"
        outcome: [error, timeout]
      keep:
        trace: 90d
        log_snapshot: 30d
        profile: 30d
        evidence_index: 180d

    - name: read-endpoints-success
      priority: 20
      match:
        http_method: [GET]
        outcome: [success]
      keep:
        trace: 2d
        log_snapshot: 0s
        profile: 3d
        evidence_index: 30d
```

### 10.2 Policy behavior

- Define and document rule precedence. Use explicit priority, then specificity, then default policy.
- Evaluate policy at ingestion/decision time and persist the selected policy ID/version with the record.
- Support separate retention for traces, error events, log snapshots, metric raw samples, metric rollups, profiles, and evidence indexes.
- Support separate head/tail sampling rules. Preserve errors, slow traces, rare failures, and explicitly important operation classes at higher rates.
- Avoid retaining every successful trace at full fidelity. Preserve aggregate counts and latency distributions after raw trace expiry.
- Provide a **policy preview**: show which recent operations would match each rule and estimate daily storage impact before activation.
- Provide dry-run mode, validation, overlap/conflict warnings, and an audit trail for policy changes.
- TTL deletion must be automatic, observable, and tested. Expired data must not remain queryable through stale indexes or object references.

## 11. Ingestion pipeline and resource protection

Build a pipeline with clear boundaries:

1. Agent discovery and capture.
2. Bounded local queue / optional disk write-ahead buffer.
3. OTLP or internal transport to the ingest service.
4. Validation, resource enrichment, redaction, normalization, and deduplication.
5. Sampling and retention-policy evaluation.
6. Batching and compressed writes to ClickHouse.
7. Profile/evidence-object persistence.
8. Indexing and query availability.

Requirements:

- Memory limiter must run before processors that can accumulate significant state.
- Batch after filtering/redaction/enrichment where appropriate.
- Set hard bounds for queue size, batch size, in-flight requests, retries, and per-tenant/service buffers.
- Use backpressure and explicit drop behavior. Never allow an outage in the observability platform to exhaust application memory or disk.
- Use retry with exponential backoff and jitter, but cap retry queues and total retry age.
- Use a disk-backed spool only when configured, with a strict size quota and safe cleanup.
- Report dropped, sampled, delayed, redacted, and failed telemetry counts.
- Make the agent fail open for application traffic: telemetry failure must not block the observed application.
- Do not put the agent on the application's critical request path.
- Support graceful shutdown and bounded flush time.

## 12. ClickHouse data and storage design

Design tables for analytical access patterns, not ORM convenience.

- Prefer typed columns for frequently filtered fields.
- Use `LowCardinality` for suitable repeated categorical dimensions.
- Use compact numeric types where ranges are known.
- Use UUID/fixed-width representations where appropriate.
- Use ZSTD compression and benchmark codecs by column type.
- Use MergeTree-family engines selected for the deployment mode; use replicated variants only when replication is actually configured.
- Partition by a sensible time interval; do not create one partition per service, endpoint, trace, or tenant.
- Choose `ORDER BY` keys from real search patterns: time, service, normalized route, trace ID, outcome, and operation identity.
- Keep arbitrary/high-cardinality attributes in a controlled secondary structure; do not index every attribute.
- Use materialized views/aggregate tables for service and endpoint rollups.
- Apply TTL policies to raw spans, logs, metric samples, and indexes independently.
- Avoid unbounded `Map`/JSON attribute growth and large raw payload columns.
- Keep large pprof files outside ClickHouse, with integrity hashes and metadata references.
- Benchmark insert throughput, query latency, merge pressure, disk amplification, compression ratio, and TTL cleanup behavior with realistic data.

Provide documented DDL and migration scripts. Explain backup, restore, disk sizing, and safe schema evolution.

## 13. RCA and evidence-first user experience

Design the UI around a single operational investigation, with clear paths from overview to evidence.

### 13.1 Main navigation

Keep it small and task-oriented:

- **Operations** — recent and searchable transactions/requests/jobs.
- **Services** — health, traffic, dependencies, and endpoint performance.
- **Incidents** — grouped errors, latency regressions, and triggered evidence snapshots.
- **Profiles** — CPU and runtime profiling.
- **Policies** — sampling, retention, redaction, and capture rules.
- **System** — agent status, ingestion health, storage usage, and instrumentation coverage.

### 13.2 Operation detail: the primary screen

A single operation detail page should tell the story from start to finish:

1. **Summary:** operation name, service, method, normalized path, outcome, total duration, timestamp, trace ID, and deployment version.
2. **Narrative:** a short, deterministic plain-language sequence generated from observed events. Example: “Gateway accepted the request → authentication completed in 18 ms → payment service called PostgreSQL → query took 1.8 s → request exceeded the 2 s deadline.” Only state what telemetry supports.
3. **Waterfall:** chronological spans with duration, parent/child hierarchy, service boundaries, critical path, and visible gaps.
4. **Process flow:** service and dependency transitions, including HTTP/gRPC, database, cache, and queue events.
5. **Errors:** exact failing event, status/exception, stack excerpt if available, fingerprint, and related occurrences.
6. **Log snippets:** bounded pre/post-event context with direct/heuristic correlation labels.
7. **Metrics around the event:** latency/error/throughput and relevant CPU, memory, pool, and queue indicators.
8. **Profiles:** relevant process/time-window profiles, when available.
9. **Evidence and confidence:** source of every claim, missing telemetry, clock skew, and correlation confidence.

### 13.3 RCA presentation

Use clear labels:

- **Observed fact** — directly present in a span, log, metric, profile, or host event.
- **Strong indication** — multiple independent evidence sources support the same explanation.
- **Possible cause** — plausible correlation, not yet proven.
- **Unknown / missing evidence** — required telemetry is absent or incomplete.

A generated RCA summary must never state a definitive root cause solely because two events happened near each other in time. Show the evidence chain and let the operator inspect each source.

Include:

- Incident timeline.
- Critical path and slowest contributing spans.
- Affected services/endpoints and frequency.
- First-seen/latest-seen and deployment correlation.
- Error fingerprint and representative examples.
- Relevant logs, profiles, and metric deltas.
- Data completeness and confidence.
- A copyable, plain-text evidence summary suitable for an incident ticket or postmortem.

Do not make AI-generated RCA a dependency for the core product. Deterministic explanations based on evidence must work offline and remain useful. An optional future assistant may summarize evidence, but it must cite the exact evidence IDs and distinguish facts from hypotheses.

### 13.4 Visual language

Use a restrained, dense-but-readable operational interface. Avoid decorative dashboards and oversized cards. Prioritize:

- One clear timeline.
- One readable service/dependency flow.
- One compact waterfall.
- Inline evidence markers.
- Consistent severity and latency encoding.
- Obvious navigation from a symptom to the underlying event.

Use SVG for diagrams and charts. Include accessible text equivalents and table views for every important visualization.

## 14. Operational configuration

Expose a small number of explicit configuration files and a simple UI editor. Include validated examples and document every field.

At minimum support:

- Agent discovery and process exclusions.
- Supported runtime/framework instrumentation toggles.
- Service naming and resource attributes.
- Endpoint normalization overrides.
- OTLP endpoints and authentication.
- Sampling and tail-sampling windows.
- Retention rules by signal and operation type.
- Log snapshot triggers and time windows.
- Ring-buffer sizes and memory budgets.
- Profile sampling rates and trigger thresholds.
- Redaction/allow-list rules.
- ClickHouse connection, batching, and storage policies.
- Object storage settings (optional).
- Query timeouts and per-user limits.
- System health thresholds.

The UI must show **effective configuration** after defaults, environment variables, and file configuration are merged. Validate config before applying it, support reload where safe, and show which services/agents have received the latest version.

## 15. Security, privacy, and access control

Treat observability data as sensitive production data.

- Redact Authorization headers, cookies, credentials, API keys, tokens, session IDs, and configured sensitive fields by default.
- Do not capture request/response bodies by default.
- Do not collect SQL bind values by default; store normalized query fingerprints/summaries.
- Support explicit allow-lists for attributes and log fields.
- Avoid storing raw personal identifiers when a stable hash or masked representation is sufficient.
- Encrypt transport with TLS; support authentication between agents, ingest, and query services.
- Add role-based access controls for service/environment scope and sensitive evidence.
- Audit policy edits, redaction changes, access to sensitive records, and manual evidence exports.
- Protect the platform from cardinality attacks, oversized attributes, malformed telemetry, compression bombs, and query abuse.
- Apply query timeouts and resource limits.
- Make retention and deletion verifiable.

## 16. Deployment and operational simplicity

### Single-host installation

The first usable deployment should run on a plain Ubuntu host without requiring Kubernetes:

- One Go control-plane/query/UI binary (or a very small number of clearly separated binaries).
- One lightweight Go host agent.
- ClickHouse.
- Local profile/evidence directory.
- systemd service units and a simple installer/configuration guide.

The platform must continue operating if optional components are absent. Make service ports, directories, limits, and credentials explicit.

### Kubernetes installation

Add:

- Host agent as a DaemonSet with only the required privileges/capabilities.
- Control plane and ingest as Deployments.
- ConfigMaps/Secrets and resource requests/limits.
- Namespace, pod, container, node, and workload metadata enrichment.
- Clear documentation for kernel version, eBPF capabilities, security context, and cluster permissions.

Do not require cluster-admin privileges if a narrower permission model is feasible. Document any unavoidable privilege requirements.

## 17. Performance and resource budgets

Set measurable budgets before optimizing. Treat the following as initial engineering targets to validate—not guarantees:

- Agent idle RSS target: **under 64 MiB per host**, excluding kernel memory and optional profilers.
- Agent CPU overhead target: **under 1% of one CPU core at representative normal load**, excluding deliberate profile capture.
- Bounded memory regardless of telemetry burst or backend outage.
- No application request blocking on telemetry persistence.
- Predictable disk usage with explicit queue/spool quotas.
- Ingest and UI query latency measured at realistic event volume.

Benchmark with representative workloads, including bursts, errors, slow requests, high-cardinality input, backend unavailability, and large traces. Report overhead against an uninstrumented baseline. If a target cannot be met, show measured results, explain the limiting factor, and offer a configuration trade-off.

## 18. MVP scope and delivery phases

Deliver in vertical slices. Keep the repository buildable and tests passing after each phase.

### Phase 0 — repository and feasibility assessment

- Inspect the existing repository, runtime environment, deployment model, and actual service/framework versions.
- Identify the target Linux kernel and eBPF capabilities.
- Produce a short architecture diagram, component inventory, risk list, and instrumentation compatibility matrix.
- Verify licensing and current maintenance status of proposed upstream dependencies.
- Write ADRs for agent approach, storage model, no-JavaScript UI, and auto-instrumentation coverage.
- Do not start with a large UI mockup or a broad rewrite.

### Phase 1 — end-to-end automatic HTTP tracing

- Host agent discovers a supported Go HTTP/Gin service.
- Capture method, normalized route, status, duration, service/process identity, trace ID, and span relationships where supported.
- OTLP ingestion and ClickHouse persistence.
- Search operations by service, route, method, status, and time.
- Server-rendered operation detail page with waterfall and evidence metadata.
- Demonstrate that the application source does not need manual span additions for the supported path.

### Phase 2 — dependencies and error evidence

- Add PostgreSQL and Redis client visibility for supported libraries.
- Add gRPC and asynchronous queue/job linkage where supported.
- Capture errors, timeouts, retries, and exception/panic signals within documented limits.
- Implement error fingerprints and operation-linked error views.

### Phase 3 — bounded log snapshots

- Implement local ring buffers and configurable log sources.
- Implement snapshot triggers, before/after windows, redaction, deduplication, compression, and retention.
- Show log snippets and a process-flow timeline with direct versus heuristic correlation.

### Phase 4 — policy engine and storage lifecycle

- Implement route/method/operation-based retention and sampling policies.
- Add rule precedence, policy preview, dry-run, audit, and TTL enforcement.
- Add service/endpoint metric rollups and storage-usage reporting.

### Phase 5 — profiling

- Add low-overhead CPU profiling.
- Add profile object storage, profile metadata, SVG flamegraphs, and before/after comparison.
- Add bounded incident-triggered capture and cooldown controls.

### Phase 6 — operational hardening

- Kubernetes deployment, authentication/RBAC, TLS, audit log, backup/restore, retention verification, load testing, failure testing, and installation documentation.

Do not build every phase as a disconnected subsystem. Each phase must integrate with the operation evidence model and primary investigation workflow.

## 19. Required acceptance tests

The MVP is not complete until the following can be demonstrated in an automated or reproducible test environment.

### Automatic tracing

- A supported Go HTTP/Gin request is visible without adding manual spans to the application handler.
- Method and normalized endpoint path are correct for dynamic route parameters.
- Parent-child span relationships are correct for supported boundaries.
- Unsupported boundaries are explicitly marked partial/unknown, not fabricated.

### Dependency correlation

- A request performing a PostgreSQL query shows a database operation linked to the correct request when the driver integration supports it.
- A Redis/cache operation is linked similarly where supported.
- A queue producer and worker are linked through trace context or clearly represented as separate linked operations.

### Error capture

- A server error and a supported runtime/database error appear in the operation detail.
- Repeated errors are fingerprinted and grouped.
- Sensitive fields are redacted before persistence.

### Log snapshots

- A configured error or slow-operation trigger flushes only the bounded before/after log window.
- No full log stream is persisted when no trigger occurs, except explicitly configured events.
- Buffer limits, drops, deduplication, and snapshot retention are tested.
- Logs without trace IDs are labeled as time/process correlations, not exact trace links.

### Retention and sampling

- A POST write endpoint and a GET read endpoint can receive different retention policies based on normalized route and method.
- Error/slow transaction retention differs from normal success retention.
- Policy preview predicts matching operations and estimated storage impact.
- TTL expiry removes raw data and makes expired records unavailable through search and object references.

### Profiling

- A CPU profile can be captured for a selected process/time interval and rendered as SVG.
- Profile capture respects memory/CPU/cooldown limits.
- The UI does not claim per-request attribution when only process-level time-window profiling is available.

### UI and toolchain

- All major views work with JavaScript disabled in the browser.
- No Node.js, npm, TypeScript, JavaScript build tool, or browser JS bundle is present in the product build.
- Every important graph has an accessible text/table alternative.

### Failure and overhead

- Agent/backend disconnection does not block application traffic.
- Queue and memory limits remain bounded under a telemetry burst.
- ClickHouse outage and recovery behavior are tested.
- Instrumented versus uninstrumented performance and resource consumption are benchmarked and documented.

## 20. Engineering rules for the implementation agent

- Start by inspecting the actual repository and environment. Do not assume the codebase structure.
- Produce an implementation plan with small, testable milestones, then implement the first end-to-end slice.
- Prefer a working vertical slice over many empty modules, mock screens, or placeholder interfaces.
- Do not generate fake telemetry in production code. Synthetic data is allowed only in fixtures, demos, and tests, and must be clearly labeled.
- Do not silently substitute a weaker technology or add JavaScript to work around a UI limitation.
- Do not introduce a new database, queue, cache, or service without documenting the operational cost and reason.
- Keep public interfaces and stored schemas versioned.
- Add unit tests, integration tests, migration tests, and reproducible performance benchmarks.
- Include realistic test services that generate normal, slow, failed, database-backed, cache-backed, and asynchronous operations.
- Treat resource limits, data deletion, redaction, and telemetry-loss accounting as first-class features—not future cleanup.
- Add structured application logs for the platform itself, but prevent recursive self-observability loops.
- Every configuration option must have a documented default, valid range, and effect.
- Every UI claim must be traceable to a source evidence record.
- Keep documentation concise and operational: installation, configuration, architecture, troubleshooting, limits, upgrade, backup, and restore.
- Do not stop after generating a proposal. Implement the first usable vertical slice unless the repository or permissions make that impossible; report blockers precisely.

## 21. Required deliverables

1. Architecture overview and component diagram.
2. ADRs explaining the major technology and data-model decisions.
3. Instrumentation compatibility matrix with tested versions and known gaps.
4. Agent and ingest implementation with bounded resource behavior.
5. ClickHouse schema, indexes, materialized views, TTL policies, and migrations.
6. Server-rendered, JavaScript-free UI for operations, operation detail, services, incidents, profiles, and policies (phase-gated; do not fake unfinished screens).
7. Example configuration for agent, ingestion, sampling, retention, log snapshots, profiling, and redaction.
8. Single-host Ubuntu deployment and systemd units.
9. Kubernetes deployment manifests when that phase is reached.
10. Automated tests and reproducible load/overhead benchmarks.
11. Documentation for operational installation, data lifecycle, troubleshooting, and limitations.
12. A demonstration scenario that follows one request across an API, PostgreSQL, Redis, and an asynchronous worker, then shows the error/slow-path evidence as one coherent operational story.

## 22. Final product quality bar

The platform succeeds when an on-call engineer can open it, select a failing or slow operation, and quickly understand:

- the actual sequence of events;
- which service and dependency consumed the time;
- where the error originated or where evidence points;
- what logs and profiles are available around the event;
- what data is missing or only correlated heuristically;
- how often the issue occurs and which endpoints are affected;
- what evidence can be attached to an RCA or incident report.

The experience should feel like following the story of one real operation through a distributed system—not querying disconnected telemetry products.

**Optimize for operational truth, automatic usefulness, explainability, low overhead, and low storage cost. Do not optimize for feature count.**
