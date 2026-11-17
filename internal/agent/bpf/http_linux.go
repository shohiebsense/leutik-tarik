//go:build linux

package bpf

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// HTTPEvent is one completed request observed through uprobes.
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

// Symbols the HTTP probe attaches to. Exported so callers and docs agree.
const (
	SymbolServeHTTP   = "net/http.serverHandler.ServeHTTP"
	SymbolWriteHeader = "net/http.(*response).WriteHeader"
)

// HTTPProbe observes request status and duration without touching the app.
type HTTPProbe struct {
	coll   *ebpf.Collection
	reader *ringbuf.Reader
	links  []link.Link
	events chan HTTPEvent
	once   sync.Once
	err    error
}

// LoadHTTP loads the HTTP probe program.
func LoadHTTP() (*HTTPProbe, error) {
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(httpObject))
	if err != nil {
		return nil, fmt.Errorf("parse http object: %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load http collection: %w", err)
	}
	rd, err := ringbuf.NewReader(coll.Maps["http_events"])
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("open http ring buffer: %w", err)
	}
	return &HTTPProbe{coll: coll, reader: rd}, nil
}

// Attach attaches both uprobes to a binary.
func (p *HTTPProbe) Attach(binaryPath string) error {
	ex, err := link.OpenExecutable(binaryPath)
	if err != nil {
		return fmt.Errorf("open executable %s: %w", binaryPath, err)
	}
	for symbol, prog := range map[string]string{
		SymbolServeHTTP:   "on_serve",
		SymbolWriteHeader: "on_write_header",
	} {
		pr, ok := p.coll.Programs[prog]
		if !ok {
			return fmt.Errorf("program %s missing", prog)
		}
		l, err := ex.Uprobe(symbol, pr, nil)
		if err != nil {
			return fmt.Errorf("attach %s: %w", symbol, err)
		}
		p.links = append(p.links, l)
	}
	return nil
}

// Events streams completed requests.
func (p *HTTPProbe) Events() <-chan HTTPEvent {
	if p.events != nil {
		return p.events
	}
	p.events = make(chan HTTPEvent, 64)
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
			var e HTTPEvent
			e.PID = le32(rec.RawSample[0:])
			e.TGID = le32(rec.RawSample[4:])
			e.TS = le64(rec.RawSample[8:])
			e.DurNS = le64(rec.RawSample[16:])
			e.Status = le32(rec.RawSample[24:])
			copy(e.Comm[:], rec.RawSample[32:48])
			select {
			case p.events <- e:
			default:
			}
		}
	}()
	return p.events
}

// Err reports the reader error after Events closes.
func (p *HTTPProbe) Err() error { return p.err }

// Close releases everything.
func (p *HTTPProbe) Close() error {
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
