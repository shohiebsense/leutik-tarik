// Package store persists normalized spans and answers operation queries.
package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Store persists records and answers operation queries. Memory is for tests and
// demos; ClickHouse is the production implementation.
type Store interface {
	Insert(ctx context.Context, records []model.Record) error
	// ListOperations returns root spans (an operation == the root of a trace),
	// newest first, filtered by q.
	ListOperations(ctx context.Context, q Query) ([]model.Span, error)
	// GetTrace returns every span of one trace ordered by start time.
	GetTrace(ctx context.Context, traceID string) ([]model.Span, error)
}

// Query is the searchable operation identity. Time is always expressible: it is
// the primary axis of every observability question (review M8).
type Query struct {
	Service string
	Route   string
	Method  string
	Outcome string

	From time.Time
	To   time.Time
	// Cursor returns operations starting strictly before it (keyset paging;
	// stable while new spans arrive).
	Cursor time.Time

	Limit int
}

// MaxLimit caps every result set.
const MaxLimit = 1000

func (q Query) limit() int {
	if q.Limit <= 0 || q.Limit > MaxLimit {
		return 100
	}
	return q.Limit
}

// Memory is an in-memory Store for tests and demos.
type Memory struct {
	mu      sync.RWMutex
	records []model.Record
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Insert(_ context.Context, records []model.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, records...)
	return nil
}

func (m *Memory) ListOperations(_ context.Context, q Query) ([]model.Span, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []model.Span
	for _, r := range m.records {
		s := r.Span
		if s.ParentSpanID != "" {
			continue
		}
		if q.Service != "" && s.ServiceName != q.Service {
			continue
		}
		if q.Route != "" && s.HTTPRoute != q.Route {
			continue
		}
		if q.Method != "" && s.HTTPMethod != q.Method {
			continue
		}
		if q.Outcome != "" && s.Outcome != q.Outcome {
			continue
		}
		if !q.From.IsZero() && s.StartTime.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && s.StartTime.After(q.To) {
			continue
		}
		if !q.Cursor.IsZero() && !s.StartTime.Before(q.Cursor) {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	if n := q.limit(); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (m *Memory) GetTrace(_ context.Context, traceID string) ([]model.Span, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]struct{})
	var out []model.Span
	for _, r := range m.records {
		if r.Span.TraceID != traceID {
			continue
		}
		// Mirror the ClickHouse dedup: one row per (trace_id, span_id).
		if _, dup := seen[r.Span.SpanID]; dup {
			continue
		}
		seen[r.Span.SpanID] = struct{}{}
		out = append(out, r.Span)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out, nil
}
