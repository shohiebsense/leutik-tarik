// Package store persists normalized spans and answers operation queries.
package store

import (
	"context"
	"sort"
	"sync"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Store persists spans and answers operation queries. A ClickHouse
// implementation satisfies the same interface; Memory is for tests and demos.
type Store interface {
	InsertSpans(ctx context.Context, spans []model.Span) error
	// ListOperations returns root spans (an operation == the root of a trace),
	// newest first, filtered by q.
	ListOperations(ctx context.Context, q Query) ([]model.Span, error)
	// GetTrace returns every span of one trace ordered by start time.
	GetTrace(ctx context.Context, traceID string) ([]model.Span, error)
}

// Query is a subset of the searchable operation identity.
type Query struct {
	Service string
	Route   string
	Method  string
	Outcome string
	Limit   int
}

// Memory is an in-memory Store for tests and demos.
type Memory struct {
	mu    sync.RWMutex
	spans []model.Span
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{} }

func (m *Memory) InsertSpans(_ context.Context, spans []model.Span) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, spans...)
	return nil
}

func (m *Memory) ListOperations(_ context.Context, q Query) ([]model.Span, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 100
	}
	var out []model.Span
	for _, s := range m.spans {
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
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (m *Memory) GetTrace(_ context.Context, traceID string) ([]model.Span, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []model.Span
	for _, s := range m.spans {
		if s.TraceID == traceID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out, nil
}
