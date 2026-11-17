package ingest

import (
	"fmt"
	"time"

	collectorv1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// rawSpan is the encoding-neutral intermediate: both decoders produce it and
// one normaliser turns it into model.Span, so kind/outcome/redaction logic
// exists exactly once.
type rawSpan struct {
	resource map[string]string
	attrs    map[string]string
	dropped  int

	traceID      string
	spanID       string
	parentSpanID string
	name         string
	kind         int
	start        time.Time
	end          time.Time
	statusCode   int
	statusMsg    string
}

func marshalEmptyResponse() []byte {
	b, err := proto.Marshal(&collectorv1.ExportTraceServiceResponse{})
	if err != nil {
		return nil
	}
	return b
}

func decode(kind wire, body []byte) ([]rawSpan, error) {
	if kind == wireJSON {
		return decodeJSON(body)
	}
	return decodeProto(body)
}

func decodeProto(body []byte) ([]rawSpan, error) {
	req := &collectorv1.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		return nil, fmt.Errorf("protobuf: %w", err)
	}
	var out []rawSpan
	for _, rs := range req.GetResourceSpans() {
		res, resDropped := protoAttrs(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				attrs, dropped := protoAttrs(sp.GetAttributes())
				out = append(out, rawSpan{
					resource:     res,
					attrs:        attrs,
					dropped:      resDropped + dropped,
					traceID:      hexFromBytes(sp.GetTraceId()),
					spanID:       hexFromBytes(sp.GetSpanId()),
					parentSpanID: hexFromBytes(sp.GetParentSpanId()),
					name:         sp.GetName(),
					kind:         int(sp.GetKind()),
					start:        time.Unix(0, int64(sp.GetStartTimeUnixNano())),
					end:          time.Unix(0, int64(sp.GetEndTimeUnixNano())),
					statusCode:   int(sp.GetStatus().GetCode()),
					statusMsg:    sp.GetStatus().GetMessage(),
				})
			}
		}
	}
	return out, nil
}

// protoAttrs keeps scalars and counts everything it cannot represent, instead
// of dropping silently (review M4).
func protoAttrs(kvs []*commonv1.KeyValue) (map[string]string, int) {
	m := make(map[string]string, len(kvs))
	dropped := 0
	for _, kv := range kvs {
		v, ok := protoValue(kv.GetValue())
		if !ok {
			dropped++
			continue
		}
		m[kv.GetKey()] = v
	}
	return m, dropped
}

func protoValue(v *commonv1.AnyValue) (string, bool) {
	switch val := v.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return val.StringValue, true
	case *commonv1.AnyValue_IntValue:
		return fmt.Sprint(val.IntValue), true
	case *commonv1.AnyValue_BoolValue:
		return fmt.Sprint(val.BoolValue), true
	case *commonv1.AnyValue_DoubleValue:
		return fmt.Sprint(val.DoubleValue), true
	case *commonv1.AnyValue_BytesValue:
		return hexFromBytes(val.BytesValue), true
	default:
		// Arrays and kvlists are counted as dropped, not guessed at.
		return "", false
	}
}

func hexFromBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const hextable = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hextable[c>>4], hextable[c&0x0f])
	}
	return string(out)
}

// kindName maps OTLP SpanKind to its textual form.
func kindName(k int) string {
	switch tracev1.Span_SpanKind(k) {
	case tracev1.Span_SPAN_KIND_INTERNAL:
		return "internal"
	case tracev1.Span_SPAN_KIND_SERVER:
		return "server"
	case tracev1.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracev1.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracev1.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	default:
		return "unspecified"
	}
}
