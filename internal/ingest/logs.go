package ingest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/shohiebsense/leutik-tarik/internal/logsnap"
	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
)

// LogsHandler accepts OTLP/HTTP logs and feeds the bounded ring buffer.
//
// It deliberately does not write to storage: a log line becomes durable only
// when a trigger fires (build prompt §8). Everything else overwrites.
type LogsHandler struct {
	Ring         *logsnap.Ring
	MaxBodyBytes int64
	MaxRecords   int
	Logf         func(format string, args ...any)

	// Retention for a captured log row; set by the caller from the policy.
	Retention time.Duration
}

func (h LogsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Ring == nil {
		http.Error(w, "log capture not configured", http.StatusServiceUnavailable)
		return
	}

	max := h.MaxBodyBytes
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)

	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	body, err := readBody(r, encoding, max)
	if err != nil {
		status := http.StatusBadRequest
		if err == errTooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}

	kind := wireKind(r.Header.Get("Content-Type"))
	events, dropped, err := decodeLogs(kind, body)
	if err != nil {
		http.Error(w, "invalid otlp logs payload: "+err.Error()+contentTypeHint(kind, body), http.StatusBadRequest)
		return
	}

	maxRecords := h.MaxRecords
	if maxRecords <= 0 {
		maxRecords = 5000
	}
	kept := 0
	for _, e := range events {
		if kept >= maxRecords {
			dropped++
			continue
		}
		h.Ring.Add(e)
		kept++
	}

	h.logf("otlp logs format=%s encoding=%s decompressed_bytes=%d buffered=%d dropped=%d",
		kind, encodingOrDefault(encoding), len(body), kept, dropped)
	writeSuccess(w, kind)
}

func (h LogsHandler) logf(format string, args ...any) {
	if h.Logf == nil {
		logfDefault(format, args...)
		return
	}
	h.Logf(format, args...)
}

func decodeLogs(kind wire, body []byte) ([]model.LogEvent, int, error) {
	if kind == wireJSON {
		return decodeLogsJSON(body)
	}
	return decodeLogsProto(body)
}

func decodeLogsProto(body []byte) ([]model.LogEvent, int, error) {
	req := &collectorlogsv1.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		return nil, 0, fmt.Errorf("protobuf: %w", err)
	}
	var out []model.LogEvent
	dropped := 0
	for _, rl := range req.GetResourceLogs() {
		svc := protoResourceService(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				text, ok := protoValue(lr.GetBody())
				if !ok {
					dropped++
					continue
				}
				out = append(out, model.LogEvent{
					TraceID:  hexFromBytes(lr.GetTraceId()),
					SpanID:   hexFromBytes(lr.GetSpanId()),
					Service:  svc,
					Severity: severityOf(lr.GetSeverityText(), lr.GetSeverityNumber()),
					Body:     redactBody(text),
					Time:     time.Unix(0, int64(lr.GetTimeUnixNano())),
					Source:   "otlp",
				})
			}
		}
	}
	return out, dropped, nil
}

func decodeLogsJSON(body []byte) ([]model.LogEvent, int, error) {
	var req otlpjson.ExportLogsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, 0, fmt.Errorf("json: %w", err)
	}
	var out []model.LogEvent
	dropped := 0
	for _, rl := range req.ResourceLogs {
		svc := ""
		for _, a := range rl.Resource.Attributes {
			if a.Key == "service.name" {
				svc = a.Value.StringValue
			}
		}
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				if lr.Body.StringValue == "" {
					dropped++
					continue
				}
				out = append(out, model.LogEvent{
					TraceID:  strings.ToLower(lr.TraceID),
					SpanID:   strings.ToLower(lr.SpanID),
					Service:  svc,
					Severity: model.NormalizeSeverity(lr.SeverityText),
					Body:     redactBody(lr.Body.StringValue),
					Time:     timeFromNanos(lr.TimeUnixNano),
					Source:   "otlp",
				})
			}
		}
	}
	return out, dropped, nil
}

// protoResourceService pulls service.name out of a resource.
func protoResourceService(kvs []*commonv1.KeyValue) string {
	for _, kv := range kvs {
		if kv.GetKey() == "service.name" {
			if v, ok := protoValue(kv.GetValue()); ok {
				return v
			}
		}
	}
	return ""
}

// redactBody applies the same credential rules to log text as to attributes.
// A log line is the easiest place for a token to leak into storage.
func redactBody(s string) string {
	if out, ok := scrubBearer(s); ok {
		return out
	}
	return s
}

// severityOf prefers the explicit text and falls back to the numeric enum, so a
// sender that omits text still ranks correctly.
func severityOf(text string, n logsv1.SeverityNumber) string {
	if strings.TrimSpace(text) != "" {
		return model.NormalizeSeverity(text)
	}
	return logsv1Severity(n)
}

// logsv1Severity maps OTLP severity numbers when text is absent.
func logsv1Severity(n logsv1.SeverityNumber) string {
	switch {
	case n >= logsv1.SeverityNumber_SEVERITY_NUMBER_FATAL:
		return "fatal"
	case n >= logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR:
		return "error"
	case n >= logsv1.SeverityNumber_SEVERITY_NUMBER_WARN:
		return "warn"
	case n >= logsv1.SeverityNumber_SEVERITY_NUMBER_INFO:
		return "info"
	case n >= logsv1.SeverityNumber_SEVERITY_NUMBER_DEBUG:
		return "debug"
	default:
		return "trace"
	}
}
