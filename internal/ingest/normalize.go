package ingest

import (
	"strconv"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

type normStats struct {
	droppedAttrs int
	redacted     int
}

// normalize is the single path from wire data to a stored span: identity,
// kind, outcome, redaction and attributes are handled here only, so JSON and
// protobuf cannot drift.
func normalize(raw []rawSpan) ([]model.Span, normStats) {
	var stats normStats
	out := make([]model.Span, 0, len(raw))
	for _, r := range raw {
		stats.droppedAttrs += r.dropped

		redacted := redact(r.attrs)
		stats.redacted += redacted

		res := r.resource
		statusCode := intAt(r.attrs, "http.response.status_code")
		out = append(out, model.Span{
			TraceID:        strings.ToLower(r.traceID),
			SpanID:         strings.ToLower(r.spanID),
			ParentSpanID:   strings.ToLower(r.parentSpanID),
			ServiceName:    res["service.name"],
			ServiceVersion: res["service.version"],
			Environment:    res["deployment.environment"],
			Name:           r.name,
			Kind:           kindName(r.kind),
			HTTPMethod:     r.attrs["http.request.method"],
			HTTPRoute:      r.attrs["http.route"],
			StatusCode:     statusCode,
			StartTime:      r.start,
			EndTime:        r.end,
			Duration:       r.end.Sub(r.start),
			ErrorType:      r.attrs["error.type"],
			Outcome:        outcome(statusCode, r.statusCode),
			StatusMessage:  r.statusMsg,
			Attributes:     r.attrs,
		})
	}
	return out, stats
}

// timeFromNanos parses OTLP's string-encoded 64-bit nanosecond timestamps.
func timeFromNanos(s string) time.Time {
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func intAt(m map[string]string, key string) int {
	n, err := strconv.Atoi(m[key])
	if err != nil {
		return 0
	}
	return n
}

// outcome classifies the span. otlpStatus is the OTLP status code: 0 unset,
// 1 ok, 2 error.
func outcome(statusCode, otlpStatus int) string {
	switch {
	case otlpStatus == 2:
		return model.OutcomeError
	case statusCode >= 500:
		return model.OutcomeServerError
	case statusCode >= 400:
		return model.OutcomeClientError
	case statusCode > 0:
		return model.OutcomeSuccess
	default:
		return model.OutcomeUnknown
	}
}

// sensitiveKeys are matched on the bare attribute name so both OTLP forms
// (`authorization`, `http.request.header.authorization`) are covered (prompt
// §15: credentials must be redacted before persistence).
var sensitiveKeys = []string{
	"authorization",
	"proxy-authorization",
	"cookie",
	"set-cookie",
	"password",
	"passwd",
	"secret",
	"token",
	"api-key",
	"apikey",
	"x-api-key",
	"session_id",
	"sessionid",
	"credential",
}

const redactedPlaceholder = "[REDACTED]"

// redact blanks sensitive attribute values in place and returns how many it
// changed.
func redact(attrs map[string]string) int {
	n := 0
	for k, v := range attrs {
		if v == "" {
			continue
		}
		if isSensitive(k) {
			attrs[k] = redactedPlaceholder
			n++
			continue
		}
		if replaced, ok := scrubBearer(v); ok {
			attrs[k] = replaced
			n++
		}
	}
	return n
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// scrubBearer catches credentials that arrive in a non-sensitive key, e.g.
// `http.request.header.x-custom: Bearer eyJ…`.
func scrubBearer(v string) (string, bool) {
	lower := strings.ToLower(v)
	i := strings.Index(lower, "bearer ")
	if i < 0 {
		return v, false
	}
	return v[:i] + "Bearer " + redactedPlaceholder, true
}
