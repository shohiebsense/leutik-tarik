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
