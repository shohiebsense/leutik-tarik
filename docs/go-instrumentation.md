# Go instrumentation through uprobes — what works, what doesn't

Status: **partial HTTP spans shipping** — status and duration captured from an
uninstrumented server, method/route still missing (see below). This document records
the measurements so the next session does not repeat the research.

## The question this answers

Can leutik-tarik capture HTTP activity from a Go service **without changing its
code**? That requires reading function arguments at a uprobe, and Go 1.17+
passes arguments in **registers**, not on the stack: AX, BX, CX, DI, SI, R8, R9,
R10, R11 (receiver counts as the first argument).

## Proven (measured, 2026-11-18)

| Test | Result |
|---|---|
| Read a user-space function's argument register | `arg0=4242` — exact value read from `main.mark(4242)` |
| Attach to a **standard library** symbol | `net/http.(*response).WriteHeader` resolved and attached |
| Read a value the stdlib received | **`503`** captured from an uninstrumented `net/http` server (receiver in AX, `code` in BX) |

So the two things that were genuinely uncertain are settled:

1. `pt_regs` at a uprobe carries Go's argument registers, and reading them works
   (`rax`, `rbx`, `rcx` at fixed offsets of the x86_64 `pt_regs` layout).
2. Go binaries expose symbol names well enough that stdlib methods can be
   probed by name (`net/http.(*response).WriteHeader`), with no build tags and
   no app changes.

## Shipping now: partial spans (measured 2026-11-18)

`leutik-agent -http-binary <path>` attaches to `net/http.serverHandler.ServeHTTP`
(entry, start time) and `net/http.(*response).WriteHeader` (status + elapsed),
and emits one span per completed request.

Measured against an uninstrumented server, 3x `/fail` (503) and 3x `/ok` (200):

```
service      name                  outcome  completeness  source  ms
probe-http   http request (probe)  error    partial       agent   0.023
probe-http   http request (probe)  success  partial       agent   0.010
... 6 spans, all partial/agent
```

The spans carry `leutik.capture.missing = http.request.method,http.route` and
`leutik.capture.correlation = os-thread`, so a reader is told what the probe did
not see rather than left to infer it.

**Correlation weakness, stated plainly:** start and finish are matched by OS
thread id, and Go may migrate a goroutine between threads mid-call. Under heavy
concurrency a duration can therefore pair with the wrong request. Fixing that
needs a goroutine id, which means reading runtime internals — the same work that
unlocks method and route.

## What is NOT solved yet

| Piece | Difficulty | Why |
|---|---|---|
| **Duration** | easy | pair an entry and an exit probe, keep per-goroutine state in a hash map |
| **Status code** | done | proven above |
| **HTTP method** | medium | requires reading a Go string (`{ptr,len}`) out of `*http.Request`, then the field **offset within the struct** |
| **Route template** | hard | the pattern lives in `ServeMux`/framework router state, not on the request; Go binaries carry no BTF, so offsets are per-Go-version |
| **Trace assembly** | medium | generate IDs, group spans by connection/goroutine, stitch children |

The blocker for method and route is **struct field offsets**, which change
between Go releases and cannot be derived at runtime (Go binaries ship no type
metadata a probe can read). OBI maintains a per-version offset table for exactly
this reason. That is the work — not the kernel side, which already works.

## Honest capability statement

If shipped as-is, a probe could report: *"this process served N requests, with
these status codes and durations, at these times"* — no method, no route. That
is a real capability, and it must be reported as `completeness=partial`, never
as a complete HTTP span. Which is also why the platform stores completeness per
record rather than inferring it at read time.

## Practical constraints found

- The probed binary must be on local disk or `hostPath`: a uprobe on a binary
  bind-mounted from macOS (virtiofs) fails with `perf_event_open: EIO`
  (`docs/agent-portability.md §5c`).
- Symbols must exist: stripping a Go binary (`-ldflags "-s -w"`) removes the
  names and with them the ability to attach by name.
- Inlined functions have no symbol to attach to. `WriteHeader` and similar
  stdlib methods are large enough not to be inlined; small helpers may not be
  probeable at all.

## Reproduce

```sh
make agent-bpf                      # builds probe.bpf.o and args.bpf.o in a container

CGO_ENABLED=0 GOOS=linux go test -c -o /tmp/args.test ./internal/agent/bpf
CGO_ENABLED=0 GOOS=linux go build -o /tmp/http_target ./internal/agent/bpf/testdata/http_target.go

docker run --rm --privileged -e LEUTIK_HTTP_TEST_BINARY=/target \
  -v /tmp/http.test:/t/http.test -v /tmp/http_target:/src/target \
  alpine sh -c 'cp /src/target /target && chmod +x /target && /t/http.test -test.run TestReadsStatusCodeFromNetHTTP -test.v'
```
