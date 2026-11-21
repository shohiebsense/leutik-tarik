// Query API contract: JSON views guarded like ingest, shaped for scripts.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func seedRootSpan(t *testing.T, mem *store.Memory, traceID, route, outcome string, start time.Time) {
	t.Helper()
	err := mem.Insert(t.Context(), []model.Record{{
		Span: model.Span{
			TraceID: traceID, SpanID: "s1", ServiceName: "fare-membership",
			Name: "POST /abt/scans", Kind: "server", HTTPMethod: "POST",
			HTTPRoute: route, StatusCode: 202, StartTime: start,
			EndTime: start.Add(50 * time.Millisecond), Duration: 50 * time.Millisecond,
			Outcome: outcome,
		},
	}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestQueryOperationsFiltersAndShapes(t *testing.T) {
	mem := store.NewMemory()
	now := time.Now().UTC().Truncate(time.Second)
	seedRootSpan(t, mem, "tr-scans", "/abt/scans", "ok", now.Add(-time.Minute))
	seedRootSpan(t, mem, "tr-trace", "/abt/trace", "error", now.Add(-2*time.Minute))

	h := NewWithOptions(Options{Store: mem})

	req := httptest.NewRequest(http.MethodGet, "/v1/operations?service=fare-membership&route=abt&last=15m", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Count      int        `json:"count"`
		Operations []spanJSON `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Count != 2 || len(got.Operations) != 2 {
		t.Fatalf("want 2 operations, got %d", got.Count)
	}
	op := got.Operations[0]
	if op.Service != "fare-membership" || op.Route == "" || op.DurationMs != 50 || op.Start == "" {
		t.Fatalf("shaped span wrong: %+v", op)
	}

	// outcome filter narrows to the failing operation
	req = httptest.NewRequest(http.MethodGet, "/v1/operations?outcome=error&last=15m", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Count != 1 || got.Operations[0].Route != "/abt/trace" {
		t.Fatalf("outcome filter wrong: count=%d", got.Count)
	}

	// bad window is a 400, not a 500
	req = httptest.NewRequest(http.MethodGet, "/v1/operations?last=nonsense", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad last= should 400, got %d", rec.Code)
	}
}

func TestQueryTraceNotFoundAndFound(t *testing.T) {
	mem := store.NewMemory()
	now := time.Now().UTC()
	seedRootSpan(t, mem, "tr-scans", "/abt/scans", "ok", now)
	h := NewWithOptions(Options{Store: mem})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/traces/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing trace should 404, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/traces/tr-scans", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("found trace should 200, got %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Count int        `json:"count"`
		Spans []spanJSON `json:"spans"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Count != 1 || got.Spans[0].TraceID != "tr-scans" {
		t.Fatalf("trace spans wrong: %+v", got)
	}
}
