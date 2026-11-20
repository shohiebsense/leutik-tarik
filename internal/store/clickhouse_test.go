package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// chStore returns a ClickHouse-backed store, or skips when no endpoint is
// configured, so `go test ./...` stays green without Docker.
func chStore(t *testing.T) *store.ClickHouse {
	t.Helper()
	url := os.Getenv("LEUTIK_CLICKHOUSE_URL")
	if url == "" {
		t.Skip("LEUTIK_CLICKHOUSE_URL not set; skipping ClickHouse integration test")
	}
	s, err := store.NewClickHouse(url)
	if err != nil {
		t.Fatalf("new clickhouse store: %v", err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("clickhouse unreachable: %v", err)
	}
	return s
}

func rec(traceID, spanID, parent, svc, route, method, outcome string, start time.Time, retention time.Duration) model.Record {
	return model.Record{
		Span: model.Span{
			TraceID: traceID, SpanID: spanID, ParentSpanID: parent,
			ServiceName: svc, HTTPRoute: route, HTTPMethod: method,
			Name: method + " " + route, Kind: "server", OperationType: "write",
			StatusCode: statusFor(outcome),
			StartTime:  start, EndTime: start.Add(2 * time.Second), Duration: 2 * time.Second,
			Outcome:    outcome,
			Attributes: map[string]string{"http.request.method": method},
		},
		Decision:     model.Decision{PolicyID: "default", PolicyVersion: 1, Sampled: true, Retention: retention},
		Source:       "otlp-sdk",
		Completeness: model.CompletenessComplete,
		IngestedAt:   time.Now().UTC(),
	}
}

func statusFor(outcome string) int {
	switch outcome {
	case model.OutcomeServerError, model.OutcomeError:
		return 500
	case model.OutcomeClientError:
		return 404
	default:
		return 200
	}
}

func TestClickHouseInsertAndRead(t *testing.T) {
	s := chStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	traceID := "aabbccddeeff00112233445566778899"
	records := []model.Record{
		rec(traceID, "1111111111111111", "", "payment", "/v1/payments/{id}", "POST", model.OutcomeServerError, now.Add(-2*time.Second), 30*24*time.Hour),
		rec(traceID, "2222222222222222", "1111111111111111", "payment", "", "SELECT", model.OutcomeError, now.Add(-1*time.Second), 30*24*time.Hour),
	}
	if err := s.Insert(ctx, records); err != nil {
		t.Fatalf("insert: %v", err)
	}

	spans, err := s.GetTrace(ctx, traceID)
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
	if spans[0].SpanID != "1111111111111111" {
		t.Errorf("spans not ordered by start_time: %+v", spans)
	}
	if spans[0].ServiceName != "payment" || spans[0].Outcome != model.OutcomeServerError {
		t.Errorf("root span = %+v", spans[0])
	}
	if spans[0].Attributes["http.request.method"] != "POST" {
		t.Errorf("attributes lost: %+v", spans[0].Attributes)
	}

	summary := model.Summarize(spans)
	if summary.Outcome != model.OutcomeError || summary.ErrorSpans != 2 {
		t.Errorf("summary = %+v", summary)
	}
}

// A duplicate delivery must not double the rows. The detail page reads the
// trace directly, so that read must be exact.
func TestClickHouseDeduplicatesRetries(t *testing.T) {
	s := chStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	traceID := "00112233445566778899aabbccddeeff"

	r := rec(traceID, "abcdefabcdefabcd", "", "svc", "/r", "GET", model.OutcomeSuccess, now.Add(-time.Second), 48*time.Hour)
	for i := 0; i < 3; i++ {
		if err := s.Insert(ctx, []model.Record{r}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	spans, err := s.GetTrace(ctx, traceID)
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("duplicate deliveries stored %d rows, want 1", len(spans))
	}
}

// The list path must collapse duplicates too, and must respect the time window
// (review M8: time is the primary search axis).
func TestClickHouseListFiltersAndDeduplicates(t *testing.T) {
	s := chStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	in := rec("aaaa0000000000000000000000000001", "1000000000000001", "", "listsvc", "/v1/x", "GET", model.OutcomeSuccess, now.Add(-5*time.Minute), 48*time.Hour)
	out := rec("aaaa0000000000000000000000000002", "1000000000000002", "", "listsvc", "/v1/x", "GET", model.OutcomeSuccess, now.Add(-48*time.Hour), 48*time.Hour)
	for i := 0; i < 2; i++ {
		if err := s.Insert(ctx, []model.Record{in, out}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := s.ListOperations(ctx, store.Query{
		Service: "listsvc",
		From:    now.Add(-time.Hour),
		To:      now,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 operation in window (deduped), got %d", len(got))
	}
	if got[0].TraceID != in.Span.TraceID {
		t.Errorf("wrong operation returned: %s", got[0].TraceID)
	}
}
