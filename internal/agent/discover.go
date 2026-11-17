package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrUnsupportedPlatform is returned when procfs is absent (e.g. on macOS).
var ErrUnsupportedPlatform = errors.New("procfs not available: this platform is unsupported for discovery")

// Discover lists processes that hold listening TCP sockets. root is usually
// "/proc"; it is injectable so the whole thing is testable from fixtures.
//
// It reads only: /proc/net/tcp{,6}, /proc/<pid>/fd/*, comm, cmdline, cgroup.
func Discover(root string) ([]Service, error) {
	if _, err := os.Stat(filepath.Join(root, "net", "tcp")); err != nil {
		if _, err2 := os.Stat(filepath.Join(root, "net", "tcp6")); err2 != nil {
			return nil, ErrUnsupportedPlatform
		}
	}

	inodeToSocket, err := listeningSockets(root)
	if err != nil {
		return nil, err
	}
	if len(inodeToSocket) == 0 {
		return nil, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	var services []Service
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process directory
		}
		svc := Service{PID: pid}
		matched := false

		fdDir := filepath.Join(root, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // not ours to inspect; skip without noise
		}
		seen := make(map[uint64]struct{})
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			inode, ok := socketInode(link)
			if !ok {
				continue
			}
			sock, ok := inodeToSocket[inode]
			if !ok || !sock.Listening() {
				continue
			}
			if _, dup := seen[inode]; dup {
				continue
			}
			seen[inode] = struct{}{}
			svc.Ports = append(svc.Ports, sock)
			matched = true
		}
		if !matched {
			continue
		}

		svc.Name = readTrimmed(filepath.Join(root, e.Name(), "comm"))
		if cmd, err := os.ReadFile(filepath.Join(root, e.Name(), "cmdline")); err == nil {
			svc.Cmdline = ParseCmdline(cmd)
		}
		if cg, err := os.ReadFile(filepath.Join(root, e.Name(), "cgroup")); err == nil {
			svc.Container = ParseCgroup(string(cg))
		}
		sort.Slice(svc.Ports, func(i, j int) bool { return svc.Ports[i].Port < svc.Ports[j].Port })
		services = append(services, svc)
	}

	sort.Slice(services, func(i, j int) bool {
		if services[i].Ports[0].Port != services[j].Ports[0].Port {
			return services[i].Ports[0].Port < services[j].Ports[0].Port
		}
		return services[i].PID < services[j].PID
	})
	return services, nil
}

// listeningSockets indexes listening sockets by inode.
func listeningSockets(root string) (map[uint64]Socket, error) {
	out := make(map[uint64]Socket)
	for _, proto := range []string{"tcp", "tcp6"} {
		path := filepath.Join(root, "net", proto)
		content, err := os.ReadFile(path)
		if err != nil {
			continue // a missing stack (no IPv6) is not an error
		}
		socks, err := ParseTCP(proto, string(content))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, s := range socks {
			if s.Listening() {
				out[s.Inode] = s
			}
		}
	}
	return out, nil
}

// socketInode extracts the inode from a "socket:[12345]" fd link.
func socketInode(link string) (uint64, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, prefix), "]"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
