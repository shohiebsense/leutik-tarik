// Package logsnap keeps a bounded in-memory ring of recent log events and, when
// a trigger fires, snapshots only the window around it.
//
// It never stores a full log stream: the ring overwrites, and a durable row is
// written only because something went wrong (build prompt §8.1/§8.2).
package logsnap

import (
	"sync"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Limits are the hard bounds. Zero fields fall back to the defaults below.
type Limits struct {
	MaxEvents int // per service
	MaxBytes  int // per service, measured on body length
	MaxAge    time.Duration
}

// Defaults chosen to stay small: a few thousand lines is plenty of context, and
// the whole structure is bounded regardless of traffic.
const (
	DefaultMaxEvents = 2000
	DefaultMaxBytes  = 4 << 20
	DefaultMaxAge    = 30 * time.Minute
)

func (l Limits) withDefaults() Limits {
	if l.MaxEvents <= 0 {
		l.MaxEvents = DefaultMaxEvents
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxAge <= 0 {
		l.MaxAge = DefaultMaxAge
	}
	return l
}

// Ring keeps the most recent events per service, bounded three ways: count,
// bytes and age. Evictions are counted, never hidden.
type Ring struct {
	mu     sync.Mutex
	limits Limits
	bySvc  map[string]*serviceRing
	now    func() time.Time
	// Stats, for the System view and for honest accounting.
	Accepted, Evicted int64
}

type serviceRing struct {
	events []model.LogEvent
	bytes  int
}

// NewRing returns a ring with the given limits.
func NewRing(l Limits) *Ring {
	return &Ring{limits: l.withDefaults(), bySvc: map[string]*serviceRing{}, now: time.Now}
}

// Add appends an event, evicting from the front as needed.
func (r *Ring) Add(e model.LogEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	svc := e.Service
	sr := r.bySvc[svc]
	if sr == nil {
		sr = &serviceRing{}
		r.bySvc[svc] = sr
	}

	// A single oversized line must not evict everything else.
	if max := r.limits.MaxBytes / 8; len(e.Body) > max {
		e.Body = e.Body[:max] + "…[truncated]"
	}

	sr.events = append(sr.events, e)
	sr.bytes += len(e.Body)
	r.Accepted++

	cutoff := r.now().Add(-r.limits.MaxAge)
	for len(sr.events) > 0 && (len(sr.events) > r.limits.MaxEvents ||
		sr.bytes > r.limits.MaxBytes ||
		sr.events[0].Time.Before(cutoff)) {
		r.evictLocked(sr)
	}
}

func (r *Ring) evictLocked(sr *serviceRing) {
	if len(sr.events) == 0 {
		return
	}
	sr.bytes -= len(sr.events[0].Body)
	sr.events = sr.events[1:]
	r.Evicted++
}

// Snapshot returns one service's events inside [from, to], newest last. Events
// carrying the trace id are marked exact; everything else is a correlation and
// says so.
func (r *Ring) Snapshot(service, traceID string, from, to time.Time, maxEvents int) []model.LogEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	if maxEvents <= 0 {
		maxEvents = 200
	}
	sr := r.bySvc[service]
	if sr == nil {
		return nil
	}

	var out []model.LogEvent
	for _, e := range sr.events {
		if e.Time.Before(from) || e.Time.After(to) {
			continue
		}
		// An event that names a different trace must never be mixed in.
		if traceID != "" && e.TraceID != "" && e.TraceID != traceID {
			continue
		}
		e.Exact = traceID != "" && e.TraceID == traceID
		out = append(out, e)
	}
	if len(out) > maxEvents {
		out = out[len(out)-maxEvents:] // keep the lines nearest the trigger
	}
	return out
}

// Services lists the services currently buffered, for diagnostics.
func (r *Ring) Services() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.bySvc))
	for k := range r.bySvc {
		out = append(out, k)
	}
	return out
}

// Stats returns the accounting counters.
func (r *Ring) Stats() (accepted, evicted int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Accepted, r.Evicted
}
