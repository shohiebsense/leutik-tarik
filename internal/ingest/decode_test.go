package ingest

import (
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
)

func TestDecodeJSON(t *testing.T) {
	raw := `{
	  "resourceSpans": [{
	    "resource": {"attributes": [
	      {"key":"service.name","value":{"stringValue":"payment"}},
	      {"key":"service.version","value":{"stringValue":"v1"}}
	    ]},
	    "scopeSpans": [{"spans": [{
	      "traceId":"0123456789ABCDEF0123456789ABCDEF",
	      "spanId":"AAAAAAAAAAAAAAAA",
	      "parentSpanId":"",
	      "name":"GET /v1/users/{id}",
	      "kind":2,
	      "startTimeUnixNano":"1000000000",
	      "endTimeUnixNano":"2000000000",
	      "attributes":[
	        {"key":"http.request.method","value":{"stringValue":"GET"}},
	        {"key":"http.route","value":{"stringValue":"/v1/users/{id}"}},
	        {"key":"http.response.status_code","value":{"intValue":"200"}}
	      ],
	      "status":{"code":1}
	    }]}]
	  }]
	}`
	rs, err := decodeJSON([]byte(raw))
	if err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	records, stats := normalize(rs, time.Now())
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	s := records[0].Span
	if records[0].Decision.PolicyID == "" {
		t.Error("policy id not recorded")
	}
	if s.ServiceName != "payment" {
		t.Errorf("service = %q", s.ServiceName)
	}
	if s.HTTPRoute != "/v1/users/{id}" {
		t.Errorf("route = %q", s.HTTPRoute)
	}
	if s.StatusCode != 200 {
		t.Errorf("status = %d", s.StatusCode)
	}
	if s.Outcome != "success" {
		t.Errorf("outcome = %q", s.Outcome)
	}
	if s.Duration != 1e9 {
		t.Errorf("duration = %v", s.Duration)
	}
	// IDs are lowercased so a differently-cased duplicate still matches.
	if s.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("traceID not normalised: %q", s.TraceID)
	}
	if stats.droppedAttrs != 0 {
		t.Errorf("dropped = %d, want 0", stats.droppedAttrs)
	}
}

func TestJSONAttrsKeepsScalarsCountsTheRest(t *testing.T) {
	yes := true
	d := 1.5
	as := []otlpjson.Attribute{
		{Key: "a.string", Value: otlpjson.Value{StringValue: "x"}},
		{Key: "a.int", Value: otlpjson.Value{IntValue: "7"}},
		{Key: "a.bool", Value: otlpjson.Value{BoolValue: &yes}},
		{Key: "a.double", Value: otlpjson.Value{DoubleValue: &d}},
		{Key: "a.array"}, // unrepresentable
	}
	m, dropped := jsonAttrs(as)
	for _, k := range []string{"a.string", "a.int", "a.bool", "a.double"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	if m["a.bool"] != "true" {
		t.Errorf("bool = %q", m["a.bool"])
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestRedaction(t *testing.T) {
	attrs := map[string]string{
		"authorization": "Basic c2VjcmV0", "http.request.header.cookie": "sid=abc",
		"x-custom": "Bearer eyJhbGciOi", "db.system": "postgresql",
		"http.response.header.set-cookie": "a=b",
	}
	n := redact(attrs)
	if attrs["authorization"] != redactedPlaceholder {
		t.Errorf("authorization not redacted: %q", attrs["authorization"])
	}
	if attrs["http.request.header.cookie"] != redactedPlaceholder {
		t.Errorf("cookie not redacted: %q", attrs["http.request.header.cookie"])
	}
	if attrs["x-custom"] == "Bearer eyJhbGciOi" {
		t.Errorf("bearer token survived: %q", attrs["x-custom"])
	}
	if attrs["db.system"] != "postgresql" {
		t.Errorf("innocent attribute was altered: %q", attrs["db.system"])
	}
	if n < 4 {
		t.Errorf("redacted count = %d, want >= 4", n)
	}
}

func TestOutcome(t *testing.T) {
	cases := []struct {
		status, otlp int
		want         string
	}{
		{200, 0, "success"},
		{404, 0, "client_error"},
		{500, 0, "server_error"},
		{500, 2, "error"}, // OTLP status ERROR wins
		{0, 2, "error"},   // non-HTTP error span
		{0, 0, "unknown"}, // nothing to classify — say so, don't guess
	}
	for _, c := range cases {
		if got := outcome(c.status, c.otlp); got != c.want {
			t.Errorf("outcome(%d,%d) = %q, want %q", c.status, c.otlp, got, c.want)
		}
	}
}

// A probe that saw one function call must never be reported as a complete
// capture of a request (build prompt §5.2).
func TestCompletenessAndSourceComeFromTheSender(t *testing.T) {
	raw := []rawSpan{{
		resource: map[string]string{"service.name": "probe-target"},
		attrs: map[string]string{
			"leutik.capture.source":       "agent",
			"leutik.capture.completeness": "correlated",
		},
		traceID: "aa", spanID: "bb", name: "uprobe main.fire",
	}}
	records, _ := normalize(raw, time.Now())
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	if records[0].Source != "agent" {
		t.Errorf("source = %q, want agent", records[0].Source)
	}
	if records[0].Completeness != "correlated" {
		t.Errorf("completeness = %q, want correlated", records[0].Completeness)
	}
}

func TestCompleteIsTheDefaultForSDKSpans(t *testing.T) {
	raw := []rawSpan{{resource: map[string]string{}, attrs: map[string]string{}, traceID: "aa", spanID: "bb"}}
	records, _ := normalize(raw, time.Now())
	if records[0].Completeness != "complete" || records[0].Source != "otlp-sdk" {
		t.Errorf("defaults = %q/%q", records[0].Source, records[0].Completeness)
	}
}
