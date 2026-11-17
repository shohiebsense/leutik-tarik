// Package policy decides retention per span at ingest time and can preview the
// effect of a rule set before it is activated.
//
// Retention is persisted per row (migration 002) because ClickHouse ORs TTL
// expressions, so a rule set evaluated later cannot safely express precedence
// (ADR 0006). The engine therefore answers one question per span: which rule
// matches, and how long may this row live?
package policy

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// Policy is one versioned rule set.
type Policy struct {
	Version  uint32 `yaml:"version"`
	Defaults Keep   `yaml:"defaults"`
	Rules    []Rule `yaml:"rules"`

	// Source is the document as written, kept so the UI can show what the
	// operator actually configured instead of a re-serialised struct.
	Source string `yaml:"-"`
}

// Rule keeps matching spans for Keep's durations.
type Rule struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority"`
	Match    Match  `yaml:"match"`
	Keep     Keep   `yaml:"keep"`
}

// Match selects spans. Every populated field must match; empty means "any".
type Match struct {
	Service       string   `yaml:"service"`
	Environment   string   `yaml:"environment"`
	HTTPMethod    []string `yaml:"http_method"`
	Route         []string `yaml:"route"`
	OperationType []string `yaml:"operation_type"`
	Outcome       []string `yaml:"outcome"`
	ErrorType     []string `yaml:"error_type"`
	// MinDuration selects slow spans (a latency bucket with one threshold;
	// more buckets would need explicit ranges the prompt does not require).
	MinDuration Duration `yaml:"min_duration"`
}

// Keep is the retention applied. Only the trace signal exists today; the other
// signals are listed in the prompt but have no storage yet, so they are not
// accepted here rather than silently ignored.
type Keep struct {
	Trace Duration `yaml:"trace"`
}

// Duration is a time.Duration that unmarshals from "90d", "14d", "2h", "30m".
type Duration time.Duration

// Duration returns the wrapped value.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses a go duration or a day-suffixed value like "30d".
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// ParseDuration accepts "2h", "30m", and day units ("3d", "30d") that
// time.ParseDuration does not.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "0" || s == "0s" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := time.ParseDuration(strings.TrimSuffix(s, "d") + "h")
		if err != nil {
			return 0, fmt.Errorf("bad duration %q: %w", s, err)
		}
		return days * 24, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q: %w", s, err)
	}
	return d, nil
}

// Load reads and validates a policy file.
func Load(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads, validates and normalises a policy document.
func Parse(b []byte) (*Policy, error) {
	var p Policy
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true) // a typo in a key must fail, not silently disable a rule
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	p.Source = string(b)
	return &p, nil
}

