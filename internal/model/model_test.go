package model

import (
	"testing"
	"time"
)

func span(name, outcome string, dur time.Duration, svc string) Span {
	start := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	return Span{
		TraceID: "t1", Name: name, Outcome: outcome, ServiceName: svc,
		StartTime: start, EndTime: start.Add(dur), Duration: dur,
	}
}

// The case that motivated the summary: a 200 root must not hide a failed child.
func TestSummarizeReportsWorstOutcome(t *testing.T) {
	spans := []Span{
		span("POST /v1/payments/{id}", OutcomeSuccess, 2*time.Second, "gateway"),
		span("db.query SELECT", OutcomeError, 1800*time.Millisecond, "payment"),
	}
	s := Summarize(spans)
	if s.Outcome != OutcomeError {
		t.Errorf("outcome = %q, want %q", s.Outcome, OutcomeError)
	}
	if s.ErrorSpans != 1 {
		t.Errorf("error spans = %d, want 1", s.ErrorSpans)
	}
	if s.Slowest.Name != "POST /v1/payments/{id}" {
		t.Errorf("slowest = %q", s.Slowest.Name)
	}
	if len(s.Services) != 2 || s.Services[0] != "gateway" || s.Services[1] != "payment" {
		t.Errorf("services = %v", s.Services)
	}
	if s.Duration != 2*time.Second {
		t.Errorf("duration = %v", s.Duration)
	}
}

func TestSummarizeAllSuccess(t *testing.T) {
	s := Summarize([]Span{span("GET /x", OutcomeSuccess, 5*time.Millisecond, "svc")})
	if s.Outcome != OutcomeSuccess || s.ErrorSpans != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if s := Summarize(nil); s.Outcome != OutcomeUnknown || s.Spans != 0 {
		t.Errorf("summary = %+v", s)
	}
}
