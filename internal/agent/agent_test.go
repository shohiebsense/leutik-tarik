package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHexAddr(t *testing.T) {
	cases := []struct {
		in   string
		ip   string
		port int
	}{
		{"0100007F:1F90", "127.0.0.1", 8080},
		{"00000000:0016", "0.0.0.0", 22},
		{"0B0000C0:1F90", "192.0.0.11", 8080}, // little-endian per byte
	}
	for _, c := range cases {
		ip, port, err := parseHexAddr(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if ip != c.ip || port != c.port {
			t.Errorf("%s => %s:%d, want %s:%d", c.in, ip, port, c.ip, c.port)
		}
	}
	if _, _, err := parseHexAddr("junk"); err == nil {
		t.Error("expected error for junk address")
	}
}

const tcpFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 123456 1 0000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 654321 1 0000 100 0 0 10 0
   2: 0100007F:1F90 0100007F:D4A2 01 00000000:00000000 00:00000000 00000000  1000        0 999999 1 0000 100 0 0 10 0
`

func TestParseTCP(t *testing.T) {
	socks, err := ParseTCP("tcp", tcpFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(socks) != 3 {
		t.Fatalf("want 3 sockets, got %d", len(socks))
	}
	if !socks[0].Listening() {
		t.Error("first socket should be LISTEN")
	}
	if socks[0].Inode != 123456 || socks[0].Port != 8080 {
		t.Errorf("socket = %+v", socks[0])
	}
	if socks[2].Listening() {
		t.Error("ESTABLISHED (01) must not count as listening")
	}
}

func TestDiscoverFromFixture(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "net", "tcp"), tcpFixture)

	// pid 4242 holds the listening socket.
	writeFile(t, filepath.Join(root, "4242", "comm"), "payment\n")
	writeFile(t, filepath.Join(root, "4242", "cmdline"), "/app/payment\x00--port\x00--db-password=hunter2\x00")
	writeFile(t, filepath.Join(root, "4242", "cgroup"), "0::/docker/abc123def4567890\n")
	mustSymlink(t, "socket:[123456]", filepath.Join(root, "4242", "fd", "3"))

	// pid 4343 holds nothing relevant.
	writeFile(t, filepath.Join(root, "4343", "comm"), "idle\n")

	services, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("want 1 service, got %d: %+v", len(services), services)
	}
	s := services[0]
	if s.PID != 4242 || s.Name != "payment" {
		t.Errorf("service = %+v", s)
	}
	if s.Ports[0].Port != 8080 {
		t.Errorf("port = %d", s.Ports[0].Port)
	}
	if s.Container != "docker:abc123def456" {
		t.Errorf("container = %q", s.Container)
	}
	if strings.Contains(s.Cmdline, "hunter2") {
		t.Errorf("credential leaked into cmdline: %q", s.Cmdline)
	}
	if !strings.Contains(s.Cmdline, "[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", s.Cmdline)
	}
}

func TestDiscoverWithoutProcfs(t *testing.T) {
	if _, err := Discover(t.TempDir()); err != ErrUnsupportedPlatform {
		t.Fatalf("want ErrUnsupportedPlatform, got %v", err)
	}
}

func TestParseCgroup(t *testing.T) {
	cases := map[string]string{
		"0::/docker/abcdef1234567890\n":                    "docker:abcdef123456",
		"0::/kubepods/burstable/pod1234-5678/abc\n":        "pod:1234-5678",
		"0::/system.slice/docker-1122334455667788.scope\n": "docker:112233445566",
		"0::/user.slice/user-1000.slice\n":                 "",
	}
	for in, want := range cases {
		if got := ParseCgroup(in); got != want {
			t.Errorf("ParseCgroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiagnose(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "proc/sys/kernel/osrelease"), "6.10.14-linuxkit\n")
	writeFile(t, filepath.Join(root, "sys/kernel/btf/vmlinux"), "BTF")
	writeFile(t, filepath.Join(root, "proc/sys/kernel/unprivileged_bpf_disabled"), "0\n")
	writeFile(t, filepath.Join(root, "proc/sys/kernel/perf_event_paranoid"), "2\n")
	// CAP_BPF (39) and CAP_SYS_ADMIN (21) set.
	writeFile(t, filepath.Join(root, "proc/self/status"), "Name:\tx\nCapEff:\t000001ffffffffff\n")

	rep := Diagnose(root)
	if rep.Kernel != "6.10.14-linuxkit" {
		t.Errorf("kernel = %q", rep.Kernel)
	}
	if !rep.CanUprobes {
		t.Errorf("expected uprobes available, checks: %+v", rep.Checks)
	}
	if rep.CanProfile {
		t.Errorf("perf_event_paranoid=2 must not claim profiling is available")
	}
	if !strings.Contains(rep.Summary(), "uprobes available") {
		t.Errorf("summary = %q", rep.Summary())
	}
}

func TestDiagnoseMinimalCaps(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "proc/sys/kernel/osrelease"), "5.4.0\n")
	writeFile(t, filepath.Join(root, "proc/self/status"), "CapEff:\t00000000a80425fb\n")
	rep := Diagnose(root)
	if rep.CanUprobes {
		t.Error("default docker caps must not report uprobes available")
	}
	var btf Check
	for _, c := range rep.Checks {
		if c.Name == "kernel BTF" {
			btf = c
		}
	}
	if btf.Verdict != VerdictUnavailable {
		t.Errorf("missing BTF must be reported unavailable, got %q", btf.Verdict)
	}
}

func TestCapabilityMaskMasksTo64Bits(t *testing.T) {
	// Some kernels report more than 16 hex digits.
	if got := capabilityMask("CapEff:\t0000000000000000000001ffffffffff\n"); got == 0 {
		t.Fatal("expected non-zero mask")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
