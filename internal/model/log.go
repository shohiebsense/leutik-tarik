package model

import (
	"strings"
	"time"
)

// LogEvent is one structured log line captured around a trigger.
//
// Exact reports whether the event carried the trace id itself (direct evidence)
// or was only matched by time and process (correlation). The distinction is
// stored, not inferred at read time, because a guess made later is worth less
// than a fact recorded now (build prompt §8.3).
type LogEvent struct {
	TraceID  string
	SpanID   string
	Service  string
	Severity string
	Body     string
	Time     time.Time
	// Source names where it came from: otlp, ring, journald.
	Source string
	Exact  bool
	// Retention is this row's lifetime, decided at capture time.
	Retention time.Duration
}

// Severity ranking, used to keep the most useful lines when a snapshot is
// trimmed.
var severityRank = map[string]int{
	"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5,
}

// SeverityAtLeast reports whether sev is at least min.
func SeverityAtLeast(sev, min string) bool {
	return severityRank[normalizeSeverity(sev)] >= severityRank[normalizeSeverity(min)]
}

func normalizeSeverity(s string) string {
	// Uppercase first: loggers emit "error", "Warn" and "ERROR" alike, and a
	// case-sensitive switch would silently rank all of them as info.
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "WARN", "WARNING":
		return "warn"
	case "ERROR", "ERR":
		return "error"
	case "FATAL", "CRITICAL", "PANIC":
		return "fatal"
	case "DEBUG":
		return "debug"
	case "TRACE":
		return "trace"
	case "INFO", "NOTICE":
		return "info"
	default:
		return "info"
	}
}

// NormalizeSeverity returns the canonical lowercase severity name.
func NormalizeSeverity(s string) string { return normalizeSeverity(s) }