// Validate rejects a policy that cannot behave predictably.
func (p *Policy) Validate() error {
	var errs []error
	if p.Version == 0 {
		errs = append(errs, errors.New("version must be set (it is recorded with every row)"))
	}
	if p.Defaults.Trace.Duration() <= 0 {
		errs = append(errs, errors.New("defaults.trace must be > 0"))
	}
	seen := map[string]bool{}
	for i, r := range p.Rules {
		if strings.TrimSpace(r.Name) == "" {
			errs = append(errs, fmt.Errorf("rules[%d]: name is required", i))
		}
		if seen[r.Name] {
			errs = append(errs, fmt.Errorf("rules[%d]: duplicate name %q", i, r.Name))
		}
		seen[r.Name] = true
		if r.Keep.Trace.Duration() <= 0 {
			errs = append(errs, fmt.Errorf("rule %q: keep.trace must be > 0", r.Name))
		}
		if r.Match.empty() {
			errs = append(errs, fmt.Errorf("rule %q: match has no fields, so it would match everything — put that in defaults", r.Name))
		}
	}
	if err := p.validateAmbiguity(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateAmbiguity rejects rules that tie on priority and specificity: their
// order would then decide retention, which is not something a reader should
// have to derive from file order.
func (p *Policy) validateAmbiguity() error {
	type key struct {
		priority    int
		specificity int
	}
	seen := map[key]string{}
	for _, r := range p.Rules {
		k := key{r.Priority, r.Match.specificity()}
		if prev, ok := seen[k]; ok {
			return fmt.Errorf("rules %q and %q have the same priority (%d) and specificity (%d); set explicit priorities so precedence is unambiguous",
				prev, r.Name, r.Priority, k.specificity)
		}
		seen[k] = r.Name
	}
	return nil
}

// sorted returns rules in evaluation order: highest priority first, then most
// specific, then file order as a final deterministic tiebreak.
func (p *Policy) sorted() []Rule {
	out := make([]Rule, len(p.Rules))
	copy(out, p.Rules)
	index := make(map[string]int, len(out))
	for i, r := range out {
		index[r.Name] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		si, sj := out[i].Match.specificity(), out[j].Match.specificity()
		if si != sj {
			return si > sj
		}
		return index[out[i].Name] < index[out[j].Name]
	})
	return out
}

// empty reports whether no selector is set.
func (m Match) empty() bool {
	return m.Service == "" && m.Environment == "" && len(m.HTTPMethod) == 0 &&
		len(m.Route) == 0 && len(m.OperationType) == 0 && len(m.Outcome) == 0 &&
		len(m.ErrorType) == 0 && m.MinDuration.Duration() == 0
}

// specificity counts populated selectors, used to break priority ties.
func (m Match) specificity() int {
	n := 0
	if m.Service != "" {
		n++
	}
	if m.Environment != "" {
		n++
	}
	if len(m.HTTPMethod) > 0 {
		n++
	}
	if len(m.Route) > 0 {
		n++
	}
	if len(m.OperationType) > 0 {
		n++
	}
	if len(m.Outcome) > 0 {
		n++
	}
	if len(m.ErrorType) > 0 {
		n++
	}
	if m.MinDuration.Duration() > 0 {
		n++
	}
	return n
}

// matches reports whether the span satisfies every populated selector.
func (m Match) matches(s model.Span) bool {
	if m.Service != "" && !globMatch(m.Service, s.ServiceName) {
		return false
	}
	if m.Environment != "" && !globMatch(m.Environment, s.Environment) {
		return false
	}
	if len(m.HTTPMethod) > 0 && !anyOf(m.HTTPMethod, s.HTTPMethod, false) {
		return false
	}
	if len(m.Route) > 0 && !anyOf(m.Route, s.HTTPRoute, true) {
		return false
	}
	if len(m.OperationType) > 0 && !anyOf(m.OperationType, s.OperationType, false) {
		return false
	}
	if len(m.Outcome) > 0 && !anyOf(m.Outcome, s.Outcome, false) {
		return false
	}
	if len(m.ErrorType) > 0 && !anyOf(m.ErrorType, s.ErrorType, false) {
		return false
	}
	if min := m.MinDuration.Duration(); min > 0 && s.Duration < min {
		return false
	}
	return true
}

// anyOf is case-insensitive for enums and glob-aware for paths.
func anyOf(patterns []string, value string, glob bool) bool {
	for _, p := range patterns {
		if glob {
			if globMatch(p, value) {
				return true
			}
			continue
		}
		if strings.EqualFold(p, value) {
			return true
		}
	}
	return false
}

// DescribeMatch renders a rule's selectors in one human-readable line, so the
// policies page says what a rule actually selects instead of pointing at the
// file. Shared by the engine's preview and the UI.
func DescribeMatch(m Match) string {
	var parts []string
	if m.Service != "" {
		parts = append(parts, "service "+m.Service)
	}
	if m.Environment != "" {
		parts = append(parts, "env "+m.Environment)
	}
	if len(m.HTTPMethod) > 0 {
		parts = append(parts, strings.Join(m.HTTPMethod, "/"))
	}
	if len(m.Route) > 0 {
		parts = append(parts, "route "+strings.Join(m.Route, " | "))
	}
	if len(m.OperationType) > 0 {
		parts = append(parts, "type "+strings.Join(m.OperationType, "/"))
	}
	if len(m.Outcome) > 0 {
		parts = append(parts, strings.Join(m.Outcome, "/"))
	}
	if len(m.ErrorType) > 0 {
		parts = append(parts, "err "+strings.Join(m.ErrorType, "/"))
	}
	if min := m.MinDuration.Duration(); min > 0 {
		parts = append(parts, ">= "+HumanDuration(min))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

// HumanDuration renders a retention the way an operator writes it: "14d", not
// "336h0m0s".
func HumanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	default:
		return d.String()
	}
}
