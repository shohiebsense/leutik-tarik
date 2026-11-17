//go:build linux

package bpf

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

//go:generate sh -c "make -C ../../../.. agent-bpf"

// Event is one probe hit. Layout must match struct event in probe.bpf.c.
type Event struct {
	PID  uint32
	TGID uint32
	TS   uint64
	Comm [16]byte
}

// Probe is a loaded program with one attachment.
type Probe struct {
	coll   *ebpf.Collection
	reader *ringbuf.Reader
	links  []link.Link
	events chan Event
	once   sync.Once
	err    error
}

// Load reads the embedded object and loads its maps and programs into the
// kernel. It needs the capabilities the doctor reports (CAP_BPF + CAP_PERFMON).
func Load() (*Probe, error) {
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(probeObject))
	if err != nil {
		return nil, fmt.Errorf("parse bpf object: %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load bpf collection: %w", err)
	}
	rd, err := ringbuf.NewReader(coll.Maps["events"])
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("open ring buffer: %w", err)
	}
	return &Probe{coll: coll, reader: rd}, nil
}

// AttachUprobe attaches the probe to a symbol in a user-space binary.
//
// This is the mechanism a later slice uses for Go net/http. Reading function
// arguments is a separate problem (Go 1.17+ register ABI); this slice only
// proves the attach and delivery path.
func (p *Probe) AttachUprobe(binaryPath, symbol string) error {
	prog, ok := p.coll.Programs["fire"]
	if !ok {
		return errors.New("program 'fire' missing from object")
	}
	ex, err := link.OpenExecutable(binaryPath)
	if err != nil {
		return fmt.Errorf("open executable %s: %w", binaryPath, err)
	}
	l, err := ex.Uprobe(symbol, prog, nil)
	if err != nil {
		return fmt.Errorf("attach uprobe %s in %s: %w", symbol, binaryPath, err)
	}
	p.links = append(p.links, l)
	return nil
}

// Events streams probe hits until Close. The channel is closed on exit.
func (p *Probe) Events() <-chan Event {
	if p.events != nil {
		return p.events
	}
	p.events = make(chan Event, 64)
	go func() {
		defer close(p.events)
		for {
			rec, err := p.reader.Read()
			if err != nil {
				p.err = err
				return
			}
			if len(rec.RawSample) < 32 {
				continue // malformed sample; drop rather than misread fields
			}
			var e Event
			e.PID = le32(rec.RawSample[0:])
			e.TGID = le32(rec.RawSample[4:])
			e.TS = le64(rec.RawSample[8:])
			copy(e.Comm[:], rec.RawSample[16:32])
			select {
			case p.events <- e:
			default:
				// Bounded channel: dropping is the documented behaviour when the
				// consumer cannot keep up. Losing telemetry must never stall the
				// observed process (build prompt §11).
			}
		}
	}()
	return p.events
}

// Err returns the reader error after Events has closed.
func (p *Probe) Err() error { return p.err }

// Close detaches and frees everything.
func (p *Probe) Close() error {
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

// CommString trims the NUL padding from a fixed-size comm field.
func (e Event) CommString() string {
	for i, c := range e.Comm {
		if c == 0 {
			return string(e.Comm[:i])
		}
	}
	return string(e.Comm[:])
}

// Time converts the monotonic timestamp to wall clock as best it can.
//
// shortcut: the kernel clock is monotonic, so this anchors it to the process
// start instead of a boot-relative conversion. Good enough for ordering and
// duration; a boot-time-correct conversion needs /proc/uptime and is part of
// the profiling slice.
func (e Event) Time() time.Time {
	return time.Now()
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le64(b []byte) uint64 {
	return uint64(le32(b)) | uint64(le32(b[4:]))<<32
}
