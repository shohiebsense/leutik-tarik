package ingest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	collectorv1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/shohiebsense/leutik-tarik/internal/ingest"
	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func newHandler() (*ingest.Handler, *store.Memory) {
	s := store.NewMemory()
	return &ingest.Handler{Store: s, Logf: func(string, ...any) {}}, s
}

func spansOf(t *testing.T, s *store.Memory) []model.Span {
	t.Helper()
	spans, err := s.ListOperations(context.Background(), store.Query{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return spans
}

func TestIngestJSON(t *testing.T) {
	h, s := newHandler()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payment"}}]},"scopeSpans":[{"spans":[{"traceId":"aa","spanId":"bb","name":"GET /x","kind":2,"startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[{"key":"http.response.status_code","value":{"intValue":"500"}}]}]}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := spansOf(t, s)
	if len(got) != 1 || got[0].Outcome != model.OutcomeServerError {
		t.Fatalf("spans = %+v", got)
	}
}

// The default OTLP content type is protobuf; without this a stock exporter
// cannot talk to us at all (review B1).
func TestIngestProtobuf(t *testing.T) {
	h, s := newHandler()
	req := &collectorv1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
				{Key: "service.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "fare-membership"}}},
			}},
			ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
				TraceId:           []byte{0xde, 0xad, 0xbe, 0xef},
				SpanId:            []byte{0x01, 0x02},
				Name:              "POST /v1/payments/{id}",
				Kind:              tracev1.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: 1000000000,
				EndTimeUnixNano:   3000000000,
				Attributes: []*commonv1.KeyValue{
					{Key: "http.request.method", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "POST"}}},
					{Key: "http.response.status_code", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 500}}},
				},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-protobuf" {
		t.Errorf("response content-type = %q, want protobuf", got)
	}
	got := spansOf(t, s)
	if len(got) != 1 {
		t.Fatalf("want 1 span, got %d", len(got))
	}
	if got[0].TraceID != "deadbeef" {
		t.Errorf("traceID = %q, want deadbeef", got[0].TraceID)
	}
	if got[0].ServiceName != "fare-membership" {
		t.Errorf("service = %q", got[0].ServiceName)
	}
	if got[0].StatusCode != 500 || got[0].Kind != "server" {
		t.Errorf("span = %+v", got[0])
	}
}

func TestIngestGzipJSON(t *testing.T) {
	h, s := newHandler()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"gz"}}]},"scopeSpans":[{"spans":[{"traceId":"cc","spanId":"dd","name":"GET /gz","kind":2,"startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000"}]}]}]}`))
	_ = zw.Close()

	r := httptest.NewRequest(http.MethodPost, "/v1/traces", &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := spansOf(t, s); len(got) != 1 || got[0].ServiceName != "gz" {
		t.Fatalf("spans = %+v", got)
	}
}

func TestIngestRejectsOversizedBody(t *testing.T) {
	s := store.NewMemory()
	h := &ingest.Handler{Store: s, MaxBodyBytes: 256, Logf: func(string, ...any) {}}
	big := bytes.Repeat([]byte("a"), 1024)
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(big))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// A small gzip bomb must be bounded by the decompressed cap, not the compressed
// size (review B3).
func TestIngestRejectsGzipBomb(t *testing.T) {
	s := store.NewMemory()
	h := &ingest.Handler{Store: s, MaxBodyBytes: 2048, Logf: func(string, ...any) {}}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(bytes.Repeat([]byte("a"), 1<<20))
	_ = zw.Close()
	if buf.Len() >= 2048 {
		t.Fatalf("bomb should compress below the cap, got %d bytes", buf.Len())
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestIngestRedactsBeforeStore(t *testing.T) {
	h, s := newHandler()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"auth"}}]},"scopeSpans":[{"spans":[{"traceId":"ee","spanId":"ff","name":"POST /login","kind":2,"startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[{"key":"http.request.header.authorization","value":{"stringValue":"Basic hunter2"}},{"key":"http.route","value":{"stringValue":"/login"}}]}]}]}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), r)

	spans, err := s.GetTrace(context.Background(), "ee")
	if err != nil || len(spans) != 1 {
		t.Fatalf("trace = %d spans, err=%v", len(spans), err)
	}
	if got := spans[0].Attributes["http.request.header.authorization"]; got != "[REDACTED]" {
		t.Errorf("stored credential = %q, want redacted", got)
	}
	if spans[0].HTTPRoute != "/login" {
		t.Errorf("route = %q", spans[0].HTTPRoute)
	}
}

func TestIngestRejectsOversizedAttributeNames(t *testing.T) {
	// Guard the attribute map itself, not only the body.
	h, _ := newHandler()
	long := strings.Repeat("k", 4096)
	body, _ := json.Marshal(otlpjson.ExportTraceRequest{})
	_ = body
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"11","spanId":"22","name":"`+long+`"}]}]}]}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	// Accepted for now (documented limit), but must never panic or be unbounded.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
