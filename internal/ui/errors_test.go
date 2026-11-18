package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func tracePage(t *testing.T, spans []model.Span) string {
	t.Helper()
	m := store.NewMemory()
	var recs []model.Record
	for _, s := range spans {
		recs = append(recs, model.Record{Span: s})
	}
	if err := m.Insert(nil, recs); err != nil { //nolint:staticcheck // test store ignores ctx
		t.Fatal(err)
	}
	u := UI{Store: m, now: time.Now}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/traces/t1", nil)
	req.SetPathValue("traceID", "t1")
	u.Trace(rec, req)
	return rec.Body.String()
}

// A 401 with no message from the service must still explain itself, and must say
// that the explanation is an inference rather than evidence.
func TestClientErrorShowsInferredCause(t *testing.T) {
	now := time.Now()
	body := tracePage(t, []model.Span{
		{TraceID: "t1", SpanID: "s1", Name: "Handler.Login", Kind: "server",
			HTTPRoute: "/acm/login", HTTPMethod: "POST", StatusCode: 401,
			Outcome: model.OutcomeClientError, StartTime: now, Duration: 5 * time.Millisecond},
		{TraceID: "t1", SpanID: "s2", ParentSpanID: "s1", Name: "Service.Login",
			Kind: "internal", Outcome: "ok", StartTime: now, Duration: 4 * time.Millisecond},
	})

	if !strings.Contains(body, `class="errbox"`) || !strings.Contains(body, "401") {
		t.Fatal("no error panel for the client error")
	}
	if !strings.Contains(body, "authentication failed") {
		t.Error("no cause derived from 401")
	}
	if !strings.Contains(body, "Inferred from the status code") {
		t.Error("the inference is not labelled as one")
	}
}

// When the service does send a reason, that is the headline - not our guess.
func TestReportedReasonWins(t *testing.T) {
	now := time.Now()
	body := tracePage(t, []model.Span{
		{TraceID: "t1", SpanID: "s1", Name: "Handler.Pay", Kind: "server", HTTPRoute: "/v1/payments",
			StatusCode: 500, Outcome: model.OutcomeServerError, StatusMessage: "settlement gateway timeout",
			StartTime: now, Duration: time.Second},
	})
	if !strings.Contains(body, "settlement gateway timeout") {
		t.Error("the service's own message is not shown")
	}
	if !strings.Contains(body, "Reported by the service") {
		t.Error("the reported reason is not marked as reported")
	}
	if strings.Contains(body, "Inferred from the status code") {
		t.Error("showed a guess when the service said what happened")
	}
}

// A healthy trace gets no panel.
func TestSuccessfulTraceHasNoErrorPanel(t *testing.T) {
	now := time.Now()
	body := tracePage(t, []model.Span{
		{TraceID: "t1", SpanID: "s1", Name: "Handler.List", Kind: "server", HTTPRoute: "/v1/x",
			StatusCode: 200, Outcome: model.OutcomeSuccess, StartTime: now, Duration: time.Millisecond},
	})
	if strings.Contains(body, `class="errbox"`) {
		t.Error("error panel on a successful trace")
	}
}

func TestInferCause(t *testing.T) {
	cases := map[int]string{
		400: "malformed", 401: "authentication failed", 403: "not allowed",
		404: "no such route", 409: "conflicts", 422: "validation", 429: "rate limited",
		500: "failed while handling", 503: "failed while handling",
	}
	for status, want := range cases {
		if got := inferCause(status, model.OutcomeClientError); !strings.Contains(got, want) {
			t.Errorf("inferCause(%d) = %q, want it to mention %q", status, got, want)
		}
	}
}
