package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

func mustParse(t *testing.T, doc string) *Policy {
	t.Helper()
	p, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

const examplePolicy = `
version: 2
defaults:
  trace: 14d
rules:
  - name: payment-write-errors
    priority: 100
    match:
      service: "payment*"
      http_method: [POST, PUT, PATCH]
      route: ["/v1/payments/**"]
      outcome: [error, server_error, client_error]
    keep:
      trace: 90d
  - name: all-errors
    priority: 90
    match:
      outcome: [error, server_error]
    keep:
      trace: 30d
  - name: slow-success
    priority: 50
    match:
      outcome: [success]
      min_duration: 2s
    keep:
      trace: 30d
  - name: read-success
    priority: 20
    match:
      http_method: [GET, HEAD]
      outcome: [success]
    keep:
      trace: 2d
  - name: db-operations
    priority: 10
    match:
      operation_type: [db]
    keep:
      trace: 7d
`

func sp(svc, method, route, outcome, opType string, dur time.Duration) model.Span {
	return model.Span{
		ServiceName: svc, HTTPMethod: method, HTTPRoute: route,
		Outcome: outcome, OperationType: opType, Duration: dur,
	}
}

func TestEvaluateExamplePolicy(t *testing.T) {
	eng := New(mustParse(t, examplePolicy))
	cases := []struct {
		name string
		span model.Span
		rule string
		keep time.Duration
	}{
		{"payment write 500", sp("payment-api", "POST", "/v1/payments/{id}", model.OutcomeServerError, "write", time.Second), "payment-write-errors", 90 * 24 * time.Hour},
		{"other service 500", sp("fare-engine", "POST", "/v1/calc", model.OutcomeServerError, "write", time.Second), "all-errors", 30 * 24 * time.Hour},
		{"GET success", sp("payment-api", "GET", "/v1/payments/{id}", model.OutcomeSuccess, "read", 10*time.Millisecond), "read-success", 2 * 24 * time.Hour},
		{"slow success", sp("payment-api", "POST", "/v1/payments/{id}", model.OutcomeSuccess, "write", 3*time.Second), "slow-success", 30 * 24 * time.Hour},
		{"db span", sp("payment-api", "", "", model.OutcomeUnknown, "db", time.Millisecond), "db-operations", 7 * 24 * time.Hour},
		{"unmatched", sp("other", "DELETE", "/x", model.OutcomeUnknown, "", time.Millisecond), "default", 14 * 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := eng.Evaluate(c.span)
			if d.Rule != c.rule && !(c.rule == "default" && d.PolicyID == "default") {
				t.Errorf("rule = %q (policy %q), want %q", d.Rule, d.PolicyID, c.rule)
			}
			if d.Retention != c.keep {
				t.Errorf("retention = %v, want %v", d.Retention, c.keep)
			}
			if d.PolicyVersion != 2 {
				t.Errorf("version = %d, want 2", d.PolicyVersion)
			}
		})
	}
}

func TestPriorityBeatsSpecificity(t *testing.T) {
	p := mustParse(t, `
version: 1
defaults: {trace: 1d}
rules:
  - name: broad-but-urgent
    priority: 100
    match: {outcome: [server_error]}
    keep: {trace: 90d}
  - name: narrow-and-lazy
    priority: 10
    match: {service: api, http_method: [POST], route: ["/v1/**"], outcome: [server_error]}
    keep: {trace: 1d}
`)
	eng := New(p)
	d := eng.Evaluate(sp("api", "POST", "/v1/x", model.OutcomeServerError, "", time.Second))
	if d.Rule != "broad-but-urgent" {
		t.Errorf("priority must win over specificity; got %q", d.Rule)
	}
}

func TestSpecificityBreaksPriorityTie(t *testing.T) {
	p := mustParse(t, `
version: 1
defaults: {trace: 1d}
rules:
  - name: general
    priority: 50
    match: {outcome: [server_error]}
    keep: {trace: 10d}
  - name: specific
    priority: 50
    match: {service: api, outcome: [server_error]}
    keep: {trace: 20d}
`)
	eng := New(p)
	if d := eng.Evaluate(sp("api", "", "", model.OutcomeServerError, "", time.Second)); d.Rule != "specific" {
		t.Errorf("specificity should break the tie; got %q", d.Rule)
	}
	if d := eng.Evaluate(sp("other", "", "", model.OutcomeServerError, "", time.Second)); d.Rule != "general" {
		t.Errorf("non-matching specific rule must fall through; got %q", d.Rule)
	}
}

