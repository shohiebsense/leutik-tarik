package logsnap

import (
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

func base() time.Time { return time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC) }

// newRing pins the ring's clock to the fixture time so age eviction is only
// exercised by the tests that mean to test it.
func newRing(l Limits) *Ring {
	r := NewRing(l)
	r.now = func() time.Time { return base().Add(time.Second) }
	return r
}

func ev(svc, traceID, body string, at time.Time) model.LogEvent {
	return model.LogEvent{Service: svc, TraceID: traceID, Body: body, Time: at, Severity: "error"}
}

// The whole point: the buffer is bounded no matter how much traffic arrives.
func TestRingEvictsByCount(t *testing.T) {
	r := newRing(Limits{MaxEvents: 3})
	for i := 0; i < 10; i++ {
		r.Add(ev("svc", "", "line", base().Add(time.Duration(i)*time.Second)))
	}
	got := r.Snapshot("svc", "", base().Add(-time.Hour), base().Add(time.Hour), 100)
	if len(got) != 3 {
		t.Fatalf("want 3 events retained, got %d", len(got))
	}
	// Newest three survive.
	if got[2].Time != base().Add(9*time.Second) {
		t.Errorf("newest event wrong: %v", got[2].Time)
	}
	accepted, evicted := r.Stats()
	if accepted != 10 || evicted != 7 {
		t.Errorf("stats = %d accepted / %d evicted", accepted, evicted)
	}
}

func TestRingEvictsByBytes(t *testing.T) {
	r := newRing(Limits{MaxEvents: 1000, MaxBytes: 100})
	for i := 0; i < 20; i++ {
		r.Add(ev("svc", "", "0123456789", base()))
	}
	got := r.Snapshot("svc", "", base().Add(-time.Hour), base().Add(time.Hour), 1000)
	if len(got) > 10 {
		t.Errorf("byte bound not enforced: %d events", len(got))
	}
}

// One huge line must not wipe the buffer.
func TestOversizedLineIsTruncated(t *testing.T) {
	r := newRing(Limits{MaxEvents: 100, MaxBytes: 1000})
	r.Add(ev("svc", "", "small", base()))
	r.Add(ev("svc", "", string(make([]byte, 5000)), base()))
	got := r.Snapshot("svc", "", base().Add(-time.Hour), base().Add(time.Hour), 100)
	if len(got) != 2 {
		t.Fatalf("want both events kept, got %d", len(got))
	}
	if len(got[1].Body) > 1000 {
		t.Errorf("oversized body not truncated: %d bytes", len(got[1].Body))
	}
}

func TestRingEvictsByAge(t *testing.T) {
	now := base()
	r := NewRing(Limits{MaxAge: time.Minute})
	r.now = func() time.Time { return now }
	r.Add(ev("svc", "", "old", base()))
	now = base().Add(2 * time.Minute)
	r.Add(ev("svc", "", "new", now))
	got := r.Snapshot("svc", "", base().Add(-time.Hour), base().Add(time.Hour), 100)
	if len(got) != 1 || got[0].Body != "new" {
		t.Fatalf("age bound not enforced: %+v", got)
	}
}

// Exact vs correlated is the honesty bit: a line with the trace id is evidence,
// a line without it is only a guess by time.
func TestSnapshotLabelsExactAndCorrelated(t *testing.T) {
	r := newRing(Limits{})
	at := base()
	r.Add(ev("svc", "trace-1", "with trace id", at))
	r.Add(ev("svc", "", "no trace id", at))
	r.Add(ev("svc", "trace-2", "other trace", at))

	got := r.Snapshot("svc", "trace-1", at.Add(-time.Minute), at.Add(time.Minute), 100)
	if len(got) != 2 {
		t.Fatalf("want 2 events (trace-1 + correlated), got %d", len(got))
	}
	if !got[0].Exact {
		t.Error("event carrying the trace id must be exact")
	}
	if got[1].Exact {
		t.Error("event without a trace id must not be exact")
	}
	for _, e := range got {
		if e.TraceID == "trace-2" {
			t.Error("a different trace leaked into the snapshot")
		}
	}
}

func TestSnapshotRespectsWindow(t *testing.T) {
	r := newRing(Limits{})
	r.Add(ev("svc", "t", "before", base().Add(-time.Minute)))
	r.Add(ev("svc", "t", "inside", base()))
	r.Add(ev("svc", "t", "after", base().Add(time.Minute)))
	got := r.Snapshot("svc", "t", base().Add(-10*time.Second), base().Add(10*time.Second), 100)
	if len(got) != 1 || got[0].Body != "inside" {
		t.Errorf("window not respected: %+v", got)
	}
}

func TestTriggerFires(t *testing.T) {
	tr := DefaultTrigger()
	cases := []struct {
		span   model.Span
		fires  bool
		reason string
	}{
		{model.Span{Outcome: model.OutcomeServerError}, true, "server_error"},
		{model.Span{Outcome: model.OutcomeError}, true, "error"},
		{model.Span{Outcome: model.OutcomeSuccess, Duration: 3 * time.Second}, true, "slow"},
		{model.Span{Outcome: model.OutcomeSuccess, Duration: 10 * time.Millisecond}, false, ""},
		{model.Span{Outcome: model.OutcomeClientError, Duration: time.Millisecond}, false, ""},
	}
	for _, c := range cases {
		fires, reason := tr.Fires(c.span)
		if fires != c.fires || reason != c.reason {
			t.Errorf("Fires(%+v) = %v/%q, want %v/%q", c.span, fires, reason, c.fires, c.reason)
		}
	}
}

func TestTriggerWindow(t *testing.T) {
	tr := DefaultTrigger()
	start := base()
	s := model.Span{StartTime: start, EndTime: start.Add(2 * time.Second), Duration: 2 * time.Second}
	from, to := tr.Window(s)
	if from != start.Add(-30*time.Second) {
		t.Errorf("from = %v", from)
	}
	if to != start.Add(12*time.Second) {
		t.Errorf("to = %v", to)
	}
}

func TestSeverityRanking(t *testing.T) {
	if !model.SeverityAtLeast("ERROR", "warn") {
		t.Error("error should be at least warn")
	}
	if model.SeverityAtLeast("info", "error") {
		t.Error("info must not pass an error threshold")
	}
}
