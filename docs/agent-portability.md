# Agent portability — what BTF is, what a container can and cannot provide

Question this answers: *"BTF — is it a prerequisite? Can we install everything
portably via Docker/Kubernetes instead of having prerequisites?"*

Short answer: **the userspace half is fully containerizable; the kernel half is
not, and never can be — but on any modern host the kernel half is already
satisfied, and Kubernetes can grant the remaining privileges declaratively.**
So the goal is not "zero prerequisites", it is "zero *installs*, and a
self-diagnosing agent that names the exact prerequisite that is missing".

Everything below marked **[measured]** was run on this machine on 2026-11-17.

---

## 1. What BTF actually is

**BTF = BPF Type Format.** A compressed blob of *type metadata* about the running
kernel — every struct, field, enum, and function signature, with offsets.

It is not a library and not a driver. It is emitted by the kernel build
(`CONFIG_DEBUG_INFO_BTF=y`) and published by the kernel at
`/sys/kernel/btf/vmlinux`.

**Why an eBPF agent needs it (CO-RE).** A probe reads kernel or application
structures, e.g. "the 4th field of `struct sock`". Those offsets differ between
kernel builds. Without type metadata you must compile the probe **against the
exact headers of the exact kernel** — the old, brittle model: one binary per
kernel version.

With BTF, a single compiled program carries **relocations** instead of fixed
offsets; the loader reads `/sys/kernel/btf/vmlinux` and rewrites the offsets at
load time. That is **CO-RE — Compile Once, Run Everywhere**. It is the reason an
agent can ship as one container image instead of a matrix of kernel-specific
builds.

So BTF is the *portability mechanism itself*, not an optional extra.

## 2. The real prerequisite chain

Five layers. Only the bottom three are host-side, and none of them is an
`apt install` of our software.

| # | Layer | Requirement | Deliverable by container/K8s? |
|---|---|---|---|
| 1 | Kernel version | uprobes/kprobes (`CONFIG_UPROBE_EVENTS`), `CAP_BPF` split (≥ 5.8), ideally ≥ 5.8 | ❌ kernel is the host's |
| 2 | Kernel type info | `CONFIG_DEBUG_INFO_BTF=y` → `/sys/kernel/btf/vmlinux` | ❌ (but see BTFHub fallback, §4) |
| 3 | Sysctls | `unprivileged_bpf_disabled`, `perf_event_paranoid`, `kptr_restrict` | ⚠️ only if node tuning is permitted |
| 4 | Capabilities | `CAP_BPF`, `CAP_PERFMON`, `CAP_SYS_RESOURCE`, `CAP_SYS_ADMIN` (uprobe/debugfs) | ✅ declaratively, via `securityContext` |
| 5 | Userspace | our static Go binary + embedded BPF objects; optional clang only to *build* | ✅ fully, in the image |

Nothing in layers 1–3 can be installed by a container: a container *shares the
host kernel*. There is no such thing as a container that brings its own kernel
(except nested-VM schemes like Kata/microVM, which are a different deployment
model).

## 3. Measured: what this machine actually has

| Probe | Result |
|---|---|
| Docker Desktop VM kernel | `6.10.14-linuxkit` **[measured]** |
| `/sys/kernel/btf/vmlinux` | **present, 6,666,502 bytes** **[measured]** |
| `unprivileged_bpf_disabled` | `0` **[measured]** |
| `perf_event_paranoid` | `2` **[measured]** |
| privileged container: `kprobe_events` / `uprobe_events` | **both present** after mounting debugfs **[measured]** |
| privileged container `CapEff` | `000001ffffffffff` (full) **[measured]** |
| default container `CapEff` | `00000000a80425fb` (no BPF/PERFMON/SYS_ADMIN) **[measured]** — BTF still readable, probes not attachable |

**Consequences that change the plan:**

1. **The eBPF agent can be developed and tested on this Mac**, inside a
   privileged Docker container — the LinuxKit VM has a modern kernel with BTF.
   Phase 1c is *not* blocked on Jupler access.
2. The agent must run with elevated capabilities; a default container is not
   enough. That is a `securityContext`, not an installation.

## 4. Portability ladder — cheapest useful option first

| Level | Mechanism | Host prerequisite | App change | Status |
|---|---|---|---|---|
| **L0** | OTLP SDK / any OTel exporter | none | yes (one env var) | **working today** |
| **L1** | containerized agent, privileged DaemonSet or `docker run --privileged` | kernel ≥ 5.8 + BTF | no | the target |
| **L2** | L1 + **embedded BTFHub blobs** (per-distro BTF shipped in the image, selected at runtime by `uname -r`) | kernel ≥ 5.8, **BTF not required** | no | fallback for old distro kernels |
| **L3** | sidecar/proxy capture (HTTP/gRPC at the network edge) | none | no | later, if L1/L2 blocked |
| **L4** | agent in relay mode: reads OTLP from elsewhere, no probes | none | yes | already covered by ingest |

