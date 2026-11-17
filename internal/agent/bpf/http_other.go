//go:build !linux

package bpf

import (
	"fmt"
	"time"
)

// HTTPEvent mirrors the Linux HTTP event.
type HTTPEvent struct {
	PID    uint32
	TGID   uint32
	TS     uint64
	DurNS  uint64
	Status uint32
	Comm   [16]byte
}

// Duration returns the observed request duration.
func (e HTTPEvent) Duration() time.Duration { return time.Duration(e.DurNS) }

// CommString trims NUL padding.
func (e HTTPEvent) CommString() string {
	for i, c := range e.Comm {
		if c == 0 {
			return string(e.Comm[:i])
		}
	}
	return string(e.Comm[:])
}

// Symbols the HTTP probe attaches to.
const (
	SymbolServeHTTP   = "net/http.serverHandler.ServeHTTP"
	SymbolWriteHeader = "net/http.(*response).WriteHeader"
)

// HTTPProbe is a placeholder off Linux.
type HTTPProbe struct{}

// LoadHTTP always fails off Linux.
func LoadHTTP() (*HTTPProbe, error) { return nil, fmt.Errorf("eBPF is only available on Linux") }

// Attach always fails off Linux.
func (p *HTTPProbe) Attach(binaryPath string) error {
	return fmt.Errorf("eBPF is only available on Linux")
}

// Events returns a closed channel off Linux.
func (p *HTTPProbe) Events() <-chan HTTPEvent {
	ch := make(chan HTTPEvent)
	close(ch)
	return ch
}

// Err reports the platform limitation off Linux.
func (p *HTTPProbe) Err() error { return fmt.Errorf("eBPF is only available on Linux") }

// Close is a no-op off Linux.
func (p *HTTPProbe) Close() error { return nil }
