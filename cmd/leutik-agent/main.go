// Command leutik-agent reports what runs on this host and whether the host can
// be instrumented. Read-only; it never modifies the observed system.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/shohiebsense/leutik-tarik/internal/agent"
)

func main() {
	procRoot := flag.String("proc", "/proc", "procfs root (for tests and chroots)")
	sysRoot := flag.String("sys", "/", "filesystem root for sysfs/procfs diagnosis")
	asJSON := flag.Bool("json", false, "machine-readable output")
	doctorOnly := flag.Bool("doctor", false, "only check instrumentation prerequisites")
	discoverOnly := flag.Bool("discover", false, "only list discovered services")
	flag.Parse()

	// Default: both, because the useful question is "what is here, and can we
	// observe it".
	runDoctor := *doctorOnly || !*discoverOnly
	runDiscover := *discoverOnly || !*doctorOnly

	var report agent.Report
	if runDoctor {
		report = agent.Diagnose(*sysRoot)
	}

	var services []agent.Service
	var discoverErr error
	if runDiscover {
		services, discoverErr = agent.Discover(*procRoot)
	}

	if *asJSON {
		out := map[string]any{
			"doctor":   report,
			"services": services,
		}
		if discoverErr != nil {
			out["discover_error"] = discoverErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if runDoctor {
		fmt.Printf("%s\n\n", report.Summary())
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, c := range report.Checks {
			fmt.Fprintf(w, "  %-8s\t%s\t%s\n", c.Verdict, c.Name, c.Detail)
		}
		_ = w.Flush()
		fmt.Println()
	}

	if runDiscover {
		if discoverErr != nil {
			// Not fatal: doctor output is still useful on a platform without
			// procfs (e.g. macOS).
			fmt.Printf("discovery: %v\n", discoverErr)
			return
		}
		if len(services) == 0 {
			fmt.Println("discovery: no processes holding a listening TCP socket")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PORT\tPID\tNAME\tCONTAINER\tCOMMAND")
		for _, s := range services {
			// One process often holds both the IPv4 and IPv6 listener on the
			// same port; show the port once.
			seen := map[int]struct{}{}
			var uniq []string
			for _, p := range s.Ports {
				if _, dup := seen[p.Port]; dup {
					continue
				}
				seen[p.Port] = struct{}{}
				uniq = append(uniq, fmt.Sprint(p.Port))
			}
			ports := strings.Join(uniq, ",")
			container := s.Container
			if container == "" {
				container = "-"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", ports, s.PID, s.Name, container, truncate(s.Cmdline, 70))
		}
		_ = w.Flush()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
