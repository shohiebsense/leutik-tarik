package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// livePage renders /operations with the clock frozen, so the window is a
// deterministic value instead of "whenever the test ran".
func livePage(t *testing.T, target string, now time.Time) string {
	t.Helper()
	u := UI{Store: store.NewMemory(), now: func() time.Time { return now }}
	rec := httptest.NewRecorder()
	u.Operations(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The default window is the last 15 minutes, and it follows the clock: the page
// asks the browser to re-request itself so nobody has to press reload.
func TestOperationsDefaultWindowIsLive(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations", now)

	if !strings.Contains(body, `<meta http-equiv="refresh" content="60">`) {
		t.Error("live page carries no auto-refresh")
	}
	if !strings.Contains(body, `15m window`) {
		t.Errorf("want a 15m window in the footer, got: %s", footerLine(body))
	}
	// from/to rendered from a frozen clock: 14:45..15:00 in the display zone,
	// expressed by the same layout the form posts back.
	if !strings.Contains(body, `from `+expectedStamp(now.Add(-15*time.Minute))) {
		t.Errorf("window start is not now-15m: %s", footerLine(body))
	}
	if !strings.Contains(body, `to `+expectedStamp(now)) {
		t.Errorf("window end is not now: %s", footerLine(body))
	}
}

func expectedStamp(t time.Time) string { return inDisplay(t).Format(inputTimeLayout) }

func footerLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "window ·") {
			return strings.TrimSpace(line)
		}
	}
	return "(no footer line)"
}

// A window pinned in the URL is an investigation, not a live view: refreshing it
// would eventually show a different window than the one being read.
func TestOperationsPinnedWindowDoesNotRefresh(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?from=2026-11-18+08%3A00&to=2026-11-18+08%3A30", now)

	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("pinned window should not auto-refresh")
	}
	if !strings.Contains(body, `frozen`) || !strings.Contains(body, `back to live`) {
		t.Errorf("pinned footer should say it is frozen: %s", footerLine(body))
	}
}

// The freeze link must carry the filters along, or freezing would silently drop
// the thing being looked at.
func TestFreezeLinkKeepsFilters(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?route=%2Fv1%2Fpayments&outcome=error", now)

	if !strings.Contains(body, "freeze this window") {
		t.Fatal("no freeze link on a live page")
	}
	href := linkAfter(body, "freeze this window")
	for _, want := range []string{"route=%2Fv1%2Fpayments", "outcome=error", "from=", "to="} {
		if !strings.Contains(href, want) {
			t.Errorf("freeze link %q is missing %q", href, want)
		}
	}
}

// linkAfter returns the href of the anchor whose text contains label.
func linkAfter(body, label string) string {
	i := strings.Index(body, label)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], `href="`)
	if start < 0 {
		return ""
	}
	start += len(`href="`)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func TestWindowURL(t *testing.T) {
	in := map[string][]string{"route": {"/v1/x"}, "from": {"old"}, "to": {"old"}}
	got := windowURL("/operations", in, "2026-11-18 08:00", "")
	if strings.Contains(got, "old") {
		t.Errorf("previous from/to survived: %q", got)
	}
	if want := "/operations?from=2026-11-18+08%3A00&route=%2Fv1%2Fx"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := windowURL("/operations", map[string][]string{"route": {"/v1/x"}}, "", ""); got != "/operations?route=%2Fv1%2Fx" {
		t.Errorf("clear-only window = %q", got)
	}
}
