package ingest

import (
	"strconv"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
)

type normStats struct {
	droppedAttrs int
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

		redacted := redact(r.attrs)
		stats.redacted += redacted

		statusCode := intAt(r.attrs, "http.response.status_code")
		outcome := outcome(statusCode, r.statusCode)

		span := model.Span{
			TraceID:      strings.ToLower(r.traceID),
			SpanID:       strings.ToLower(r.spanID),
			ParentSpanID: strings.ToLower(r.parentSpanID),

			ServiceName:    r.resource["service.name"],
			ServiceVersion: r.resource["service.version"],
			Environment:    r.resource["deployment.environment"],

			Name:          r.name,
			Kind:          kindName(r.kind),
			OperationType: operationType(r.attrs, r.name),

			HTTPMethod: r.attrs["http.request.method"],
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

			HostName:      r.attrs["host.name"],
			PodName:       r.attrs["k8s.pod.name"],
			Namespace:     r.attrs["k8s.namespace.name"],
			ContainerName: r.attrs["container.name"],
			PeerService:   r.attrs["peer.service"],

			StartTime: r.start,
			EndTime:   r.end,
			Duration:  r.end.Sub(r.start),

			ErrorType:     r.attrs["error.type"],
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
	switch strings.ToUpper(attrs["http.request.method"]) {
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
	i := strings.Index(strings.ToLower(v), "bearer ")
	if i < 0 {
		return v, false
	}
	return v[:i] + "Bearer " + redactedPlaceholder, true
}
