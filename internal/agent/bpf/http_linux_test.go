//go:build linux

package bpf

import (
	"bufio"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestReadsStatusCodeFromNetHTTP is the real question: can a probe read a value
// out of Go's standard library, from a server that was never instrumented?
//
// Target: net/http.(*response).WriteHeader(code int) — receiver in AX, code in
// BX, so arg1 must be the status the handler wrote.
func TestReadsStatusCodeFromNetHTTP(t *testing.T) {
	target := os.Getenv("LEUTIK_HTTP_TEST_BINARY")
	if target == "" {
		dir := t.TempDir()
		target = filepath.Join(dir, "http_target")
		cmd := exec.Command("go", "build", "-o", target, "./testdata/http_target.go")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot build fixture (%v): %s", err, out)
		}
	}

	p, err := LoadArgs()
	if err != nil {
		t.Skipf("cannot load args probe: %v", err)
	}
	defer p.Close()

	if err := p.AttachUprobe(target, "net/http.(*response).WriteHeader"); err != nil {
		t.Skipf("cannot attach to net/http.(*response).WriteHeader: %v", err)
	}

	run := exec.Command(target)
	stdout, err := run.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	run.Stderr = os.Stderr
	if err := run.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

	// Wait for the server to accept traffic.
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			close(ready)
			return
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not start")
	}
	time.Sleep(200 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18099/fail")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("fixture returned %d", resp.StatusCode)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				t.Fatalf("event channel closed: %v", p.Err())
			}
			if e.Arg0 == 0 {
				continue // receiver pointer resolved to nil-ish; skip noise
			}
			if e.Arg1 == 503 {
				t.Logf("net/http status read from an uninstrumented server: %d (comm=%q)", e.Arg1, e.CommString())
				return
			}
		case <-deadline:
			t.Fatal("no WriteHeader(503) captured within 10s")
		}
	}
}
