//go:build linux

package bpf

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestArgumentRegisters proves the one thing automatic HTTP capture depends on:
// a uprobe can read Go's argument registers at function entry.
//
// Go 1.17+ passes arguments in AX/BX/CX..., so if this passes, method and
// status-code extraction is a matter of knowing which argument holds what.
// If it fails, automatic capture needs a different mechanism entirely.
func TestArgumentRegisters(t *testing.T) {
	target := os.Getenv("LEUTIK_ARGS_TEST_BINARY")
	if target == "" {
		dir := t.TempDir()
		target = filepath.Join(dir, "args_target")
		cmd := exec.Command("go", "build", "-o", target, "./testdata/args_target.go")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot build fixture (%v): %s", err, out)
		}
	}

	p, err := LoadArgs()
	if err != nil {
		t.Skipf("cannot load args probe (needs privileges + BTF): %v", err)
	}
	defer p.Close()

	if err := p.AttachUprobe(target, "main.mark"); err != nil {
		t.Skipf("cannot attach uprobe: %v", err)
	}

	run := exec.Command(target)
	run.Stdout = os.Stderr
	if err := run.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				t.Fatalf("event channel closed: %v", p.Err())
			}
			// main.mark(4242): arg0 (AX) must be the constant.
			if e.Arg0 == 4242 {
				t.Logf("register read OK: arg0=%d comm=%q tgid=%d", e.Arg0, e.CommString(), e.TGID)
				return
			}
		case <-deadline:
			t.Fatal("no event with arg0=4242 within 10s: register ABI read did not work")
		}
	}
}
