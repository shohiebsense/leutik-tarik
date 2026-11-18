package ingest

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
)

type normStats struct {
	droppedAttrs int
	droppedSpans int
	redacted     int
}

// normalize is the single path from wire data to a stored record: identity,
// semantic attributes, kind, outcome and redaction are handled here only, so
// JSON and protobuf cannot drift.
func normalize(raw []rawSpan, ingestedAt time.Time, eng *policy.Engine) ([]model.Record, normStats) {
	if eng == nil {
		eng = policy.Default()
	}
	var stats normStats
	out := make([]model.Record, 0, len(raw))
	for _, r := range raw {
		stats.droppedAttrs += r.dropped

		// Drop instrumentation-internal spans before anything else: they would
		// otherwise occupy a row in every trace that touches the database.
		if isNoise(r.name) {
			stats.droppedSpans++
			continue
		}

		redacted := redact(r.attrs)
		stats.redacted += redacted

		statusCode := intOf(r.attrs, "http.response.status_code", "http.status_code")
		outcome := outcome(statusCode, r.statusCode)

		span := model.Span{
			TraceID:      strings.ToLower(r.traceID),
			SpanID:       strings.ToLower(r.spanID),
			ParentSpanID: strings.ToLower(r.parentSpanID),

			ServiceName:    r.resource["service.name"],
			ServiceVersion: r.resource["service.version"],
			Environment:    r.resource["deployment.environment"],

			Name:          descriptiveName(spanName(r), r.attrs),
			Kind:          kindName(r.kind),
			OperationType: operationType(r.attrs, r.name),

			HTTPMethod: firstOf(r.attrs, "http.request.method", "http.method"),
			HTTPRoute:  r.attrs["http.route"],
			StatusCode: statusCode,

			RPCSystem:      r.attrs["rpc.system"],
			RPCService:     r.attrs["rpc.service"],
			RPCMethod:      r.attrs["rpc.method"],
			GRPCStatusCode: intAt(r.attrs, "rpc.grpc.status_code"),

			DBSystem:       r.attrs["db.system"],
			DBOperation:    firstOf(r.attrs, "db.operation.name", "db.operation"),
			DBQuerySummary: r.attrs["db.query.summary"],

			MessagingSystem:      r.attrs["messaging.system"],
			MessagingDestination: firstOf(r.attrs, "messaging.destination.name", "messaging.destination"),
			MessagingOperation:   firstOf(r.attrs, "messaging.operation.type", "messaging.operation"),

			HostName:      firstOf(r.attrs, "host.name", "net.host.name"),
			PodName:       r.attrs["k8s.pod.name"],
			Namespace:     r.attrs["k8s.namespace.name"],
			ContainerName: r.attrs["container.name"],
			PeerService:   firstOf(r.attrs, "peer.service", "net.peer.name", "server.address"),

			StartTime: r.start,
			EndTime:   r.end,
			Duration:  r.end.Sub(r.start),

			ErrorType:     firstOf(r.attrs, "error.type", "error"),
			Outcome:       outcome,
			StatusMessage: r.statusMsg,
			Attributes:    r.attrs,
			Links:         r.links,
		}

		pd := eng.Evaluate(span)
		out = append(out, model.Record{
			Span: span,
			Decision: model.Decision{
				PolicyID:      pd.PolicyID,
				PolicyVersion: pd.PolicyVersion,
				Sampled:       true,
				Redactions:    redacted,
				Retention:     pd.Retention,
			},
			Source:       provenance(r.attrs, "leutik.capture.source", "otlp-sdk"),
			Completeness: completeness(r.attrs),
			IngestedAt:   ingestedAt,
		})
	}
	return out, stats
}

// completeness reports how much the capture actually proves. A sender may
// declare it; otherwise an OTLP SDK's own spans are complete (it knows its own
// parentage). Never upgraded by guesswork — a probe that saw one function call
// did not see the request (build prompt §5.2).
func completeness(attrs map[string]string) string {
	switch v := attrs["leutik.capture.completeness"]; v {
	case model.CompletenessPartial, model.CompletenessCorrelated, model.CompletenessUnsupported:
		return v
	default:
		return model.CompletenessComplete
	}
}

// provenance reads a declared source, falling back to the default.
func provenance(attrs map[string]string, key, def string) string {
	if v := attrs[key]; v != "" {
		return v
	}
	return def
}

