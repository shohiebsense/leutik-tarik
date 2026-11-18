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

	// No-JS browsers still get a document-level refresh, but only inside
	// <noscript>: everyone else updates in place.
	if !strings.Contains(body, `<noscript><meta http-equiv="refresh" content="60"></noscript>`) {
		t.Error("live page carries no noscript refresh fallback")
	}
	if !strings.Contains(body, `data-live-url=`) || !strings.Contains(body, `data-live-interval="15"`) {
		t.Error("live page does not expose a poll target")
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

	if strings.Contains(body, `http-equiv="refresh"`) || strings.Contains(body, `data-live-url=`) {
		t.Error("pinned window should not update itself")
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

// The filter form used to post its own preview of from/to, which froze the page
// on every search. Empty while live is what keeps it rolling.
func TestLiveFormDoesNotPinTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?service=&route=&method=&outcome=", now)

	if strings.Contains(body, `placeholder="YYYY-MM-DD HH:MM" value="20`) {
		t.Error("live form prefilled from/to, which would pin the window on submit")
	}
	if !strings.Contains(body, `name="last" value="15m"`) {
		t.Error("form does not carry the rolling window length")
	}
}

// A pinned window still prefills, so the reader can see what they pinned.
func TestPinnedFormPrefills(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?from=2026-11-18+08%3A00&to=2026-11-18+08%3A30", now)
	if !strings.Contains(body, `value="2026-11-18 08:00"`) {
		t.Error("pinned form should show the pinned range")
	}
}

func TestLastWindow(t *testing.T) {
	for _, bad := range []string{"", "0", "-5m", "15", "30d", "1w"} {
		if _, ok := lastWindow(bad); ok {
			t.Errorf("lastWindow(%q) accepted", bad)
		}
	}
	for _, good := range []string{"1m", "15m", "90s", "1h", "24h"} {
		if _, ok := lastWindow(good); !ok {
			t.Errorf("lastWindow(%q) rejected", good)
		}
	}
}

// The point of the presets: ask for an hour without typing a date.
func TestPresetsAreClickableAndMarked(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?last=1h&route=%2Fabt%2Fscans", now)

	for _, want := range []string{
		`>15m</a>`, `>1h</a>`, `>6h</a>`, `>24h</a>`, `>7d</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing preset %s", want)
		}
	}
	// The active preset carries route=/abt/scans, so switching period keeps the
	// thing being looked at.
	href := linkAfter(body, ">1h</a>")
	if !strings.Contains(href, "route=%2Fabt%2Fscans") || !strings.Contains(href, "last=1h") {
		t.Errorf("preset link lost its filters: %q", href)
	}
}

// One search box, and it is the first field: pasting a token must not need a
// decision about which field it belongs in.
func TestFreeTextSearchBox(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	body := livePage(t, "/operations?q=21616513243938675087", now)

	if !strings.Contains(body, `name="q" value="21616513243938675087"`) {
		t.Error("search box does not carry the pasted term")
	}
	if !strings.Contains(body, "paste a media token") {
		t.Error("search box has no hint about what to paste")
	}
}
