package store

import (
	"context"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// ServiceStat is the RED view of one service over a window.
type ServiceStat struct {
	Service  string
	Spans    int64
	Errors   int64
	P50      time.Duration
	P95      time.Duration
	LastSeen time.Time
	Routes   int64
}

// ErrorRate returns errors/spans, 0 when nothing was recorded.
func (s ServiceStat) ErrorRate() float64 {
	if s.Spans == 0 {
		return 0
	}
	return float64(s.Errors) / float64(s.Spans)
}

// ErrorGroup is one error fingerprint, grouped so "how often?" is answerable
// without reading individual traces.
type ErrorGroup struct {
	Service       string
	Route         string
	Outcome       string
	ErrorType     string
	StatusMessage string
	Count         int64
	Traces        int64
	FirstSeen     time.Time
	LastSeen      time.Time
}

// Stats is implemented alongside Store by the stores that can aggregate.
// Kept separate so a store may support spans without rollups.
type Stats interface {
	ServiceStats(ctx context.Context, from, to time.Time) ([]ServiceStat, error)
	ErrorGroups(ctx context.Context, from, to time.Time, limit int) ([]ErrorGroup, error)
	// EndpointStats ranks endpoints by total time: the "what do I tune first"
	// list. Ordered by Total descending.
	EndpointStats(ctx context.Context, from, to time.Time, limit int) ([]EndpointStat, error)
	// HTTPFailures lists failed responses, split 4xx/5xx by Class.
	HTTPFailures(ctx context.Context, service string, from, to time.Time, limit int) ([]HTTPFailure, error)
}

// ProfileMeta is one stored profile artefact.
type ProfileMeta struct {
	ID         string
	Service    string
	Type       string
	StartedAt  time.Time
	Bytes      int64
	Path       string
	IngestedAt time.Time
	Retention  time.Duration
}

// Profiles stores profile artefacts and their metadata.
type Profiles interface {
	InsertProfile(ctx context.Context, meta ProfileMeta, data []byte) error
	ListProfiles(ctx context.Context, limit int) ([]ProfileMeta, error)
	GetProfile(ctx context.Context, id string) (ProfileMeta, []byte, error)
}

// dedup wraps a query so ReplacingMergeTree duplicates are counted once.
// Concrete stores embed the equivalent SQL; this documents the intent.
const dedupNote = "count each (trace_id, span_id) once"

var _ = dedupNote
var _ = model.OutcomeError

// EndpointStat ranks one endpoint by where time is spent.
//
// Total is what "which endpoint do I tune first" actually needs: an endpoint
// with a 2s p95 hit 10 times costs more than one with 4s hit twice. Count is
// kept alongside so a single freak request is visible as a single freak request.
type EndpointStat struct {
	Service string
	Route   string
	Method  string
	Count   int64
	Errors  int64
	P50     time.Duration
	P95     time.Duration
	Avg     time.Duration
	Total   time.Duration
}

// ErrorRate returns errors/count.
func (e EndpointStat) ErrorRate() float64 {
	if e.Count == 0 {
		return 0
	}
	return float64(e.Errors) / float64(e.Count)
}

// TopFunctions ranks functions by self time inside one profile.
//
// Self time is the actionable number: it is time spent *in* the function, not
// in what it called. Sort by it and the top row is where to look.
type TopFunction struct {
	Name    string
	Self    int64
	Cum     int64
	SelfPct float64
}

// FunctionRanking is a profile's hot functions, already ordered.
type FunctionRanking struct {
	Functions []TopFunction
	Unit      string
	Total     int64
}

// HTTPFailure is one failed response, grouped by route and status code, so the
// 4xx and 5xx classes can be read separately: a bad request is the client's
// problem, a 5xx is ours.
type HTTPFailure struct {
	Service    string
	Route      string
	Method     string
	StatusCode int
	Class      string // "4xx" or "5xx"
	Count      int64
	LastSeen   time.Time
	// ExampleTrace points at one occurrence to open.
	ExampleTrace string
}
