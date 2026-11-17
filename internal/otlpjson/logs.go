package otlpjson

// ExportLogsRequest is the OTLP/HTTP JSON logs envelope.
type ExportLogsRequest struct {
	ResourceLogs []ResourceLogs `json:"resourceLogs"`
}

// ResourceLogs groups log records by resource.
type ResourceLogs struct {
	Resource  Resource   `json:"resource"`
	ScopeLogs []ScopeLog `json:"scopeLogs"`
}

// ScopeLog is one instrumentation scope's log records.
type ScopeLog struct {
	LogRecords []LogRecord `json:"logRecords"`
}

// LogRecord is one OTLP log record. 64-bit integers are strings.
type LogRecord struct {
	TimeUnixNano   string      `json:"timeUnixNano"`
	SeverityText   string      `json:"severityText"`
	SeverityNumber int         `json:"severityNumber"`
	Body           Value       `json:"body"`
	TraceID        string      `json:"traceId"`
	SpanID         string      `json:"spanId"`
	Attributes     []Attribute `json:"attributes"`
}
