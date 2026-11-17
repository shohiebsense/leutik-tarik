package server_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/server"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

const traceJSON = `{
  "resourceSpans": [{
    "resource": {"attributes": [
      {"key":"service.name","value":{"stringValue":"payment"}},
      {"key":"service.version","value":{"stringValue":"v1.2.3"}},
      {"key":"deployment.environment","value":{"stringValue":"dev"}}
    ]},
    "scopeSpans": [{"spans": [
      {
        "traceId":"0123456789abcdef0123456789abcdef",
        "spanId":"aaaaaaaaaaaaaaaa",
        "parentSpanId":"",
        "name":"POST /v1/payments/{id}",
        "kind":2,
        "startTimeUnixNano":"1000000000",
        "endTimeUnixNano":"3000000000",
        "attributes":[
          {"key":"http.request.method","value":{"stringValue":"POST"}},
          {"key":"http.route","value":{"stringValue":"/v1/payments/{id}"}},
          {"key":"http.response.status_code","value":{"intValue":"500"}},
          {"key":"error.type","value":{"stringValue":"db_timeout"}}
        ],
        "status":{"code":2,"message":"query timeout"}
      },
      {
        "traceId":"0123456789abcdef0123456789abcdef",
        "spanId":"bbbbbbbbbbbbbbbb",
        "parentSpanId":"aaaaaaaaaaaaaaaa",
        "name":"db.query SELECT",
        "kind":3,
        "startTimeUnixNano":"1100000000",
        "endTimeUnixNano":"2900000000",
        "attributes":[
          {"key":"db.system","value":{"stringValue":"postgresql"}},
          {"key":"db.operation.name","value":{"stringValue":"SELECT"}}
        ],
        "status":{"code":2,"message":"statement timeout"}
      }
    ]}]
  }]
}`

func TestIngestThenListAndDetail(t *testing.T) {
	now := time.Now()
	s := store.NewMemory()
	h := server.New(s)

	// Ingest. Timestamps are relative to now: the operations list defaults to a
	// 24h window, so a 1970 fixture would (correctly) return nothing.
	nanos := func(d time.Duration) string { return strconv.Quote(fmt.Sprint(now.Add(-d).UnixNano())) }
	body := strings.NewReplacer(
		`"1000000000"`, nanos(3*time.Second),
		`"1100000000"`, nanos(2900*time.Millisecond),
		`"3000000000"`, nanos(1*time.Second),
		`"2900000000"`, nanos(1100*time.Millisecond),
	).Replace(traceJSON)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d body=%s", rec.Code, rec.Body.String())
	}

	// Operations list.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/operations", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec2.Code)
	}
	for _, want := range []string{"payment", "/v1/payments/{id}", "500", "server_error"} {
		if !strings.Contains(rec2.Body.String(), want) {
			t.Errorf("operations page missing %q", want)
		}
	}

	// Trace detail (waterfall).
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/traces/0123456789abcdef0123456789abcdef", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "db.query SELECT") {
		t.Errorf("detail page missing child span")
	}

	// Unknown trace.
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/traces/deadbeef", nil))
	if rec4.Code != http.StatusNotFound {
		t.Errorf("unknown trace status = %d, want 404", rec4.Code)
	}
}