L2 is the answer to "BTF missing": ship the type metadata ourselves
(`cilium/ebpf` + a BTFHub archive keyed by kernel release). It removes
prerequisite #2 but not #1 — a kernel that cannot load BPF cannot be helped by
any image.

## 5. What Kubernetes changes, and what it does not

**Kubernetes gives:** declarative capability grants, `hostPID`, host path mounts
of `/sys/kernel/btf`, node selector for "only nodes we know are capable", and a
single rollout mechanism.

```yaml
# shape, not final manifest
kind: DaemonSet
spec:
  template:
    spec:
      hostPID: true
      containers:
        - name: agent
          securityContext:
            capabilities:
              add: ["BPF", "PERFMON", "SYS_RESOURCE", "SYS_ADMIN"]
          volumeMounts:
            - {name: btf, mountPath: /sys/kernel/btf, readOnly: true}
      volumes:
        - name: btf
          hostPath: {path: /sys/kernel/btf}
```

**Kubernetes does not give:** a newer kernel, BTF on a kernel built without it,
or BPF on a runtime that blocks it. Known blockers: **GKE Autopilot** (no
privileged/hostPID workloads), gVisor/Kata sandboxes, hardened seccomp profiles,
Windows nodes, and most managed "serverless" container platforms.

So the honest framing: **Kubernetes can *grant* the prerequisites; it cannot
*provide* them.** The nodes still need a capable kernel — which every mainstream
image since Ubuntu 20.10 / Debian 11 / RHEL 9 already has.

## 5b. Build-time constraint (measured)

Apple's clang **cannot compile BPF**: `clang -print-targets | grep -c bpf` → `0`
on macOS (Apple clang 17). Linux clang in a container reports **3** bpf targets.

Consequences for the repository:

- BPF objects must be compiled in a Linux container (or Linux host), never with
  the macOS toolchain. `make agent-bpf` must shell out to a container.
- Generated objects are committed, so a normal build and the CI unit tests do
  not need clang at all. Only regenerating them does.
- `bpf2go` (cilium/ebpf) is the generator; `bpftool`/`llvm-strip` are not
  installed on this host either, which is another reason to keep the whole
  compile step inside the container.

## 5c. Where the probed binary lives matters (measured)

A uprobe failed with `perf_event_open: input/output error` on a binary that was
bind-mounted from macOS, and succeeded on the identical binary copied into the
container:

| Target location | Result |
|---|---|
| macOS bind mount (virtiofs), `/t/fire` | **EIO — attach refused** |
| container-local disk, `/fire` (same file, `cp`) | **attached, events flowed** |

So a uprobe needs the executable on a filesystem the kernel can mmap normally.
Practical rules: keep target binaries on `hostPath`/local disk, never probe from
NFS, ConfigMap/Secret mounts, or (on Docker Desktop) a macOS bind mount. The
agent will report this as an attach failure, not as "no traffic".

## 5d. The pipeline is proven (measured)

kernel object → load → uprobe on a Go binary → ring buffer → Go → OTLP → ClickHouse → UI:

```
attached uprobe main.fire in /fire, emitting to http://127.0.0.1:8080
stopping: 63 emitted, 0 failed
operations page: 63 x "uprobe main.fire"  (service probe-target)
clickhouse:      probe-target  uprobe main.fire  agent  correlated  63
```

Probe spans are stored as `source=agent`, `completeness=correlated` — one
function call is not evidence of a whole request, and the record says so.

## 6. Decision

1. **Ship the agent as a container image** (multi-arch, static Go binary, BPF
   objects embedded — no clang, no kernel headers at runtime). This is the
   "portable install" the question asks for, and it is achievable.
2. **Ship a `leutik agent doctor` command** that prints the exact failing layer:
   kernel version, BTF presence/size, each sysctl, each capability, and whether
   a probe can actually attach. That converts "prerequisites" from a document
   into a self-diagnosis, which is the part that actually removes support load.
3. **Include BTFHub blobs** in the image for L2, so "kernel without BTF" is a
   warning, not a wall.
4. **Keep L0 (OTLP SDK) as the documented answer** for hosts where probes are
   impossible. The platform already accepts that traffic, so no user is ever
   blocked — they just get less automatic capture.
5. **Never silently degrade.** If probes cannot attach, the agent reports
   `unsupported` for that capability (build prompt §5.2) instead of pretending
   it is collecting.
