//go:build linux

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/agent/bpf"
)

// runHTTPProbe attaches to a Go binary's net/http and streams spans.
func runHTTPProbe(cfg probeConfig) error {
	p, err := bpf.LoadHTTP()
	if err != nil {
		return fmt.Errorf("load http probe (needs CAP_BPF + CAP_PERFMON + BTF): %w", err)
	}
	defer p.Close()

	if err := p.Attach(cfg.Binary); err != nil {
		return err
	}
	fmt.Printf("http probe attached to %s -> %s (partial capture: status+duration only)\n", cfg.Binary, cfg.EmitURL)

	em := bpf.NewEmitter(cfg.EmitURL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	emitted, failed := 0, 0
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				return fmt.Errorf("probe stopped after %d spans: %v", emitted, p.Err())
			}
			// The event carries the completion time; back-date by the measured
			// duration so the span covers the request, not just its end.
			start := time.Now().Add(-e.Duration())
			if err := em.Emit(bpf.HTTPRequest(e, cfg.Service, start)); err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "emit failed (%d total): %v\n", failed, err)
				continue
			}
			emitted++
			if emitted%50 == 0 {
				fmt.Printf("emitted %d spans (%d failed)\n", emitted, failed)
			}
		case <-stop:
			fmt.Printf("stopping: %d emitted, %d failed\n", emitted, failed)
			return nil
		}
	}
}
