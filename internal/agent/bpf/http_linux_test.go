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

// TestHTTPProbeProducesSpan drives a real request through an uninstrumented
// server and asserts the probe reports the right status AND a plausible
// duration in one event.
func TestHTTPProbeProducesSpan(t *testing.T) {
	target := os.Getenv("LEUTIK_HTTP_TEST_BINARY")
	if target == "" {
		dir := t.TempDir()
		target = filepath.Join(dir, "http_target")
		cmd := exec.Command("go", "build", "-o", target, "./testdata/http_target.go")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot build fixture (%v): %s", err, out)
		}
	}

	p, err := LoadHTTP()
	if err != nil {
		t.Skipf("cannot load http probe: %v", err)
	}
	defer p.Close()
	if err := p.Attach(target); err != nil {
		t.Skipf("cannot attach: %v", err)
	}

	run := exec.Command(target)
	stdout, _ := run.StdoutPipe()
	run.Stderr = os.Stderr
	if err := run.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			return
		}
	}()
	time.Sleep(300 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18099/fail")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				t.Fatalf("channel closed: %v", p.Err())
			}
			if e.Status != 503 {
				continue
			}
			if e.Duration() <= 0 {
				t.Fatalf("status 503 but no duration: %+v", e)
			}
			span := HTTPSpan(e, "probe-http", time.Now())
			if span.EndTimeUnixNano <= span.StartTimeUnixNano {
				t.Error("span end must be after start")
			}
			t.Logf("probe span: status=%d duration=%v comm=%q", e.Status, e.Duration(), e.CommString())
			return
		case <-deadline:
			t.Fatal("no 503 event within 10s")
		}
	}
}
