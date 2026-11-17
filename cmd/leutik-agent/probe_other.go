//go:build !linux

package main

import "fmt"

// runProbe reports that probing is unavailable here rather than silently doing
// nothing (build prompt §5.2: report unsupported).
func runProbe(cfg probeConfig) error {
	return fmt.Errorf("eBPF probing requires Linux; this host is not supported")
}
