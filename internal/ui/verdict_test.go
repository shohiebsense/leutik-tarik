package ui

import (
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func ep(count, errors int64, p50, p95 time.Duration) store.EndpointStat {
	return store.EndpointStat{Service: "svc", Route: "/x", Method: "GET",
		Count: count, Errors: errors, P50: p50, P95: p95,
		Total: time.Duration(count) * p50}
}

// A single slow request must not raise a flag: that is the difference between
// statistics and anecdote.
func TestSlowOnceIsNotFlagged(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	v := cfg.Judge(ep(1, 0, 30*time.Second, 30*time.Second))
	if v.NeedsAttention {
		t.Errorf("one slow request was flagged: %+v", v)
	}
	if v.State != "low_sample" {
		t.Errorf("state = %q, want low_sample", v.State)
	}
}

func TestSlowMedianIsFlagged(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	v := cfg.Judge(ep(20, 0, 1200*time.Millisecond, 1500*time.Millisecond))
	if !v.NeedsAttention || v.State != "slow" {
		t.Errorf("median 1.2s over 20 hits not flagged: %+v", v)
	}
}

// A fast median with a bad p95 is a different problem and gets its own reason.
func TestSpikyIsFlaggedWithItsOwnReason(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	v := cfg.Judge(ep(50, 0, 200*time.Millisecond, 5*time.Second))
	if !v.NeedsAttention || v.State != "spiky" {
		t.Errorf("spiky endpoint not flagged: %+v", v)
	}
}

func TestHighErrorRateIsFlagged(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	v := cfg.Judge(ep(100, 10, 50*time.Millisecond, 100*time.Millisecond))
	if !v.NeedsAttention || v.State != "failing" {
		t.Errorf("10%% failures not flagged: %+v", v)
	}
}

func TestFastEndpointIsOk(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	v := cfg.Judge(ep(100, 0, 80*time.Millisecond, 300*time.Millisecond))
	if v.NeedsAttention || v.State != "ok" {
		t.Errorf("fast endpoint flagged: %+v", v)
	}
}

// Flags come out worst-median first, so the top row is the one to open.
func TestFlagOrdersByMedian(t *testing.T) {
	cfg := VerdictConfig{SlowMedian: time.Second, SlowP95: 2 * time.Second, MinSamples: 5}
	a := ep(10, 0, 2*time.Second, 3*time.Second)
	a.Route = "/a"
	b := ep(10, 0, 1200*time.Millisecond, 1500*time.Millisecond)
	b.Route = "/b"
	flagged := cfg.Flag([]store.EndpointStat{b, a})
	if len(flagged) != 2 || flagged[0].Endpoint.Route != "/a" {
		t.Fatalf("order = %+v", flagged)
	}
}
