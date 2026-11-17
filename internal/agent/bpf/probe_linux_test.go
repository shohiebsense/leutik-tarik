//go:build linux

package bpf

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestUprobeFires proves the whole path: compile-time object -> kernel load ->
// uprobe attach on a Go binary -> ring buffer -> Go channel.
//
// It needs privileges and BTF, so it skips (with the reason) on a host that
// cannot probe. Pre-build the fixture and test binary to run it in a container
// without a Go toolchain: see LEUTIK_PROBE_TEST_BINARY.
func TestUprobeFires(t *testing.T) {
	target := os.Getenv("LEUTIK_PROBE_TEST_BINARY")
	if target == "" {
		// Build the fixture here; skip if there is no toolchain.
		dir := t.TempDir()
		target = filepath.Join(dir, "target")
		cmd := exec.Command("go", "build", "-o", target, "./testdata/target.go")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot build fixture (%v): %s", err, out)
		}
	}

	p, err := Load()
	if err != nil {
		t.Skipf("cannot load bpf (needs CAP_BPF + CAP_PERFMON + BTF): %v", err)
	}
	defer p.Close()

	if err := p.AttachUprobe(target, "main.fire"); err != nil {
		t.Skipf("cannot attach uprobe: %v", err)
	}

	run := exec.Command(target)
	run.Stdout = os.Stderr
	if err := run.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

	deadline := time.After(10 * time.Second)
	select {
	case e, ok := <-p.Events():
		if !ok {
			t.Fatalf("event channel closed early: %v", p.Err())
		}
		if e.TGID == 0 {
			t.Errorf("event has no process id: %+v", e)
		}
		if e.CommString() == "" {
			t.Errorf("event has no comm: %+v", e)
		}
		t.Logf("probe fired: tgid=%d tid=%d comm=%q", e.TGID, e.PID, e.CommString())
	case <-deadline:
		t.Fatal("no probe event within 10s")
	}
}