func TestAmbiguousRulesAreRejected(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
defaults: {trace: 1d}
rules:
  - name: a
    priority: 50
    match: {outcome: [server_error]}
    keep: {trace: 10d}
  - name: b
    priority: 50
    match: {http_method: [POST]}
    keep: {trace: 20d}
`))
	if err == nil {
		t.Fatal("expected rejection of equal priority + specificity")
	}
	if !strings.Contains(err.Error(), "unambiguous") {
		t.Errorf("error should explain precedence: %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"no version":       "defaults: {trace: 1d}\nrules: []\n",
		"zero default":     "version: 1\ndefaults: {trace: 0s}\nrules: []\n",
		"empty match":      "version: 1\ndefaults: {trace: 1d}\nrules:\n  - name: x\n    match: {}\n    keep: {trace: 1d}\n",
		"zero keep":        "version: 1\ndefaults: {trace: 1d}\nrules:\n  - name: x\n    match: {outcome: [error]}\n    keep: {trace: 0s}\n",
		"duplicate name":   "version: 1\ndefaults: {trace: 1d}\nrules:\n  - name: x\n    match: {outcome: [error]}\n    keep: {trace: 1d}\n  - name: x\n    priority: 5\n    match: {outcome: [success]}\n    keep: {trace: 1d}\n",
		"unknown yaml key": "version: 1\ndefaults: {trace: 1d}\nrules: []\nretention: {}\n",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseDurationDays(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90d": 90 * 24 * time.Hour,
		"2h":  2 * time.Hour,
		"0":   0,
	} {
		got, err := ParseDuration(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("%s = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseDuration("banana"); err == nil {
		t.Error("expected error for junk duration")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"payment*", "payment-api", true},
		{"payment*", "fare-engine", false},
		{"/v1/payments/**", "/v1/payments/{id}", true},
		{"/v1/payments/**", "/v1/payments/123/refund", true},
		{"/v1/payments/**", "/v1/users/1", false},
		{"/v1/*/users", "/v1/x/users", true},
		{"/v1/*/users", "/v1/x/y/users", false},
		{"/v1/payments/{id}", "/v1/payments/{id}", true},
		{"/v1/payments/{id}", "/v1/payments/123", false}, // braces are literal
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.value); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestPreviewCountsAndShadowing(t *testing.T) {
	eng := New(mustParse(t, examplePolicy))
	spans := []model.Span{
		sp("payment-api", "POST", "/v1/payments/{id}", model.OutcomeServerError, "write", time.Second),
		sp("payment-api", "GET", "/v1/payments/{id}", model.OutcomeSuccess, "read", time.Millisecond),
		sp("fare-engine", "GET", "/v1/calc", model.OutcomeSuccess, "read", time.Millisecond),
	}
	preview := eng.Preview(spans, 24*time.Hour, 11)
	byName := map[string]RulePreview{}
	for _, r := range preview {
		byName[r.Name] = r
	}
	if got := byName["payment-write-errors"].Matches; got != 1 {
		t.Errorf("payment-write-errors matches = %d, want 1", got)
	}
	if got := byName["read-success"].Matches; got != 2 {
		t.Errorf("read-success matches = %d, want 2", got)
	}
	if byName["read-success"].BytesPerDay <= 0 {
		t.Error("expected a positive storage estimate")
	}
	// db-operations cannot win: those spans carry no HTTP fields, but no span
	// here is a db span, so it simply never matches.
	if byName["db-operations"].Matches != 0 {
		t.Errorf("db-operations matches = %d, want 0", byName["db-operations"].Matches)
	}
}

func TestDefaultEngineKeepsFourteenDays(t *testing.T) {
	d := Default().Evaluate(sp("anything", "GET", "/x", model.OutcomeSuccess, "", time.Millisecond))
	if d.Retention != 14*24*time.Hour {
		t.Errorf("retention = %v", d.Retention)
	}
	if d.PolicyID != "default" {
		t.Errorf("policy id = %q", d.PolicyID)
	}
}

func TestShippedExamplePolicyIsValid(t *testing.T) {
	// The file in config/ must always load: it is what docs point users at.
	p, err := Load("../../config/policy.example.yaml")
	if err != nil {
		t.Fatalf("config/policy.example.yaml: %v", err)
	}
	if len(p.Rules) == 0 {
		t.Fatal("example policy has no rules")
	}
}
