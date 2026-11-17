package policy

import (
	"fmt"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Decision is the engine's answer for one span.
type Decision struct {
	PolicyID      string
	PolicyVersion uint32
	Retention     time.Duration
	// Rule is the matched rule name, empty when the defaults applied.
	Rule string
}

// Engine evaluates a policy.
type Engine struct {
	policy *Policy
	rules  []Rule
}

// New returns an engine for a validated policy.
func New(p *Policy) *Engine {
	return &Engine{policy: p, rules: p.sorted()}
}

// Default returns an engine with a single catch-all rule, so callers without a
// configured policy still behave predictably instead of guessing.
func Default() *Engine {
	return New(&Policy{
		Version:  1,
		Defaults: Keep{Trace: Duration(14 * 24 * time.Hour)},
	})
}

// Policy exposes the underlying rule set (for the effective-config view).
func (e *Engine) Policy() *Policy { return e.policy }

// Evaluate returns the retention for a span. First match wins in
// (priority desc, specificity desc, file order) order.
func (e *Engine) Evaluate(s model.Span) Decision {
	for _, r := range e.rules {
		if r.Match.matches(s) {
			return Decision{
				PolicyID:      r.Name,
				PolicyVersion: e.policy.Version,
				Retention:     r.Keep.Trace.Duration(),
				Rule:          r.Name,
			}
		}
	}
	return Decision{
		PolicyID:      "default",
		PolicyVersion: e.policy.Version,
		Retention:     e.policy.Defaults.Trace.Duration(),
	}
}

// RulePreview is what one rule would do to the sampled traffic.
type RulePreview struct {
	Name     string
	Priority int
	// Match is the rule's selector rendered for display.
	Match       string
	Retention   time.Duration
	Matches     int
	BytesPerDay int64
	// ShadowedBy is set when a higher-precedence rule already claims every
	// span this rule would match, which means the rule cannot take effect.
	ShadowedBy string
}

// Preview evaluates the policy against recent spans and estimates storage.
//
// measuredBytesPerSpan is a constant from a real measurement (20.82 MiB for 2M
// spans, docs/review-2026-11-17.md), not a guess; the caller may override it.
func (e *Engine) Preview(spans []model.Span, spanWindow time.Duration, measuredBytesPerSpan int64) []RulePreview {
	out := make([]RulePreview, 0, len(e.rules))
	counts := map[string]int{}

	// who wins each span, so shadowing can be detected rather than guessed
	winners := map[string]map[string]int{} // rule -> shadowed rule -> count
	for _, s := range spans {
		d := e.Evaluate(s)
		counts[d.PolicyID]++
		if d.Rule != "" {
			if winners[d.Rule] == nil {
				winners[d.Rule] = map[string]int{}
			}
			for _, r := range e.rules {
				if r.Name != d.Rule && r.Match.matches(s) {
					if winners[d.Rule] == nil {
						winners[d.Rule] = map[string]int{}
					}
					winners[d.Rule][r.Name]++
				}
			}
		}
	}

	days := spanWindow.Hours() / 24
	if days <= 0 {
		days = 1
	}
	for _, r := range e.rules {
		matches := counts[r.Name]
		perDay := float64(matches) / days
		rp := RulePreview{
			Name:        r.Name,
			Priority:    r.Priority,
			Match:       DescribeMatch(r.Match),
			Retention:   r.Keep.Trace.Duration(),
			Matches:     matches,
			BytesPerDay: int64(perDay * float64(measuredBytesPerSpan)),
		}
		// Only a winner can be shadowed; a rule that never wins while another
		// always claims its spans is dead weight.
		if matches == 0 {
			for winner, shadows := range winners {
				if shadows[r.Name] > 0 {
					rp.ShadowedBy = fmt.Sprintf("%s (%d spans)", winner, shadows[r.Name])
					break
				}
			}
		}
		out = append(out, rp)
	}
	return out
}