// spanName prefers the function/method that produced the span over the
// instrumentation's generic name ("get", "incr", "/healthcheck"). A trace reads
// as a code path when every span is a function name, and the original is kept in
// an attribute rather than thrown away.
func spanName(r rawSpan) string {
	if fn := r.attrs["code.function"]; fn != "" {
		return fn
	}
	return r.name
}

func firstOf(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := attrs[k]; v != "" {
			return v
		}
	}
	return ""
}

// operationType is a conservative classification for retention selectors
// (prompt §10). Anything not clearly identified stays empty rather than being
// guessed — a wrong class would silently move data to the wrong retention tier.
func operationType(attrs map[string]string, name string) string {
	switch {
	case attrs["db.system"] != "":
		return "db"
	case attrs["rpc.system"] != "":
		return "rpc"
	case attrs["messaging.system"] != "":
		return "queue"
	}
	switch strings.ToUpper(firstOf(attrs, "http.request.method", "http.method")) {
	case "GET", "HEAD", "OPTIONS":
		return "read"
	case "POST", "PUT", "PATCH", "DELETE":
		return "write"
	case "":
		return ""
	default:
		return ""
	}
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
	if key == "" {
		return 0
	}
	n, err := strconv.Atoi(m[key])
	if err != nil {
		return 0
	}
	return n
}

// intOf reads the first integer-valued attribute that is present, across naming
// generations. Returning the value (not the key) is the point: intAt takes a key.
func intOf(attrs map[string]string, keys ...string) int {
	for _, k := range keys {
		v, ok := attrs[k]
		if !ok || v == "" {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// outcome classifies a span. otlpStatus is the OTLP status code: 0 unset,
// 1 ok, 2 error.
//
// OTLP defines UNSET as "no error was recorded", not "we do not know", so a
// database or service span with UNSET is reported as ok. Labelling those
// "unknown" made every internal span in every trace look unresolved, which is
// noise rather than honesty - the one case that genuinely stays unknown is a
// span with no status and no outcome semantics at all, which does not occur in
// practice.
func outcome(statusCode, otlpStatus int) string {
	switch {
	// An HTTP status is more specific than a generic OTLP error: a 5xx and a 4xx
	// are different problems and the whole /services split depends on telling
	// them apart.
	case statusCode >= 500:
		return model.OutcomeServerError
	case statusCode >= 400:
		return model.OutcomeClientError
	case otlpStatus == 2:
		return model.OutcomeError
	case statusCode > 0:
		return model.OutcomeSuccess
	case otlpStatus == 1:
		return model.OutcomeSuccess
	default:
		// UNSET on a non-HTTP span: the operation completed and nothing reported
		// an error.
		return model.OutcomeOK
	}
}

// sensitiveKeys are matched on the bare attribute name so both OTLP forms
// (`authorization`, `http.request.header.authorization`) are covered (prompt
// §15: credentials must be redacted before persistence).
// sensitiveAttrKeys are attribute names that must never be persisted with their
// value. db.query.text and db.statement are here because instrumentation
// libraries emit the FULL SQL by default (otelpgx does: semconv.DBQueryText),
// and a query string contains member identifiers, phone numbers and transaction
// numbers. Redacting on the way in is the only point where it can be guaranteed.
var sensitiveAttrKeys = []string{
	"db.query.parameter",
	"db.statement.parameter",
	"http.request.body",
	"http.response.body",
}

// sqlTextKeys are statement-bearing attributes. They are redacted unless
// CaptureSQL is on, because a statement contains member identifiers, phone
// numbers and transaction numbers - redacting on the way in is the only place
// where that can be guaranteed.
var sqlTextKeys = []string{"db.query.text", "db.statement"}

// CaptureSQL keeps statement text. Off by default: safe unless asked.
var CaptureSQL = os.Getenv("LEUTIK_CAPTURE_SQL_TEXT") == "on"

// sensitiveKeyPrefixes catch families, e.g. db.query.parameter.0.
var sensitiveKeyPrefixes = []string{
	"db.query.parameter.",
	"db.statement.parameter.",
}

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
	for _, sqlKey := range sqlTextKeys {
		if k == sqlKey {
			return !CaptureSQL
		}
	}
	for _, exact := range sensitiveAttrKeys {
		if k == exact {
			return true
		}
	}
	for _, prefix := range sensitiveKeyPrefixes {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
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
	i := strings.Index(strings.ToLower(v), "bearer ")
	if i < 0 {
		return v, false
	}
	return v[:i] + "Bearer " + redactedPlaceholder, true
}
