//go:build linux

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shohiebsense/leutik-tarik/internal/agent/bpf"
)

// runProbe attaches a uprobe and forwards hits to ingest until interrupted.
func runProbe(cfg probeConfig) error {
	p, err := bpf.Load()
	if err != nil {
		return fmt.Errorf("load bpf (needs CAP_BPF + CAP_PERFMON + BTF): %w", err)
	}
	defer p.Close()

	if err := p.AttachUprobe(cfg.Binary, cfg.Symbol); err != nil {
		return err
	}
	fmt.Printf("attached uprobe %s in %s, emitting to %s\n", cfg.Symbol, cfg.Binary, cfg.EmitURL)

	em := bpf.NewEmitter(cfg.EmitURL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	emitted, failed := 0, 0
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				return fmt.Errorf("probe reader stopped after %d events: %v", emitted, p.Err())
			}
			if err := em.Emit(bpf.Request(e, cfg.Symbol, cfg.Service, e.Time())); err != nil {
				failed++
				// Telemetry loss is counted, not hidden, and never fatal to the
				// observed process.
				fmt.Fprintf(os.Stderr, "emit failed (%d total): %v\n", failed, err)
				continue
			}
			emitted++
			if emitted%100 == 0 {
				fmt.Printf("emitted %d events (%d failed)\n", emitted, failed)
			}
		case <-stop:
			fmt.Printf("stopping: %d emitted, %d failed\n", emitted, failed)
			return nil
		}
	}
}
