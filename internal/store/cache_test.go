package store

import (
	"context"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// countingStore counts the reads that pass through, so a test can tell a cache
// hit from a miss.
type countingStore struct {
	Store
	Stats
	lists      int
	statsCalls int
}

func (c *countingStore) ListOperations(ctx context.Context, q Query) ([]model.Span, error) {
	c.lists++
	return c.Store.ListOperations(ctx, q)
}

func (c *countingStore) ServiceStats(ctx context.Context, from, to time.Time) ([]ServiceStat, error) {
	c.statsCalls++
	return c.Stats.ServiceStats(ctx, from, to)
}

func cachedTestRecord(start time.Time, traceID string) model.Record {
	return model.Record{Span: model.Span{
		TraceID: traceID, SpanID: traceID + "-s",
		StartTime: start, EndTime: start.Add(time.Second),
		Kind: "server", HTTPRoute: "/x", ServiceName: "svc",
	}}
}

func TestCachedReads_ListOperationsHitThenExpiry(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	inner := &countingStore{Store: mem, Stats: mem}
	now := time.Now()
	if err := mem.Insert(ctx, []model.Record{cachedTestRecord(now, "t1")}); err != nil {
		t.Fatal(err)
	}

	c := NewCachedReads(inner)
	current := now
	c.now = func() time.Time { return current }

	q := Query{From: now.Add(-time.Minute), To: now, Limit: 10, CacheTTL: 5 * time.Second}
	if _, err := c.ListOperations(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListOperations(ctx, q); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 1 {
		t.Fatalf("second call inside the TTL went through: %d inner reads", inner.lists)
	}

	current = now.Add(6 * time.Second)
	if _, err := c.ListOperations(ctx, q); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 2 {
		t.Fatalf("expired entry still served: %d inner reads", inner.lists)
	}
}

func TestCachedReads_ZeroTTLReadsThrough(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	inner := &countingStore{Store: mem, Stats: mem}
	now := time.Now()
	if err := mem.Insert(ctx, []model.Record{cachedTestRecord(now, "t1")}); err != nil {
		t.Fatal(err)
	}

	c := NewCachedReads(inner)
	c.now = func() time.Time { return now }

	q := Query{From: now.Add(-time.Minute), To: now, Limit: 10}
	for i := 1; i <= 3; i++ {
		if _, err := c.ListOperations(ctx, q); err != nil {
			t.Fatal(err)
		}
		if inner.lists != i {
			t.Fatalf("call %d was served from a cache it must not use", i)
		}
	}
}

func TestCachedReads_DistinctFiltersDistinctKeys(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	inner := &countingStore{Store: mem, Stats: mem}
	now := time.Now()
	if err := mem.Insert(ctx, []model.Record{cachedTestRecord(now, "t1")}); err != nil {
		t.Fatal(err)
	}

	c := NewCachedReads(inner)
	c.now = func() time.Time { return now }
	base := Query{From: now.Add(-time.Minute), To: now, Limit: 10, CacheTTL: time.Minute}

	a, b := base, base
	a.Service, b.Service = "one", "two"
	for _, q := range []Query{a, b, a, b} {
		if _, err := c.ListOperations(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if inner.lists != 2 {
		t.Fatalf("distinct filters shared a cache key: %d inner reads for 2 queries", inner.lists)
	}
}

func TestCachedReads_ServiceStatsLiveCachedFrozenFresh(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	inner := &countingStore{Store: mem, Stats: mem}
	now := time.Now()
	if err := mem.Insert(ctx, []model.Record{cachedTestRecord(now, "t1")}); err != nil {
		t.Fatal(err)
	}

	c := NewCachedReads(inner)
	current := now
	c.now = func() time.Time { return current }

	// A window that reaches the present is live: answered once, then cached.
	liveFrom, liveTo := now.Add(-24*time.Hour), now
	for i := 1; i <= 2; i++ {
		if _, err := c.ServiceStats(ctx, liveFrom, liveTo); err != nil {
			t.Fatal(err)
		}
	}
	if inner.statsCalls != 1 {
		t.Fatalf("live ServiceStats not cached: %d inner reads", inner.statsCalls)
	}

	// A window that already ended is frozen evidence: always read fresh.
	frozenFrom, frozenTo := now.Add(-26*time.Hour), now.Add(-2*time.Hour)
	if _, err := c.ServiceStats(ctx, frozenFrom, frozenTo); err != nil {
		t.Fatal(err)
	}
	if inner.statsCalls != 2 {
		t.Fatalf("frozen window was served from cache: %d inner reads", inner.statsCalls)
	}
}

func TestCacheTTLFor(t *testing.T) {
	cases := []struct {
		window time.Duration
		want   time.Duration
	}{
		{15 * time.Minute, 5 * time.Second},
		{time.Hour, 15 * time.Second},
		{6 * time.Hour, time.Minute},
		{24 * time.Hour, 5 * time.Minute},
		{7 * 24 * time.Hour, 15 * time.Minute},
	}
	for _, c := range cases {
		if got := CacheTTLFor(c.window); got != c.want {
			t.Errorf("CacheTTLFor(%s) = %s, want %s", c.window, got, c.want)
		}
	}
}
