//go:build !linux

package bpf

import "fmt"

// ArgEvent mirrors the Linux argument-register event.
type ArgEvent struct {
	PID  uint32
	TGID uint32
	TS   uint64
	Arg0 uint64
	Arg1 uint64
	Arg2 uint64
	Comm [16]byte
}

// ArgsProbe is a placeholder off Linux.
type ArgsProbe struct{}

// LoadArgs always fails off Linux.
func LoadArgs() (*ArgsProbe, error) { return nil, fmt.Errorf("eBPF is only available on Linux") }

// AttachUprobe always fails off Linux.
func (p *ArgsProbe) AttachUprobe(binaryPath, symbol string) error {
	return fmt.Errorf("eBPF is only available on Linux")
}

// Events returns a closed channel off Linux.
func (p *ArgsProbe) Events() <-chan ArgEvent {
	ch := make(chan ArgEvent)
	close(ch)
	return ch
}

// Err reports the platform limitation off Linux.
func (p *ArgsProbe) Err() error { return fmt.Errorf("eBPF is only available on Linux") }

// Close is a no-op off Linux.
func (p *ArgsProbe) Close() error { return nil }

// CommString trims NUL padding.
func (e ArgEvent) CommString() string {
	for i, c := range e.Comm {
		if c == 0 {
			return string(e.Comm[:i])
		}
	}
	return string(e.Comm[:])
}
