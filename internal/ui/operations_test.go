package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func TestPollIntervalFor(t *testing.T) {
	cases := []struct {
		window time.Duration
		want   int
	}{
		{15 * time.Minute, 5},
		{time.Hour, 15},
		{6 * time.Hour, 60},
		{24 * time.Hour, 300},
		{7 * 24 * time.Hour, 900},
	}
	for _, c := range cases {
		if got := pollIntervalFor(c.window); got != c.want {
			t.Errorf("pollIntervalFor(%s) = %ds, want %ds", c.window, got, c.want)
		}
	}
}

// hrefBefore extracts the href of the anchor whose label appears at labelIdx,
// undoing the attribute escaping the template applies.
func hrefBefore(page string, label string) string {
	i := strings.Index(page, label)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(page[:i], `href="`)
	if start < 0 {
		return ""
	}
	start += len(`href="`)
	end := strings.Index(page[start:], `"`)
	return strings.ReplaceAll(page[start:start+end], "&amp;", "&")
}

// operationsHandler mounts the operations routes on a mux, so the handler sees
// the path value it would see in production.
func operationsHandler(u UI) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/operations", u.Operations)
	mux.HandleFunc("/operations/{service}", u.Operations)
	return mux
}

func TestOperationsCursorPaging(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	now := time.Now().UTC()
	var recs []model.Record
	for i := 0; i <= store.MaxLimit; i++ {
		start := now.Add(-time.Duration(i+1) * time.Second)
		recs = append(recs, model.Record{Span: model.Span{
			TraceID: fmt.Sprintf("t%04d", i), SpanID: fmt.Sprintf("s%04d", i),
			StartTime: start, EndTime: start.Add(time.Millisecond),
			Kind: "server", HTTPRoute: "/x", ServiceName: "svc",
		}})
	}
	if err := mem.Insert(ctx, recs); err != nil {
		t.Fatal(err)
	}
	u := UI{Store: mem, now: func() time.Time { return now }}
	mux := operationsHandler(u)

	get := func(url string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: status %d", url, w.Code)
		}
		return w.Body.String()
	}

	// Page 1: a full page offers an older link.
	page1 := get("/operations/svc?last=1h")
	if got := strings.Count(page1, "open trace "); got != store.MaxLimit {
		t.Fatalf("page 1 rows = %d, want %d", got, store.MaxLimit)
	}
	olderURL := hrefBefore(page1, "older →")
	if olderURL == "" || !strings.Contains(olderURL, "cursor=") {
		t.Fatalf("page 1 has no cursor link (olderURL=%q)", olderURL)
	}

	// Page 2: exactly the remainder, with a way back to the top and no older
	// link (the page is not full).
	page2 := get(olderURL)
	if got := strings.Count(page2, "open trace "); got != 1 {
		t.Fatalf("page 2 rows = %d, want 1", got)
	}
	if newest := hrefBefore(page2, "↑ newest"); newest == "" || strings.Contains(newest, "cursor=") {
		t.Fatalf("newest link wrong: %q", newest)
	}
	if strings.Contains(page2, "older →") {
		t.Fatal("a short page offered an older link")
	}

	// A short first page offers no paging at all.
	short := get("/operations/svc?last=15m&service=nowhere")
	if strings.Contains(short, "older →") {
		t.Fatal("an empty page offered an older link")
	}
}

// A cursor-pinned page is frozen: no live poll, so the reader's place is kept.
func TestOperationsCursorDisablesPoll(t *testing.T) {
	mem := store.NewMemory()
	now := time.Now().UTC()
	u := UI{Store: mem, now: func() time.Time { return now }}

	w := httptest.NewRecorder()
	operationsHandler(u).ServeHTTP(w, httptest.NewRequest("GET", "/operations?last=1h&cursor="+now.Add(-time.Minute).Format(time.RFC3339Nano), nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "data-live-url") {
		t.Fatal("a cursor-pinned page still polls live")
	}
	if !strings.Contains(body, "frozen") {
		t.Fatal("a cursor-pinned page does not say it is frozen")
	}
}
