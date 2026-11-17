package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// VerdictConfig decides when an endpoint is worth a human's attention.
//
// This is the whole point of the aggregation: requests are counted, the median
// and p95 are computed across all of them, and a rule turns that into a
// sentence. No upload, no manual step — the numbers arrive from normal traffic.
type VerdictConfig struct {
	// SlowMedian flags an endpoint whose typical request is slow. Median, not
	// mean: one 30s outlier should not condemn a fast endpoint.
	SlowMedian time.Duration
	// SlowP95 flags a spiky endpoint whose worst common case is far slower.
	SlowP95 time.Duration
	// MinSamples is the number of hits needed before a verdict is given. Below
	// it the honest answer is "too few samples", not a guess.
	MinSamples int64
}

// VerdictConfigFromEnv reads the thresholds, with defaults that match how people
// actually talk about this: "over a second is worth a look".
func VerdictConfigFromEnv() VerdictConfig {
	cfg := VerdictConfig{
		SlowMedian: 1 * time.Second,
		SlowP95:    2 * time.Second,
		MinSamples: 5,
	}
	if v := os.Getenv("LEUTIK_SLOW_MEDIAN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SlowMedian = d
		}
	}
	if v := os.Getenv("LEUTIK_SLOW_P95"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SlowP95 = d
		}
	}
	if v := os.Getenv("LEUTIK_MIN_SAMPLES"); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			cfg.MinSamples = n
		}
	}
	return cfg
}

// Verdict is the sentence shown to the user, plus the machine-readable state.
type Verdict struct {
	State  string // "slow", "spiky", "low_sample", "ok"
	Label  string
	Reason string
	// NeedsAttention is what the page sorts and counts by.
	NeedsAttention bool
}

// Judge turns one endpoint's statistics into a verdict.
func (c VerdictConfig) Judge(e store.EndpointStat) Verdict {
	if e.Count < c.MinSamples {
		return Verdict{
			State: "low_sample",
			Label: "too few hits",
			Reason: fmt.Sprintf("%d hit(s); %d needed before judging",
				e.Count, c.MinSamples),
		}
	}
	if e.P50 > c.SlowMedian {
		return Verdict{
			State:          "slow",
			Label:          "worth checking",
			Reason:         fmt.Sprintf("median %s over %d hits (> %s)", shortDur(e.P50), e.Count, shortDur(c.SlowMedian)),
			NeedsAttention: true,
		}
	}
	if e.P95 > c.SlowP95 {
		return Verdict{
			State:          "spiky",
			Label:          "worth checking",
			Reason:         fmt.Sprintf("median fine (%s) but p95 %s over %d hits (> %s)", shortDur(e.P50), shortDur(e.P95), e.Count, shortDur(c.SlowP95)),
			NeedsAttention: true,
		}
	}
	if e.Errors > 0 && e.ErrorRate() >= 0.05 {
		return Verdict{
			State:          "failing",
			Label:          "worth checking",
			Reason:         fmt.Sprintf("%d of %d failed (%.0f%%)", e.Errors, e.Count, e.ErrorRate()*100),
			NeedsAttention: true,
		}
	}
	return Verdict{
		State:  "ok",
		Label:  "ok",
		Reason: fmt.Sprintf("median %s, p95 %s over %d hits", shortDur(e.P50), shortDur(e.P95), e.Count),
	}
}

// shortDur renders a latency the way it is said out loud.
func shortDur(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(10 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// Flagged pairs endpoints with their verdict, keeping only the ones needing
// attention, worst first.
type Flagged struct {
	Endpoint store.EndpointStat
	Verdict  Verdict
}

func (c VerdictConfig) Flag(endpoints []store.EndpointStat) []Flagged {
	var out []Flagged
	for _, e := range endpoints {
		v := c.Judge(e)
		if v.NeedsAttention {
			out = append(out, Flagged{Endpoint: e, Verdict: v})
		}
	}
	// Worst median first: the endpoint whose *typical* request is slowest.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Endpoint.P50 > out[i].Endpoint.P50 {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
