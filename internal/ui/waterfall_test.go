package ui

import (
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

func sp(id, parent string, start time.Time, name string) model.Span {
	return model.Span{SpanID: id, ParentSpanID: parent, Name: name,
		StartTime: start, EndTime: start.Add(time.Millisecond), Duration: time.Millisecond}
}

// The order must be the call hierarchy, not the clock: a query interleaved by
// start time would appear before the function that issued it.
func TestOrderedRowsFollowTheCallTree(t *testing.T) {
	base := time.Now()
	spans := []model.Span{
		sp("root", "", base, "handler.GetCustomer"),
		sp("svc1", "root", base.Add(10*time.Millisecond), "service.GetCustomer"),
		sp("repo1", "svc1", base.Add(20*time.Millisecond), "repo.GetCustomerByID"),
		sp("sql1", "repo1", base.Add(30*time.Millisecond), "SELECT"),
		// A sibling of svc1 that STARTS EARLIER than repo1 but must be listed
		// after the whole svc1 subtree.
		sp("svc2", "root", base.Add(5*time.Millisecond), "service.GetProfiles"),
		sp("sql2", "svc2", base.Add(40*time.Millisecond), "SELECT"),
	}
	rows := orderedRows(spans, base)
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Span.Name
	}
	// Siblings are ordered by start time (that is how a timeline reads), but a
	// subtree must never be interleaved with its sibling: the point of the test.
	want := []string{
		"handler.GetCustomer",
		"service.GetProfiles", // starts earlier, so first
		"SELECT",              // its query, immediately after it
		"service.GetCustomer",
		"repo.GetCustomerByID",
		"SELECT", // nested under repo, not next to the other SELECT
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order = %v\nwant   %v", got, want)
		}
	}
	// Depth expresses nesting so the page can indent it.
	if rows[2].Depth != 2 || rows[5].Depth != 3 {
		t.Errorf("depths = %d/%d, want 2/3", rows[2].Depth, rows[5].Depth)
	}
	// A query must directly follow the function above it, never float away.
	for i, r := range rows {
		if r.Span.Name == "SELECT" && i > 0 {
			parent := rows[i-1]
			if parent.Span.SpanID != r.Span.ParentSpanID {
				t.Errorf("SELECT at %d does not follow its caller (prev=%s)", i, parent.Span.Name)
			}
		}
	}
}

func TestOrderedRowsSurfacesOrphans(t *testing.T) {
	base := time.Now()
	spans := []model.Span{
		sp("root", "", base, "handler.X"),
		sp("lost", "missing-parent", base.Add(time.Millisecond), "orphan.Span"),
	}
	if rows := orderedRows(spans, base); len(rows) != 2 {
		t.Fatalf("orphan dropped: %d rows", len(rows))
	}
}
