// Package agent discovers what runs on a host and diagnoses whether
// instrumentation is possible. It is read-only: it inspects procfs/sysfs and
// never modifies the observed system.
package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// Socket is one listening TCP socket, from /proc/net/tcp{,6}.
type Socket struct {
	Proto string // tcp or tcp6
	IP    string
	Port  int
	Inode uint64
	// State is the procfs two-letter state; 0A is LISTEN.
	State string
}

// Service is a discovered process reachable via TCP.
type Service struct {
	PID     int
	Name    string
	Cmdline string
	// Container is "docker:<id>" / "pod:<uid>" / "" when the process runs
	// outside a container, derived from cgroups.
	Container string
	Ports     []Socket
}

// ParseCmdline turns a NUL-separated /proc/<pid>/cmdline into a readable line
// and strips arguments that look like credentials (prompt §15: never persist
// secrets, and an agent that logs them is a leak).
func ParseCmdline(b []byte) string {
	parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && looksSecret(p) {
			out = append(out, "[REDACTED]")
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, " ")
}

// looksSecret catches the common shapes: --password=x, --token x, KEY=value
// where the key name says it is a credential.
func looksSecret(arg string) bool {
	lower := strings.ToLower(arg)
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "api-key", "credential"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ParseTCP parses /proc/net/tcp or tcp6. Addresses are hex, little-endian per
// 32-bit word for IPv4.
func ParseTCP(proto string, content string) ([]Socket, error) {
	lines := strings.Split(content, "\n")
	var out []Socket
	for i, line := range lines {
		if i == 0 { // header
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// sl local_address rem_address st ... uid timeout inode
		if len(fields) < 10 {
			continue
		}
		ip, port, err := parseHexAddr(fields[1])
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, Socket{Proto: proto, IP: ip, Port: port, State: fields[3], Inode: inode})
	}
	return out, nil
}

// parseHexAddr decodes "0100007F:1F90" into ("127.0.0.1", 8080).
func parseHexAddr(s string) (string, int, error) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("bad port in %q: %w", s, err)
	}
	if len(host) == 8 { // IPv4: 4 bytes, each reversed
		var b [4]byte
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(host[i*2:i*2+2], 16, 8)
			if err != nil {
				return "", 0, fmt.Errorf("bad ip in %q: %w", s, err)
			}
			b[3-i] = byte(v)
		}
		return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3]), int(port), nil
	}
	// IPv6: 16 bytes in 4 words, each word byte-swapped.
	if len(host) == 32 {
		words := make([]string, 0, 8)
		for w := 0; w < 4; w++ {
			chunk := host[w*8 : w*8+8]
			words = append(words, chunk[6:8]+chunk[4:6]+chunk[2:4]+chunk[0:2])
		}
		joined := strings.Join(words, "")
		groups := make([]string, 0, 8)
		for i := 0; i < 8; i++ {
			groups = append(groups, strings.TrimLeft(joined[i*4:i*4+4], "0"))
		}
		for i, g := range groups {
			if g == "" {
				groups[i] = "0"
			}
		}
		return strings.Join(groups, ":"), int(port), nil
	}
	return "", 0, fmt.Errorf("unsupported address width in %q", s)
}

// ParseCgroup derives a container identity from /proc/<pid>/cgroup.
func ParseCgroup(content string) string {
	for _, line := range strings.Split(content, "\n") {
		_, path, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		_, path, ok = strings.Cut(path, ":")
		if !ok {
			continue
		}
		if id := containerIDFromPath(path); id != "" {
			return id
		}
	}
	return ""
}

// containerIDFromPath finds the runtime's identifier in a cgroup path.
//
// Observed layouts, all of which must work:
//
//	cgroup v1       11:devices:/docker/<id>
//	cgroup v2       0::/system.slice/docker-<id>.scope
//	cgroup v2       0::/docker/<id>
//	Docker Desktop  0::/../<64-hex-id>      <- measured, matches nothing else
//	Kubernetes      0::/kubepods/burstable/pod<uid>/<id>
func containerIDFromPath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if seg == "docker" || seg == "docker-0" {
			if i+1 < len(segments) {
				return "docker:" + shortID(segments[i+1])
			}
		}
		if seg == "kubepods" || seg == "kubepods.slice" {
			// The pod uid segment starts with pod<uid>.
			for _, s := range segments[i:] {
				if strings.HasPrefix(s, "pod") && len(s) > 3 {
					return "pod:" + shortID(strings.TrimPrefix(s, "pod"))
				}
			}
		}
		if strings.HasSuffix(seg, ".scope") && strings.Contains(seg, "docker-") {
			id := strings.TrimSuffix(strings.TrimPrefix(seg[strings.Index(seg, "docker-"):], "docker-"), ".scope")
			if id != "" {
				return "docker:" + shortID(id)
			}
		}
		// Last resort: a bare container ID segment (64 hex chars). Container
		// runtimes name these themselves, so report the runtime as unknown
		// rather than inventing "docker".
		if isBareContainerID(seg) {
			return "container:" + shortID(seg)
		}
	}
	return ""
}

// isBareContainerID recognises a 64-character lowercase hex segment.
func isBareContainerID(seg string) bool {
	if len(seg) != 64 {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func shortID(id string) string {
	const max = 12
	if len(id) > max {
		return id[:max]
	}
	return id
}

// ParseState reports whether this socket is listening (procfs state 0A).
func (s Socket) Listening() bool { return s.State == "0A" }
