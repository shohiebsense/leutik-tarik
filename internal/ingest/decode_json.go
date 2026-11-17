package ingest

import (
	"encoding/json"
	"fmt"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
)

func decodeJSON(body []byte) ([]rawSpan, error) {
	var req otlpjson.ExportTraceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	var out []rawSpan
	for _, rs := range req.ResourceSpans {
		res, resDropped := jsonAttrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				attrs, dropped := jsonAttrs(sp.Attributes)
				out = append(out, rawSpan{
					resource:     res,
					attrs:        attrs,
					dropped:      resDropped + dropped,
					traceID:      sp.TraceID,
					spanID:       sp.SpanID,
					parentSpanID: sp.ParentSpanID,
					name:         sp.Name,
					kind:         sp.Kind,
					start:        timeFromNanos(sp.StartTimeUnixNano),
					end:          timeFromNanos(sp.EndTimeUnixNano),
					statusCode:   sp.Status.Code,
					statusMsg:    sp.Status.Message,
					links:        jsonLinks(sp.Links),
				})
			}
		}
	}
	return out, nil
}

func jsonLinks(links []otlpjson.SpanLink) []model.Link {
	if len(links) == 0 {
		return nil
	}
	out := make([]model.Link, 0, len(links))
	for _, l := range links {
		out = append(out, model.Link{TraceID: l.TraceID, SpanID: l.SpanID})
	}
	return out
}

// jsonAttrs keeps every scalar type and counts what it cannot represent, so
// attributes are never dropped silently (review M4).
func jsonAttrs(as []otlpjson.Attribute) (map[string]string, int) {
	m := make(map[string]string, len(as))
	dropped := 0
	for _, a := range as {
		switch {
		case a.Value.StringValue != "":
			m[a.Key] = a.Value.StringValue
		case a.Value.IntValue != "":
			m[a.Key] = a.Value.IntValue
		case a.Value.BoolValue != nil:
			m[a.Key] = fmt.Sprint(*a.Value.BoolValue)
		case a.Value.DoubleValue != nil:
			m[a.Key] = fmt.Sprint(*a.Value.DoubleValue)
		default:
			// Arrays and kvlists are not representable here; count, don't guess.
			dropped++
		}
	}
	return m, dropped
}
