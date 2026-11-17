package store

import (
	"context"
	"sort"
	"time"
)

// ServiceStats aggregates from the in-memory records.
func (m *Memory) ServiceStats(_ context.Context, from, to time.Time) ([]ServiceStat, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type acc struct {
		spans, errors int64
		durations     []time.Duration
		last          time.Time
		routes        map[string]struct{}
	}
	by := map[string]*acc{}
	seen := map[string]struct{}{}
	for _, r := range m.records {
		s := r.Span
		if s.ParentSpanID != "" {
			continue // roots only, matching the ClickHouse query
		}
		if s.StartTime.Before(from) || s.StartTime.After(to) {
			continue
		}
		key := s.TraceID + "|" + s.SpanID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		a := by[s.ServiceName]
		if a == nil {
			a = &acc{routes: map[string]struct{}{}}
			by[s.ServiceName] = a
		}
		a.spans++
		if s.Outcome == "error" || s.Outcome == "server_error" {
			a.errors++
		}
		a.durations = append(a.durations, s.Duration)
		if s.StartTime.After(a.last) {
			a.last = s.StartTime
		}
		if s.HTTPRoute != "" {
			a.routes[s.HTTPRoute] = struct{}{}
		}
	}

	out := make([]ServiceStat, 0, len(by))
	for name, a := range by {
		sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
		out = append(out, ServiceStat{
			Service:  name,
			Spans:    a.spans,
			Errors:   a.errors,
			P50:      percentile(a.durations, 0.50),
			P95:      percentile(a.durations, 0.95),
			LastSeen: a.last,
			Routes:   int64(len(a.routes)),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].Spans > out[j].Spans
	})
	return out, nil
}

// ErrorGroups groups failures the same way the ClickHouse query does.
func (m *Memory) ErrorGroups(_ context.Context, from, to time.Time, limit int) ([]ErrorGroup, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	type key struct{ svc, route, outcome, errType, msg string }
	type acc struct {
		g     ErrorGroup
		trace map[string]struct{}
	}
	by := map[key]*acc{}
	seen := map[string]struct{}{}
	for _, r := range m.records {
		s := r.Span
		if s.StartTime.Before(from) || s.StartTime.After(to) {
			continue
		}
		if s.Outcome != "error" && s.Outcome != "server_error" && s.Outcome != "client_error" {
			continue
		}
		k := key{s.ServiceName, s.HTTPRoute, s.Outcome, s.ErrorType, s.StatusMessage}
		a := by[k]
		if a == nil {
			a = &acc{g: ErrorGroup{
				Service: k.svc, Route: k.route, Outcome: k.outcome,
				ErrorType: k.errType, StatusMessage: k.msg,
				FirstSeen: s.StartTime, LastSeen: s.StartTime,
			}, trace: map[string]struct{}{}}
			by[k] = a
		}
		a.g.Count++
		a.trace[s.TraceID] = struct{}{}
		if s.StartTime.Before(a.g.FirstSeen) {
			a.g.FirstSeen = s.StartTime
		}
		if s.StartTime.After(a.g.LastSeen) {
			a.g.LastSeen = s.StartTime
		}
		_ = seen
	}

	out := make([]ErrorGroup, 0, len(by))
	for _, a := range by {
		a.g.Traces = int64(len(a.trace))
		out = append(out, a.g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}
