package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/otlpjson"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
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
	records, stats := normalize(rs, time.Now(), nil)
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
	if n < 3 {
		t.Errorf("redacted count = %d, want >= 3", n)
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
		{500, 2, "server_error"}, // HTTP class is more specific than OTLP error
		{0, 2, "error"},          // non-HTTP error span keeps the generic error
		{0, 0, "ok"},             // OTLP UNSET: completed, nothing reported an error
		{0, 1, "success"},        // explicit OK is a reported success
		{503, 0, "server_error"}, // 5xx is a bad request's opposite: the server failed
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
	records, _ := normalize(raw, time.Now(), nil)
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
	records, _ := normalize(raw, time.Now(), nil)
	if records[0].Completeness != "complete" || records[0].Source != "otlp-sdk" {
		t.Errorf("defaults = %q/%q", records[0].Source, records[0].Completeness)
	}
}

// The policy engine, not a hardcoded table, decides retention now, and the
// decision is recorded per record.
func TestRetentionComesFromPolicy(t *testing.T) {
	p, err := policy.Parse([]byte(`
version: 7
defaults: {trace: 14d}
rules:
  - name: errors-live-long
    priority: 100
    match: {outcome: [server_error]}
    keep: {trace: 90d}
`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []rawSpan{
		{resource: map[string]string{"service.name": "api"}, attrs: map[string]string{"http.response.status_code": "500"}, traceID: "aa", spanID: "b1"},
		{resource: map[string]string{"service.name": "api"}, attrs: map[string]string{"http.response.status_code": "200"}, traceID: "aa", spanID: "b2"},
	}
	records, _ := normalize(raw, time.Now(), policy.New(p))
	if records[0].Decision.Retention != 90*24*time.Hour || records[0].Decision.PolicyID != "errors-live-long" {
		t.Errorf("error span decision = %+v", records[0].Decision)
	}
	if records[1].Decision.Retention != 14*24*time.Hour || records[1].Decision.PolicyID != "default" {
		t.Errorf("success span decision = %+v", records[1].Decision)
	}
	if records[0].Decision.PolicyVersion != 7 {
		t.Errorf("policy version = %d, want 7", records[0].Decision.PolicyVersion)
	}
}

// Instrumentation libraries emit full SQL by default. Persisting it would put
// member phone numbers and transaction ids into the evidence store, so the
// value must be dropped on the way in.
func TestSQLTextIsNeverStored(t *testing.T) {
	attrs := map[string]string{
		"db.query.text":        "SELECT * FROM customers WHERE primary_phone_number='62844420260702'",
		"db.statement":         "UPDATE orders SET fare=1000 WHERE id='trx-99'",
		"db.query.parameter.0": "62844420260702",
		"db.system":            "postgresql",
		"db.operation.name":    "SELECT",
		"db.query.summary":     "SELECT customers",
		"http.request.body":    `{"pin":"1234"}`,
	}
	n := redact(attrs)
	if attrs["db.query.text"] != redactedPlaceholder {
		t.Errorf("db.query.text stored: %q", attrs["db.query.text"])
	}
	if attrs["db.statement"] != redactedPlaceholder {
		t.Errorf("db.statement stored: %q", attrs["db.statement"])
	}
	if attrs["db.query.parameter.0"] != redactedPlaceholder {
		t.Errorf("db.query.parameter.0 stored: %q", attrs["db.query.parameter.0"])
	}
	// The body is kept on purpose; its credential fields are not.
	if body := attrs["http.request.body"]; !strings.Contains(body, "[REDACTED]") || strings.Contains(body, "1234") {
		t.Errorf("request body not scrubbed: %q", body)
	}
	// Useful, non-sensitive context must survive.
	if attrs["db.system"] != "postgresql" || attrs["db.query.summary"] != "SELECT customers" {
		t.Errorf("safe attributes were removed: %+v", attrs)
	}
	if n < 3 {
		t.Errorf("redacted count = %d, want >= 3", n)
	}
}

// SDKs still emit the old semantic conventions (http.method, http.status_code).
// Reading only the new names silently produced empty methods and a zero status,
// which made every successful request look like "unknown".
func TestSemconvGenerationsBothWork(t *testing.T) {
	old := []rawSpan{{
		resource: map[string]string{"service.name": "api"},
		attrs: map[string]string{
			"http.method":      "POST",
			"http.status_code": "503",
			"http.route":       "/v1/payments/:id",
			"net.host.name":    "api.internal",
			"net.peer.name":    "postgres",
		},
		traceID: "aa", spanID: "bb",
	}}
	records, _ := normalize(old, time.Now(), nil)
	s := records[0].Span
	if s.HTTPMethod != "POST" {
		t.Errorf("method = %q, want POST (old semconv)", s.HTTPMethod)
	}
	if s.StatusCode != 503 {
		t.Errorf("status = %d, want 503", s.StatusCode)
	}
	if s.Outcome != model.OutcomeServerError {
		t.Errorf("outcome = %q, want server_error", s.Outcome)
	}
	if s.HostName != "api.internal" || s.PeerService != "postgres" {
		t.Errorf("host/peer = %q/%q", s.HostName, s.PeerService)
	}
	if s.OperationType != "write" {
		t.Errorf("operation type = %q", s.OperationType)
	}

	// New names must keep working too.
	nw := []rawSpan{{
		resource: map[string]string{},
		attrs: map[string]string{
			"http.request.method":       "GET",
			"http.response.status_code": "200",
		},
		traceID: "cc", spanID: "dd",
	}}
	got := records[0].Span
	_ = got
	r2, _ := normalize(nw, time.Now(), nil)
	if r2[0].Span.HTTPMethod != "GET" || r2[0].Span.StatusCode != 200 {
		t.Errorf("new semconv broke: %+v", r2[0].Span)
	}
}

// pgx emits a pool.acquire span per query: 30 queries meant 30 rows that only
// say a connection was taken. Dropped at ingest, counted, never stored.
func TestPoolSpansDropped(t *testing.T) {
	raw := []rawSpan{
		{resource: map[string]string{"service.name": "svc"}, name: "pool.acquire", traceID: "aa", spanID: "s1"},
		{resource: map[string]string{"service.name": "svc"}, name: "SELECT", traceID: "aa", spanID: "s2",
			attrs: map[string]string{"db.system": "postgresql", "db.query.text": "SELECT id FROM customers WHERE id = $1"}},
	}
	records, stats := normalize(raw, time.Now(), nil)
	if len(records) != 1 {
		t.Fatalf("want 1 stored span, got %d", len(records))
	}
	if stats.droppedSpans != 1 {
		t.Errorf("dropped = %d, want 1", stats.droppedSpans)
	}
}

// "SELECT" tells a reader nothing; the statement's table does.
func TestDescriptiveDBNames(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"SELECT", "SELECT id FROM customers WHERE id = $1", "SELECT customers"},
		{"INSERT", "INSERT INTO abt_scan_units (x) VALUES ($1)", "INSERT abt_scan_units"},
		{"UPDATE", `UPDATE "payment_journeys" SET status = 'X'`, "UPDATE payment_journeys"},
		// A CTE reports the table it reads, which is the useful part.
		{"WITH", "WITH recent AS (SELECT * FROM fare_version) SELECT 1", "WITH fare_version"},
		{"GET /x", "SELECT 1 FROM ignored", "GET /x"}, // non-SQL names untouched
	}
	for _, c := range cases {
		attrs := map[string]string{"db.system": "postgresql"}
		if c.text != "" {
			attrs["db.query.text"] = c.text
		}
		if got := descriptiveName(c.name, attrs); got != c.want {
			t.Errorf("descriptiveName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// db.query.summary wins when the instrumentation supplies one.
	got := descriptiveName("SELECT", map[string]string{"db.query.summary": "SELECT customers"})
	if got != "SELECT customers" {
		t.Errorf("summary not preferred: %q", got)
	}
	// Long names are truncated with an ellipsis, never silently.
	long := descriptiveName("SELECT", map[string]string{"db.query.summary": strings.Repeat("x", 100)})
	if len(long) > 64 || !strings.HasSuffix(long, "…") {
		t.Errorf("long name not truncated: %q", long)
	}
}

// The payload is kept; credentials inside it are not.
func TestBodyCapturedButSecretsScrubbed(t *testing.T) {
	body := `{"username":"62844420260702","password":"hunter2","pin":"1234","note":"keep me"}`
	scrubbed, changed := scrubBody(body)
	if !changed {
		t.Fatal("expected a scrub")
	}
	for _, secret := range []string{"hunter2", `"1234"`} {
		if strings.Contains(scrubbed, secret) {
			t.Errorf("%s survived: %s", secret, scrubbed)
		}
	}
	for _, keep := range []string{"62844420260702", "keep me"} {
		if !strings.Contains(scrubbed, keep) {
			t.Errorf("%s was lost: %s", keep, scrubbed)
		}
	}

	// Form encoding too.
	form, _ := scrubBody("username=a&password=hunter2&ok=1")
	if strings.Contains(form, "hunter2") {
		t.Errorf("form password survived: %s", form)
	}

	// The body attribute is no longer blanked wholesale.
	raw := []rawSpan{{resource: map[string]string{}, attrs: map[string]string{
		"http.request.body": `{"password":"hunter2"}`,
	}, traceID: "aa", spanID: "bb"}}
	records, _ := normalize(raw, time.Now(), nil)
	got := records[0].Span.Attributes["http.request.body"]
	if !strings.Contains(got, "[REDACTED]") || strings.Contains(got, "hunter2") {
		t.Errorf("body attribute = %q", got)
	}
}

// An allowlisted account's own test payload is more useful readable than
// scrubbed; everyone else's stays scrubbed.
func TestRevealBodyAllowlist(t *testing.T) {
	old := revealBodyFor
	defer func() { revealBodyFor = old }()

	body := `{"username":"shohieb","password":"hunter2"}`

	revealBodyFor = map[string]bool{}
	if out, _ := scrubBody(body); strings.Contains(out, "hunter2") {
		t.Fatal("scrubbing disabled by default")
	}

	revealBodyFor = map[string]bool{"shohieb": true}
	attrs := map[string]string{"enduser.id": "shohieb", "http.request.body": body}
	redact(attrs)
	if !strings.Contains(attrs["http.request.body"], "hunter2") {
		t.Errorf("allowlisted body still scrubbed: %q", attrs["http.request.body"])
	}

	// A different account is still scrubbed.
	other := map[string]string{"enduser.id": "someone-else", "http.request.body": body}
	redact(other)
	if strings.Contains(other["http.request.body"], "hunter2") {
		t.Errorf("non-allowlisted body leaked: %q", other["http.request.body"])
	}
}
