// Command demo-sender posts one synthetic trace to a running leutik-tarik.
// A clearly-labeled fixture for demos and manual testing only — never used to
// fabricate production telemetry.
//
// Flags mirror a real OTLP exporter: -proto sends protobuf (OTLP's default
// content type), -gzip compresses. Used to prove the ingest path over real
// HTTP, not just in-process.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	collectorv1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
)

const (
	traceID = "0123456789abcdef0123456789abcdef"
	rootID  = "aaaaaaaaaaaaaaaa"
	dbID    = "bbbbbbbbbbbbbbbb"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "leutik-tarik address")
	useProto := flag.Bool("proto", false, "send protobuf (OTLP default) instead of JSON")
	useGzip := flag.Bool("gzip", false, "send a gzip-compressed body")
	flag.Parse()

	now := time.Now().UnixNano()
	body, contentType, err := payload(now, *useProto)
	if err != nil {
		log.Fatal(err)
	}

	if *useGzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(body); err != nil {
			log.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			log.Fatal(err)
		}
		body = buf.Bytes()
	}

	req, err := http.NewRequest(http.MethodPost, *addr+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if *useGzip {
		req.Header.Set("Content-Encoding", "gzip")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	fmt.Printf("demo trace sent: %s (status %d, proto=%v gzip=%v)\n",
		resp.Status, resp.StatusCode, *useProto, *useGzip)
}

func payload(now int64, asProto bool) ([]byte, string, error) {
	if asProto {
		b, err := proto.Marshal(protoRequest(now))
		return b, "application/x-protobuf", err
	}
	b, err := json.Marshal(jsonRequest(now))
	return b, "application/json", err
}

func jsonRequest(now int64) otlpjson.ExportTraceRequest {
	str := func(k, v string) otlpjson.Attribute {
		return otlpjson.Attribute{Key: k, Value: otlpjson.Value{StringValue: v}}
	}
	root := otlpjson.Span{
		TraceID:           traceID,
		SpanID:            rootID,
		Name:              "POST /v1/payments/{id}",
		Kind:              2,
		StartTimeUnixNano: fmt.Sprint(now),
		EndTimeUnixNano:   fmt.Sprint(now + int64(2*time.Second)),
		Attributes: []otlpjson.Attribute{
			str("http.request.method", "POST"),
			str("http.route", "/v1/payments/{id}"),
			{Key: "http.response.status_code", Value: otlpjson.Value{IntValue: "500"}},
			str("error.type", "db_timeout"),
			str("http.request.header.authorization", "Bearer supersecret"),
		},
		Status: otlpjson.Status{Code: 2, Message: "query timeout"},
	}
	child := otlpjson.Span{
		TraceID:           traceID,
		SpanID:            dbID,
		ParentSpanID:      rootID,
		Name:              "db.query SELECT",
		Kind:              3,
		StartTimeUnixNano: fmt.Sprint(now + int64(100*time.Millisecond)),
		EndTimeUnixNano:   fmt.Sprint(now + int64(1900*time.Millisecond)),
		Attributes: []otlpjson.Attribute{
			str("db.system", "postgresql"),
			str("db.operation.name", "SELECT"),
		},
		Status: otlpjson.Status{Code: 2, Message: "statement timeout"},
	}
	scope := otlpjson.ScopeSpans{Spans: []otlpjson.Span{root, child}}
	res := otlpjson.ResourceSpans{
		Resource: otlpjson.Resource{Attributes: []otlpjson.Attribute{
			str("service.name", "payment"),
			str("service.version", "v1.2.3"),
			str("deployment.environment", "dev"),
		}},
		ScopeSpans: []otlpjson.ScopeSpans{scope},
	}
	return otlpjson.ExportTraceRequest{ResourceSpans: []otlpjson.ResourceSpans{res}}
}

func protoRequest(now int64) *collectorv1.ExportTraceServiceRequest {
	str := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{
			Value: &commonv1.AnyValue_StringValue{StringValue: v},
		}}
	}
	root := &tracev1.Span{
		TraceId:           []byte(traceIDBytes()),
		SpanId:            []byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa},
		Name:              "POST /v1/payments/{id}",
		Kind:              tracev1.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: uint64(now),
		EndTimeUnixNano:   uint64(now + int64(2*time.Second)),
		Attributes: []*commonv1.KeyValue{
			str("http.request.method", "POST"),
			str("http.route", "/v1/payments/{id}"),
			{Key: "http.response.status_code", Value: &commonv1.AnyValue{
				Value: &commonv1.AnyValue_IntValue{IntValue: 500},
			}},
			// Proves redaction on the protobuf path as well as the JSON one.
			str("http.request.header.authorization", "Bearer supersecret"),
		},
		Status: &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: "query timeout"},
	}
	child := &tracev1.Span{
		TraceId:           []byte(traceIDBytes()),
		SpanId:            []byte{0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb},
		ParentSpanId:      []byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa},
		Name:              "db.query SELECT",
		Kind:              tracev1.Span_SPAN_KIND_CLIENT,
		StartTimeUnixNano: uint64(now + int64(100*time.Millisecond)),
		EndTimeUnixNano:   uint64(now + int64(1900*time.Millisecond)),
		Attributes: []*commonv1.KeyValue{
			str("db.system", "postgresql"),
			str("db.operation.name", "SELECT"),
		},
		Status: &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: "statement timeout"},
	}

	res := &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		str("service.name", "payment"),
		str("service.version", "v1.2.3"),
		str("deployment.environment", "dev"),
	}}
	scope := &tracev1.ScopeSpans{Spans: []*tracev1.Span{root, child}}
	resourceSpans := &tracev1.ResourceSpans{
		Resource:   res,
		ScopeSpans: []*tracev1.ScopeSpans{scope},
	}
	return &collectorv1.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{resourceSpans},
	}
}

// traceIDBytes converts the hex trace ID to the 16 bytes protobuf carries.
func traceIDBytes() []byte {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, 0, len(traceID)/2)
	for i := 0; i+1 < len(traceID); i += 2 {
		hi := indexByte(hexdigits, traceID[i])
		lo := indexByte(hexdigits, traceID[i+1])
		b = append(b, byte(hi<<4|lo))
	}
	return b
}

func indexByte(set string, c byte) int {
	for i := 0; i < len(set); i++ {
		if set[i] == c {
			return i
		}
	}
	return 0
}
