package bpf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
)

// Emitter posts probe hits to the platform's OTLP endpoint.
type Emitter struct {
	URL    string
	Client *http.Client
}

// NewEmitter returns an emitter for an ingest base URL (e.g. http://127.0.0.1:8080).
func NewEmitter(url string) *Emitter {
	return &Emitter{
		URL:    url + "/v1/traces",
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SpanFor converts a probe hit into an OTLP span. Probe-sourced spans are
// marked "correlated", never "complete": one hit proves a function ran, not
// that the surrounding request was captured.
func SpanFor(e Event, symbol string, start time.Time) otlpjson.Span {
	comm := e.CommString()
	traceID := pseudoTraceID(e.TGID)
	return otlpjson.Span{
		TraceID:           traceID,
		SpanID:            pseudoSpanID(e.TGID, start),
		Name:              "uprobe " + symbol,
		Kind:              1, // internal
		StartTimeUnixNano: fmt.Sprint(start.UnixNano()),
		EndTimeUnixNano:   fmt.Sprint(start.UnixNano()),
		Attributes: []otlpjson.Attribute{
			{Key: "process.pid", Value: otlpjson.Value{IntValue: fmt.Sprint(e.TGID)}},
			{Key: "process.thread.id", Value: otlpjson.Value{IntValue: fmt.Sprint(e.PID)}},
			{Key: "process.command", Value: otlpjson.Value{StringValue: comm}},
			{Key: "leutik.probe.symbol", Value: otlpjson.Value{StringValue: symbol}},
			{Key: "leutik.capture.source", Value: otlpjson.Value{StringValue: "agent"}},
			{Key: "leutik.capture.completeness", Value: otlpjson.Value{StringValue: "correlated"}},
		},
	}
}

// Request wraps a span into the OTLP envelope for one service.
func Request(e Event, symbol, service string, start time.Time) otlpjson.ExportTraceRequest {
	return otlpjson.ExportTraceRequest{
		ResourceSpans: []otlpjson.ResourceSpans{{
			Resource: otlpjson.Resource{Attributes: []otlpjson.Attribute{
				{Key: "service.name", Value: otlpjson.Value{StringValue: service}},
				{Key: "telemetry.sdk.name", Value: otlpjson.Value{StringValue: "leutik-agent"}},
			}},
			ScopeSpans: []otlpjson.ScopeSpans{{
				Spans: []otlpjson.Span{SpanFor(e, symbol, start)},
			}},
		}},
	}
}

// Emit posts one event. Errors are returned, not swallowed, so a caller can
// count telemetry loss (build prompt §11: account for dropped telemetry).
func (em *Emitter) Emit(req otlpjson.ExportTraceRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := em.Client.Post(em.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ingest returned %d", resp.StatusCode)
	}
	return nil
}

// pseudoTraceID derives a stable trace id from the process id so probes from
// the same process group together. Not a real distributed trace: the platform
// reports these spans as "correlated", which is the honest label.
func pseudoTraceID(tgid uint32) string {
	var b [16]byte
	for i := 0; i < 4; i++ {
		b[12+i] = byte(tgid >> (8 * i))
	}
	return hexString(b[:])
}

func pseudoSpanID(tgid uint32, t time.Time) string {
	var b [8]byte
	for i := 0; i < 4; i++ {
		b[i] = byte(tgid >> (8 * i))
	}
	for i := 0; i < 4; i++ {
		b[4+i] = byte(t.UnixNano() >> (8 * i))
	}
	return hexString(b[:])
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// HTTPSpan converts a probe-observed request into an OTLP span.
//
// It reports completeness=partial because method and route are unknown: the
// probe saw a status and a duration, not a request. Naming the span by what was
// actually measured keeps the UI from implying more.
func HTTPSpan(e HTTPEvent, service string, start time.Time) otlpjson.Span {
	status := fmt.Sprint(e.Status)
	span := otlpjson.Span{
		TraceID:           pseudoTraceID(e.TGID),
		SpanID:            pseudoSpanID(e.TGID, start),
		Name:              "http request (probe)",
		Kind:              2, // server
		StartTimeUnixNano: fmt.Sprint(start.UnixNano()),
		EndTimeUnixNano:   fmt.Sprint(start.Add(e.Duration()).UnixNano()),
		Attributes: []otlpjson.Attribute{
			{Key: "process.pid", Value: otlpjson.Value{IntValue: fmt.Sprint(e.TGID)}},
			{Key: "process.command", Value: otlpjson.Value{StringValue: e.CommString()}},
			{Key: "http.response.status_code", Value: otlpjson.Value{IntValue: status}},
			{Key: "leutik.capture.source", Value: otlpjson.Value{StringValue: "agent"}},
			{Key: "leutik.capture.completeness", Value: otlpjson.Value{StringValue: "partial"}},
			// Say what is missing rather than leaving a reader to wonder.
			{Key: "leutik.capture.missing", Value: otlpjson.Value{StringValue: "http.request.method,http.route"}},
			{Key: "leutik.capture.correlation", Value: otlpjson.Value{StringValue: "os-thread"}},
		},
	}
	if e.Status >= 500 {
		span.Status = otlpjson.Status{Code: 2, Message: status}
	}
	return span
}

// HTTPRequest wraps a probe-observed request into the OTLP envelope.
func HTTPRequest(e HTTPEvent, service string, start time.Time) otlpjson.ExportTraceRequest {
	return otlpjson.ExportTraceRequest{
		ResourceSpans: []otlpjson.ResourceSpans{{
			Resource: otlpjson.Resource{Attributes: []otlpjson.Attribute{
				{Key: "service.name", Value: otlpjson.Value{StringValue: service}},
				{Key: "telemetry.sdk.name", Value: otlpjson.Value{StringValue: "leutik-agent"}},
			}},
			ScopeSpans: []otlpjson.ScopeSpans{{Spans: []otlpjson.Span{HTTPSpan(e, service, start)}}},
		}},
	}
}
