package logsnap

import (
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Trigger decides whether a finished span is worth a log snapshot.
//
// Two conditions by default, both cheap and unambiguous: an error outcome, or a
// span slower than the threshold. Every extra trigger multiplies storage, so
// this list stays short and configurable rather than clever.
type Trigger struct {
	// SlowThreshold marks a span worth investigating. Zero disables it.
	SlowThreshold time.Duration
	// Before/After bound the snapshot window around the span.
	Before time.Duration
	After  time.Duration
	// MaxEvents caps one snapshot.
	MaxEvents int
}

// DefaultTrigger captures a short window around error and slow spans.
func DefaultTrigger() Trigger {
	return Trigger{
		SlowThreshold: 2 * time.Second,
		Before:        30 * time.Second,
		After:         10 * time.Second,
		MaxEvents:     200,
	}
}

// Fires reports whether the span triggers a snapshot, and why.
func (t Trigger) Fires(s model.Span) (bool, string) {
	switch s.Outcome {
	case model.OutcomeError:
		return true, "error"
	case model.OutcomeServerError:
		return true, "server_error"
	}
	if t.SlowThreshold > 0 && s.Duration >= t.SlowThreshold {
		return true, "slow"
	}
	return false, ""
}

// Window returns the snapshot bounds for a span.
func (t Trigger) Window(s model.Span) (from, to time.Time) {
	end := s.EndTime
	if end.IsZero() {
		end = s.StartTime.Add(s.Duration)
	}
	return s.StartTime.Add(-t.Before), end.Add(t.After)
}
