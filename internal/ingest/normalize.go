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

// defaultPolicy is stamped on every record until the policy engine exists
// (Phase 4). Recorded rather than implied, so retention decisions stay
// auditable per row.
const defaultPolicy = "default"

// normalize is the single path from wire data to a stored record: identity,
// semantic attributes, kind, outcome and redaction are handled here only, so
// JSON and protobuf cannot drift.
func normalize(raw []rawSpan, ingestedAt time.Time) ([]model.Record, normStats) {
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

		out = append(out, model.Record{
			Span:     span,
			Decision: model.Decision{PolicyID: defaultPolicy, PolicyVersion: 1, Sampled: true, Redactions: redacted, Retention: retentionFor(span)},
			Source:   "otlp-sdk",
			// An OTLP SDK supplies its own spans, so parentage is real rather
			// than correlated — except for async links, which stay links.
			Completeness: model.CompletenessComplete,
			IngestedAt:   ingestedAt,
		})
	}
	return out, stats
}

func firstOf(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := attrs[k]; v != "" {
			return v
		}
	}
	return ""
}

// Default retention tiers, until the policy engine (Phase 4) reads them from
// config. Values mirror the example policy in the build prompt; the decision is
// stored on every row so a later change is auditable rather than retroactive.
const (
	retentionReadSuccess = 48 * time.Hour      // 2d  — cheap, high-volume reads
	retentionErrors      = 30 * 24 * time.Hour // 30d — failures are the evidence
	retentionDefault     = 14 * 24 * time.Hour // 14d — everything else
)

// retentionFor picks this row's lifetime. Kept deliberately dumb: a wrong class
// here silently moves evidence to the wrong tier, so only unambiguous cases
// get a special tier.
func retentionFor(s model.Span) time.Duration {
	if s.Outcome == model.OutcomeError || s.Outcome == model.OutcomeServerError {
		return retentionErrors
	}
	if s.HTTPMethod == "GET" || s.HTTPMethod == "HEAD" {
		if s.Outcome == model.OutcomeSuccess {
			return retentionReadSuccess
		}
	}
	return retentionDefault
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
