// Package model defines the normalized operation evidence record derived from
// OTLP spans. One span belongs to one trace; an operation is the root span.
package model

import (
	"sort"
	"time"
)

// Span is the storage-ready, normalized span — the unit of the operation
// evidence story.
//
// Fields are typed when they are searchable (build prompt §5.4 operation
// identity); everything else lives in Attributes so the schema does not grow a
// column per library.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string

	ServiceName    string
	ServiceVersion string
	Environment    string

	Name          string
	Kind          string
	OperationType string

	HTTPMethod string
	HTTPRoute  string
	StatusCode int

	RPCSystem      string
	RPCService     string
	RPCMethod      string
	GRPCStatusCode int

	DBSystem        string
	DBOperation     string
	DBQuerySummary  string
	MessagingSystem string
	// MessagingDestination is the queue/topic name; MessagingOperation is
	// publish/receive/process.
	MessagingDestination string
	MessagingOperation   string

	HostName      string
	PodName       string
	Namespace     string
	ContainerName string
	PeerService   string

	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration

	ErrorType     string
	Outcome       string
	StatusMessage string

	// Attributes holds the span's own attributes after redaction.
	Attributes map[string]string

	// Links carries asynchronous parentage (queue produce/consume) where the
	// relationship is a link, not a parent.
	Links []Link
}

// Link is an OTLP span link: related work that is not a parent.
type Link struct {
	TraceID string
	SpanID  string
}

// Outcome values.
const (
	OutcomeSuccess     = "success"
	OutcomeClientError = "client_error"
	OutcomeServerError = "server_error"
	OutcomeError       = "error" // non-HTTP error (OTLP status code = error)
	OutcomeUnknown     = "unknown"
)

// Instrumentation completeness states (build prompt §5.2). Never upgraded by
// guesswork: a span that was only correlated stays correlated.
const (
	CompletenessComplete    = "complete"
	CompletenessPartial     = "partial"
	CompletenessCorrelated  = "correlated"
	CompletenessUnsupported = "unsupported"
)

// Decision is what the ingest pipeline decided about a span. Persisted with the
// record so retention and sampling are auditable per row (prompt §10.2).
type Decision struct {
	PolicyID      string
	PolicyVersion uint32
	Sampled       bool
	Redactions    int
	// Retention is how long this row may live. Persisted per row and enforced
	// by the storage TTL, so retention is data, not a rule set re-evaluated
	// later (see migration 002 for why rule sets do not work).
	Retention time.Duration
}

// Record is one stored span plus the provenance needed to judge it.
type Record struct {
	Span         Span
	Decision     Decision
	Source       string // "otlp-sdk" or "agent"
	Completeness string
	IngestedAt   time.Time
}

// Summary is what one operation looks like when the whole trace is known. It is
// derived at read time, never stored, so it cannot drift from the spans.
type Summary struct {
	TraceID    string
	Outcome    string
	ErrorSpans int
	Spans      int
	Services   []string
	Duration   time.Duration
	Slowest    Span
}

// severity ranks outcomes so a summary reports the worst one seen.
var severity = map[string]int{
	OutcomeSuccess:     0,
	OutcomeUnknown:     1,
	OutcomeClientError: 2,
	OutcomeServerError: 3,
	OutcomeError:       4,
}

// Summarize rolls a trace up. A root span returning 200 with a timed-out child
// query must not read as success — that is the blindness this platform exists
// to remove (review M2).
func Summarize(spans []Span) Summary {
	s := Summary{Spans: len(spans), Outcome: OutcomeUnknown}
	if len(spans) == 0 {
		return s
	}
	s.TraceID = spans[0].TraceID

	seen := make(map[string]struct{}, len(spans))
	var first time.Time
	var last time.Time
	worst := -1
	for i, sp := range spans {
		if i == 0 || sp.StartTime.Before(first) {
			first = sp.StartTime
		}
		if sp.EndTime.After(last) {
			last = sp.EndTime
		}
		if sev := severity[sp.Outcome]; sev > worst {
			worst = sev
			s.Outcome = sp.Outcome
		}
		if sp.Outcome != OutcomeSuccess && sp.Outcome != OutcomeUnknown {
			s.ErrorSpans++
		}
		if sp.Duration > s.Slowest.Duration {
			s.Slowest = sp
		}
		if sp.ServiceName != "" {
			if _, ok := seen[sp.ServiceName]; !ok {
				seen[sp.ServiceName] = struct{}{}
				s.Services = append(s.Services, sp.ServiceName)
			}
		}
	}
	sort.Strings(s.Services)
	if !first.IsZero() && !last.IsZero() {
		s.Duration = last.Sub(first)
	}
	return s
}
