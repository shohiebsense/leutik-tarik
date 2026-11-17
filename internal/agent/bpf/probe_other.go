//go:build !linux

// Package bpf loads and attaches eBPF probes. On non-Linux hosts it reports the
// platform as unsupported rather than pretending to work (build prompt §5.2).
package bpf

import "errors"

// ErrUnsupported is returned on platforms without eBPF.
var ErrUnsupported = errors.New("eBPF is only available on Linux")

// Event mirrors the Linux event layout.
type Event struct {
	PID  uint32
	TGID uint32
	TS   uint64
	Comm [16]byte
}

// Probe is a placeholder on non-Linux platforms.
type Probe struct{}

// Load always fails off Linux.
func Load() (*Probe, error) { return nil, ErrUnsupported }

// AttachUprobe always fails off Linux.
func (p *Probe) AttachUprobe(binaryPath, symbol string) error { return ErrUnsupported }

// Events returns a closed channel off Linux.
func (p *Probe) Events() <-chan Event {
	ch := make(chan Event)
	close(ch)
	return ch
}

// Err is always ErrUnsupported off Linux.
func (p *Probe) Err() error { return ErrUnsupported }

// Close is a no-op off Linux.
func (p *Probe) Close() error { return nil }

// CommString trims NUL padding.
func (e Event) CommString() string {
	for i, c := range e.Comm {
		if c == 0 {
			return string(e.Comm[:i])
		}
	}
	return string(e.Comm[:])
}
