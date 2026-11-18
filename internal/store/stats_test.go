package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func mkRec(tid, sid, route string, d time.Duration, outcome string, at time.Time) model.Record {
	return model.Record{Span: model.Span{
		TraceID: tid, SpanID: sid, ServiceName: "svc", HTTPRoute: route,
		HTTPMethod: "GET", Outcome: outcome,
		StartTime: at, EndTime: at.Add(d), Duration: d,
	}}
}

// The ranking the whole page exists for: total time, not worst single request.
func TestEndpointStatsRanksByTotalTime(t *testing.T) {
	s := store.NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()

	// slow-once: 5s × 1 = 5s total. frequent: 1s × 10 = 10s total.
	var recs []model.Record
	recs = append(recs, mkRec("t1", "s1", "/slow-once", 5*time.Second, model.OutcomeSuccess, now))
	for i := 0; i < 10; i++ {
		recs = append(recs, mkRec("f"+string(rune('a'+i)), string(rune('a'+i))+"2", "/frequent", time.Second, model.OutcomeSuccess, now))
	}
	if err := s.Insert(ctx, recs); err != nil {
		t.Fatal(err)
	}

	got, err := s.EndpointStats(ctx, now.Add(-time.Minute), now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 endpoints, got %d", len(got))
	}
	if got[0].Route != "/frequent" {
		t.Errorf("top endpoint = %q, want /frequent (10s total beats 5s total)", got[0].Route)
	}
	if got[0].Total != 10*time.Second {
		t.Errorf("total = %v, want 10s", got[0].Total)
	}
	if got[1].Route != "/slow-once" {
		t.Errorf("second = %q", got[1].Route)
	}
	if got[1].P95 > 6*time.Second || got[1].P95 < 4*time.Second {
		t.Errorf("p95 = %v, expected ~5s", got[1].P95)
	}
}

func TestEndpointStatsCountsErrors(t *testing.T) {
	s := store.NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()
	_ = s.Insert(ctx, []model.Record{
		mkRec("e1", "a1", "/x", time.Second, model.OutcomeServerError, now),
		mkRec("e2", "a2", "/x", time.Second, model.OutcomeSuccess, now),
	})
	got, _ := s.EndpointStats(ctx, now.Add(-time.Minute), now.Add(time.Minute), 10)
	if len(got) != 1 || got[0].Count != 2 || got[0].Errors != 1 {
		t.Fatalf("stats = %+v", got)
	}
	if rate := got[0].ErrorRate(); rate != 0.5 {
		t.Errorf("error rate = %v, want 0.5", rate)
	}
}

// A background Redis call has no parent. It must not be listed as an operation,
// or 254 of them bury the two real HTTP requests.
func TestOperationsExcludeClientRoots(t *testing.T) {
	s := store.NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()

	server := model.Record{Span: model.Span{
		TraceID: "t1", SpanID: "s1", ServiceName: "svc", Kind: "server",
		HTTPRoute: "/healthcheck", HTTPMethod: "GET", Outcome: model.OutcomeSuccess,
		StartTime: now, EndTime: now.Add(time.Millisecond), Duration: time.Millisecond,
	}}
	redisRoot := model.Record{Span: model.Span{
		TraceID: "t2", SpanID: "s2", ServiceName: "svc", Kind: "client", Name: "get",
		Outcome: model.OutcomeSuccess, StartTime: now, EndTime: now.Add(time.Millisecond),
	}}
	if err := s.Insert(ctx, []model.Record{server, redisRoot}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListOperations(ctx, store.Query{From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SpanID != "s1" {
		t.Fatalf("operations = %d (want only the server span): %+v", len(got), got)
	}
}

// Instrumenting workers means every cron tick is a root span. The operations
// list must show requests by default, or 1000 worker spans bury the 3 requests.
func TestOperationsDefaultToEntryPoints(t *testing.T) {
	s := store.NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()

	req := model.Record{Span: model.Span{
		TraceID: "t1", SpanID: "s1", Kind: "server", ServiceName: "svc",
		HTTPRoute: "/abt/scans", HTTPMethod: "POST", StatusCode: 200,
		Outcome: model.OutcomeSuccess, StartTime: now, EndTime: now.Add(time.Millisecond), Duration: time.Millisecond,
	}}
	worker := model.Record{Span: model.Span{
		TraceID: "t2", SpanID: "s2", Kind: "internal", ServiceName: "svc",
		Name: "ScanUnitWorker.maybePurge", Outcome: model.OutcomeOK,
		StartTime: now, EndTime: now.Add(time.Microsecond), Duration: time.Microsecond,
	}}
	if err := s.Insert(ctx, []model.Record{req, worker}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.ListOperations(ctx, store.Query{From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	if len(got) != 1 || got[0].SpanID != "s1" {
		t.Fatalf("default list = %d rows (want only the request)", len(got))
	}

	all, _ := s.ListOperations(ctx, store.Query{From: now.Add(-time.Minute), To: now.Add(time.Minute), IncludeBackground: true})
	if len(all) != 2 {
		t.Fatalf("background list = %d rows, want 2", len(all))
	}
}

// A viewer pastes a media token, not a field name. The free-text filter has to
// find the span that carried it in its captured body.
func TestMemoryTextSearchFindsAttributeValues(t *testing.T) {
	m := store.NewMemory()
	_ = m.Insert(context.Background(), []model.Record{
		{Span: model.Span{TraceID: "aa", SpanID: "s1", ServiceName: "fare-membership",
			HTTPRoute: "/abt/scans", Kind: "server", StartTime: time.Now(),
			Attributes: map[string]string{"http.request.body": `{"abt_token":"21616513243938675087"}`}}},
		{Span: model.Span{TraceID: "bb", SpanID: "s2", ServiceName: "fare-membership",
			HTTPRoute: "/abt/scans", Kind: "server", StartTime: time.Now(),
			Attributes: map[string]string{"http.request.body": `{"abt_token":"9999"}`}}},
	})

	got, err := m.ListOperations(context.Background(), store.Query{Text: "21616513243938675087"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TraceID != "aa" {
		t.Fatalf("text search returned %d spans, want only aa", len(got))
	}

	// A trace id pasted into the same box works too.
	if got, _ := m.ListOperations(context.Background(), store.Query{Text: "bb"}); len(got) != 1 || got[0].TraceID != "bb" {
		t.Fatalf("trace-id search returned %d spans", len(got))
	}
}
