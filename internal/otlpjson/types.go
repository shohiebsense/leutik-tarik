// Package otlpjson holds the OTLP/HTTP JSON wire types for
// ExportTraceServiceRequest.
//
// Trace/span IDs are hex strings, which is what OTLP JSON specifies for those
// two fields (base64 applies to other bytes fields). Not a deviation.
//
// Missing, and tracked in docs/review-2026-11-17.md: protobuf transport (B1),
// gzip (B2), request body limits (B3).
package otlpjson

// ExportTraceRequest is the OTLP/HTTP JSON trace-export envelope.
type ExportTraceRequest struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

// ResourceSpans groups spans by resource (service identity).
type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

// Resource carries service-level attributes (service.name, version, environment).
type Resource struct {
	Attributes []Attribute `json:"attributes"`
}

// ScopeSpans groups spans under one instrumentation scope.
type ScopeSpans struct {
	Spans []Span `json:"spans"`
}

// Span is one OTLP span in JSON encoding. 64-bit integers are strings.
type Span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	Name              string      `json:"name"`
	Kind              int         `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []Attribute `json:"attributes"`
	Status            Status      `json:"status"`
}

// Status is the OTLP span status (code: 0 unset, 1 ok, 2 error).
type Status struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Attribute is a key/value pair.
type Attribute struct {
	Key   string `json:"key"`
	Value Value  `json:"value"`
}

// Value holds a string, int64 (as string), bool, or double value. Pointers are
// used for bool/double so an explicit `false`/`0` is distinguishable from absent.
type Value struct {
	StringValue string   `json:"stringValue"`
	IntValue    string   `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}
