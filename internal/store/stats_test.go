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
