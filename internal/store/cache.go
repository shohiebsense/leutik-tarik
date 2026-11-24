// A short TTL in front of the two reads the HTML pages hammer: ListOperations
// and ServiceStats. A live operations page re-asks the identical query on a
// fixed cadence, and every open tab pays the full window scan again — at a
// week of window that is seconds of ClickHouse time per viewer per poll. The
// cache makes N viewers inside one TTL window cost one query.
//
// The machine API and frozen windows never see it: their queries carry
// CacheTTL 0 (or a window that already ended), so what a script or a pinned
// incident view reads is always fresh.
package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// CacheTTLFor returns how long a live view of the given window may serve a
// cached answer. It mirrors the poll cadence: the reader is promised data no
// newer than their refresh interval, and a cached answer older than that would
// make the page lie about being live.
func CacheTTLFor(window time.Duration) time.Duration {
	switch {
	case window <= 15*time.Minute:
		return 5 * time.Second
	case window <= time.Hour:
		return 15 * time.Second
	case window <= 6*time.Hour:
		return time.Minute
	case window <= 24*time.Hour:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// NewCachedReads wraps inner for the UI read path. Both production stores
// implement Stats; a store that does not is passed through unwrapped by the
// server wiring, so the nil check here is just defensive.
func NewCachedReads(inner interface {
	Store
	Stats
}) *CachedReads {
	return &CachedReads{Store: inner, stats: inner, ops: map[string]opsEntry{}, statsCache: map[string]statsEntry{}, now: time.Now}
}

// CachedReads implements Store and Stats, caching only the two whole-window
// reads and delegating everything else.
type CachedReads struct {
	Store
	stats storeStats

	mu         sync.Mutex
	ops        map[string]opsEntry
	statsCache map[string]statsEntry
	now        func() time.Time // injectable for tests
}

// storeStats is the Stats interface the wrapper delegates to; a named alias so
// the embedded field and the cache fields read apart.
type storeStats = Stats

type opsEntry struct {
	expires time.Time
	spans   []model.Span
}

type statsEntry struct {
	expires time.Time
	stats   []ServiceStat
}

func (c *CachedReads) getOps(key string, now time.Time) ([]model.Span, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.ops[key]
	if !ok {
		return nil, false
	}
	if now.After(e.expires) {
		delete(c.ops, key)
		return nil, false
	}
	return e.spans, true
}

func (c *CachedReads) setOps(key string, spans []model.Span, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ops) >= 256 {
		for k, e := range c.ops {
			if now.After(e.expires) {
				delete(c.ops, k)
			}
		}
	}
	c.ops[key] = opsEntry{expires: now.Add(ttl), spans: spans}
}

func (c *CachedReads) getStats(key string, now time.Time) ([]ServiceStat, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.statsCache[key]
	if !ok {
		return nil, false
	}
	if now.After(e.expires) {
		delete(c.statsCache, key)
		return nil, false
	}
	return e.stats, true
}

func (c *CachedReads) setStats(key string, stats []ServiceStat, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.statsCache) >= 256 {
		for k, e := range c.statsCache {
			if now.After(e.expires) {
				delete(c.statsCache, k)
			}
		}
	}
	c.statsCache[key] = statsEntry{expires: now.Add(ttl), stats: stats}
}

// ListOperations answers from the cache when the caller declared a TTL and the
// same query was answered inside it. The key buckets the window bounds to the
// TTL, so a rolling window's moving to= still hits between polls.
func (c *CachedReads) ListOperations(ctx context.Context, q Query) ([]model.Span, error) {
	if q.CacheTTL <= 0 {
		return c.Store.ListOperations(ctx, q)
	}
	key := fmt.Sprintf("ops|%d|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		q.Limit, b2i(q.IncludeBackground), q.Service, q.Route, q.Method, q.Outcome, q.Text,
		bucketKey(q.From, q.CacheTTL), bucketKey(q.To, q.CacheTTL),
		bucketKey(q.Cursor, q.CacheTTL))
	now := c.now()
	if spans, ok := c.getOps(key, now); ok {
		return spans, nil
	}
	spans, err := c.Store.ListOperations(ctx, q)
	if err != nil {
		return nil, err
	}
	c.setOps(key, spans, q.CacheTTL, now)
	return spans, nil
}

// ServiceStats caches when the window reaches the present (a live view). A
// window that already ended is frozen evidence: it is re-read every time, so a
// pinned incident view never shows a strip older than its own list.
func (c *CachedReads) ServiceStats(ctx context.Context, from, to time.Time) ([]ServiceStat, error) {
	ttl := CacheTTLFor(to.Sub(from))
	now := c.now()
	if to.Before(now.Add(-ttl)) {
		return c.stats.ServiceStats(ctx, from, to)
	}
	key := fmt.Sprintf("stats|%s|%s", bucketKey(from, ttl), bucketKey(to, ttl))
	if stats, ok := c.getStats(key, now); ok {
		return stats, nil
	}
	stats, err := c.stats.ServiceStats(ctx, from, to)
	if err != nil {
		return nil, err
	}
	c.setStats(key, stats, ttl, now)
	return stats, nil
}

func (c *CachedReads) ErrorGroups(ctx context.Context, from, to time.Time, limit int) ([]ErrorGroup, error) {
	return c.stats.ErrorGroups(ctx, from, to, limit)
}

func (c *CachedReads) EndpointStats(ctx context.Context, from, to time.Time, limit int) ([]EndpointStat, error) {
	return c.stats.EndpointStats(ctx, from, to, limit)
}

func (c *CachedReads) HTTPFailures(ctx context.Context, service string, from, to time.Time, limit int) ([]HTTPFailure, error) {
	return c.stats.HTTPFailures(ctx, service, from, to, limit)
}

// bucketKey truncates an instant to the TTL so consecutive polls of a rolling
// window share a key.
func bucketKey(t time.Time, ttl time.Duration) string {
	if t.IsZero() {
		return "zero"
	}
	return t.UTC().Truncate(ttl).Format(time.RFC3339Nano)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
