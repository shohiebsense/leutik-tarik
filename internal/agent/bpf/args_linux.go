//go:build linux

package bpf

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// Converted to []byte at build time by go:embed below.

// ArgEvent is one function-entry hit with its argument registers.
type ArgEvent struct {
	PID  uint32
	TGID uint32
	TS   uint64
	Arg0 uint64 // Go ABI: AX (receiver, or first argument)
	Arg1 uint64 // BX
	Arg2 uint64 // CX
	Comm [16]byte
}

// ArgsProbe captures argument registers at a function entry.
type ArgsProbe struct {
	coll   *ebpf.Collection
	reader *ringbuf.Reader
	links  []link.Link
	events chan ArgEvent
	once   sync.Once
	err    error
}

// LoadArgs loads the argument-reading program.
func LoadArgs() (*ArgsProbe, error) {
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(argsObject))
	if err != nil {
		return nil, fmt.Errorf("parse args object: %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load args collection: %w", err)
	}
	rd, err := ringbuf.NewReader(coll.Maps["arg_events"])
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("open args ring buffer: %w", err)
	}
	return &ArgsProbe{coll: coll, reader: rd}, nil
}

// AttachUprobe attaches to a symbol.
func (p *ArgsProbe) AttachUprobe(binaryPath, symbol string) error {
	prog, ok := p.coll.Programs["args_probe"]
	if !ok {
		return fmt.Errorf("program args_probe missing")
	}
	ex, err := link.OpenExecutable(binaryPath)
	if err != nil {
		return fmt.Errorf("open executable %s: %w", binaryPath, err)
	}
	l, err := ex.Uprobe(symbol, prog, nil)
	if err != nil {
		return fmt.Errorf("attach uprobe %s: %w", symbol, err)
	}
	p.links = append(p.links, l)
	return nil
}

// Events streams captured argument sets.
func (p *ArgsProbe) Events() <-chan ArgEvent {
	if p.events != nil {
		return p.events
	}
	p.events = make(chan ArgEvent, 64)
	go func() {
		defer close(p.events)
		for {
			rec, err := p.reader.Read()
			if err != nil {
				p.err = err
				return
			}
			if len(rec.RawSample) < 48 {
				continue
			}
			var e ArgEvent
			e.PID = le32(rec.RawSample[0:])
			e.TGID = le32(rec.RawSample[4:])
			e.TS = le64(rec.RawSample[8:])
			e.Arg0 = le64(rec.RawSample[16:])
			e.Arg1 = le64(rec.RawSample[24:])
			e.Arg2 = le64(rec.RawSample[32:])
			copy(e.Comm[:], rec.RawSample[40:48])
			select {
			case p.events <- e:
			default:
			}
		}
	}()
	return p.events
}

// Err reports the reader error after Events closes.
func (p *ArgsProbe) Err() error { return p.err }

// Close releases everything.
func (p *ArgsProbe) Close() error {
	var first error
	p.once.Do(func() {
		for _, l := range p.links {
			if err := l.Close(); err != nil && first == nil {
				first = err
			}
		}
		if p.reader != nil {
			if err := p.reader.Close(); err != nil && first == nil {
				first = err
			}
		}
		if p.coll != nil {
			p.coll.Close()
		}
	})
	return first
}

// CommString trims NUL padding.
func (e ArgEvent) CommString() string {
	for i, c := range e.Comm {
		if c == 0 {
			return string(e.Comm[:i])
		}
	}
	return string(e.Comm[:])
}
