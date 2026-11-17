package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Verdict is the doctor's answer for one capability.
type Verdict string

const (
	VerdictOK          Verdict = "ok"
	VerdictDegraded    Verdict = "degraded"
	VerdictUnavailable Verdict = "unavailable"
)

// Check is one prerequisite with its outcome and the reason, so a user never
// has to guess why instrumentation is not working (build prompt §5.2: report
// unsupported rather than pretend).
type Check struct {
	Name    string
	Verdict Verdict
	Detail  string
}

// Report is the full diagnosis.
type Report struct {
	Kernel     string
	Checks     []Check
	CanUprobes bool
	CanProfile bool
}

// capability bit numbers we care about (linux/capability.h).
var capNames = map[int]string{
	19: "CAP_SYS_PTRACE",
	21: "CAP_SYS_ADMIN",
	24: "CAP_SYS_RESOURCE",
	38: "CAP_PERFMON",
	39: "CAP_BPF",
}

// Diagnose inspects sysfs/procfs for the prerequisites documented in
// docs/agent-portability.md. root is "/" normally; injectable for tests.
func Diagnose(root string) Report {
	rep := Report{Kernel: strings.TrimSpace(readTrimmed(filepath.Join(root, "proc/sys/kernel/osrelease")))}

	btfPath := filepath.Join(root, "sys/kernel/btf/vmlinux")
	if info, err := os.Stat(btfPath); err == nil {
		rep.Checks = append(rep.Checks, Check{
			Name:    "kernel BTF",
			Verdict: VerdictOK,
			Detail:  fmt.Sprintf("%s present (%d bytes) — CO-RE probes can relocate", btfPath, info.Size()),
		})
	} else {
		rep.Checks = append(rep.Checks, Check{
			Name:    "kernel BTF",
			Verdict: VerdictUnavailable,
			Detail:  "no /sys/kernel/btf/vmlinux — probes would need per-kernel builds; ship BTFHub blobs for this kernel release",
		})
	}

	unpriv := readTrimmed(filepath.Join(root, "proc/sys/kernel/unprivileged_bpf_disabled"))
	switch unpriv {
	case "0":
		rep.Checks = append(rep.Checks, Check{"unprivileged_bpf_disabled", VerdictOK, "0 — BPF syscall allowed"})
	case "":
		rep.Checks = append(rep.Checks, Check{"unprivileged_bpf_disabled", VerdictDegraded, "unreadable — assumes BPF may be restricted"})
	default:
		rep.Checks = append(rep.Checks, Check{"unprivileged_bpf_disabled", VerdictDegraded, unpriv + " — BPF requires privileges"})
	}

	paranoid := readTrimmed(filepath.Join(root, "proc/sys/kernel/perf_event_paranoid"))
	switch {
	case paranoid == "":
		rep.Checks = append(rep.Checks, Check{"perf_event_paranoid", VerdictDegraded, "unreadable"})
	case paranoid <= "1":
		rep.Checks = append(rep.Checks, Check{"perf_event_paranoid", VerdictOK, paranoid + " — CPU profiling usable"})
		rep.CanProfile = true
	default:
		rep.Checks = append(rep.Checks, Check{"perf_event_paranoid", VerdictDegraded, paranoid + " — profiling needs privileges"})
	}

	gained := map[string]bool{}
	eff := capabilityMask(readTrimmed(filepath.Join(root, "proc/self/status")))
	if eff == 0 {
		rep.Checks = append(rep.Checks, Check{"capabilities", VerdictDegraded, "CapEff unreadable"})
	} else {
		var have, missing []string
		for bit, name := range capNames {
			if eff&(1<<uint(bit)) != 0 {
				have = append(have, name)
				gained[name] = true
			} else {
				missing = append(missing, name)
			}
		}
		verdict := VerdictDegraded
		detail := "have: " + strings.Join(have, ", ")
		if len(missing) > 0 {
			detail += " | missing: " + strings.Join(missing, ", ")
		}
		if gained["CAP_BPF"] && gained["CAP_SYS_ADMIN"] {
			verdict = VerdictOK
			// uProbes need SYS_ADMIN for the perf interface plus BPF.
			rep.CanUprobes = true
		}
		rep.Checks = append(rep.Checks, Check{"capabilities", verdict, detail})
	}

	return rep
}

// capabilityMask parses the CapEff line out of /proc/self/status.
func capabilityMask(status string) uint64 {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		// A 64-bit kernel reports 16 hex digits; anything longer is truncated
		// to the low 64 bits, which is all capability numbers 0-63 use.
		if len(hex) > 16 {
			hex = hex[len(hex)-16:]
		}
		n, err := strconv.ParseUint(hex, 16, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// Summary is a one-line answer suitable for a CLI or a log line.
func (r Report) Summary() string {
	state := "unavailable"
	if r.CanUprobes {
		state = "uprobes available"
	} else if len(r.Checks) > 0 {
		state = "uprobes unavailable"
	}
	profiling := "profiling off"
	if r.CanProfile {
		profiling = "profiling on"
	}
	return fmt.Sprintf("kernel %s: %s, %s", r.Kernel, state, profiling)
}
