// Package model defines the normalized operation evidence record derived from
// OTLP spans. One span belongs to one trace; an operation is the root span.
package model

import "time"

// Span is the storage-ready, normalized span — the unit of the operation
// evidence story. Attributes keeps the span's own attributes (post-redaction)
// so evidence views never need a second lookup.
type Span struct {
	TraceID        string
	SpanID         string
	ParentSpanID   string
	ServiceName    string
	ServiceVersion string
	Environment    string
	Name           string
	Kind           string
	HTTPMethod     string
	HTTPRoute      string
	StatusCode     int
	StartTime      time.Time
	EndTime        time.Time
	Duration       time.Duration
	ErrorType      string
	Outcome        string
	StatusMessage  string
	Attributes     map[string]string
}

// Outcome values.
const (
	OutcomeSuccess     = "success"
	OutcomeClientError = "client_error"
	OutcomeServerError = "server_error"
	OutcomeError       = "error" // non-HTTP error (OTLP status code = error)
	OutcomeUnknown     = "unknown"
)
