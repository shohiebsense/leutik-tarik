// Package ui renders server-side HTML views. Zero browser JavaScript.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/auth"
	"github.com/shohiebsense/leutik-tarik/internal/ingest"
	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// revealUsers is the allowlist of Leutik-Tarik VIEWERS who see raw values.
//
// The gate is the ingress basic-auth user, forwarded as X-Auth-User by Caddy. It
// is deliberately keyed on the viewer, not on the account a trace belongs to: an
// operator wants their own view unmasked regardless of which customer the request
// was about. Empty allowlist = everyone sees redacted values.
var revealUsers = func() map[string]bool {
	if v := os.Getenv("LEUTIK_REVEAL_USERS"); v != "" {
		return splitNameList(v)
	}
	return map[string]bool{"shohieb": true} // the operator account, by default
}()

func splitNameList(v string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

// viewerSeesRaw reports whether the signed-in caller is allowlisted to read raw
// values. Identity comes from the session (set by the auth middleware), not
// from a proxy header: the platform authenticates its own viewers now.
func viewerSeesRaw(r *http.Request) bool {
	if len(revealUsers) == 0 {
		return false
	}
	user, ok := auth.User(r.Context())
	if !ok {
		return false
	}
	return revealUsers[strings.ToLower(user)]
}

// UI serves the operations list and trace detail pages.
type UI struct {
	Store store.Store

	// Policies drives the effective-config view and the retention preview.
	Policies *policy.Engine
	// VerdictCfg is the "is this worth checking" rule. Nil reads the
	// environment, so the platform works with no configuration.
	VerdictCfg *VerdictConfig

	// now is injectable so time ranges are testable.
	now func() time.Time
}

var outcomes = []string{
	model.OutcomeSuccess,
	model.OutcomeClientError,
	model.OutcomeServerError,
	model.OutcomeError,
	model.OutcomeUnknown,
}

func (u UI) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// refreshSeconds is how often a browser without JavaScript re-asks for a live
// window. Kept longer than livePollSeconds because that path is a full document
// reload, and reloading every 15s would make the page unreadable.
const refreshSeconds = 60

// livePollSeconds is how often the shortest live window updates in place when
// JavaScript is available. Deliberately a poll rather than a stream: a
// long-lived connection would make the UI stateful, and this platform scales
// by adding stateless replicas. Longer windows poll far less often — see
// pollIntervalFor — because their query cost grows with the window.
const livePollSeconds = 5

// pollIntervalFor is how often a live window refreshes in place, in seconds.
// The cadence scales with the window because the query cost does: a 15m scan
// answers in tens of milliseconds, but a 7d scan re-reads and re-sorts the
// whole span table — seconds. Re-asking that every 5s per open tab kept
// ClickHouse permanently busy, and every page load queued behind the backlog.
// Even a 7d window stays live, at a 0.5% duty cycle.
func pollIntervalFor(window time.Duration) int {
	switch {
	case window <= 15*time.Minute:
		return livePollSeconds
	case window <= time.Hour:
		return 15
	case window <= 6*time.Hour:
		return 60
	case window <= 24*time.Hour:
		return 300
	default:
		return 900
	}
}

// defaultWindow is the rolling window a bare /operations asks for.
const defaultWindow = 15 * time.Minute

// durationParam is the shortest form of d that time.ParseDuration accepts:
// "15m", "24h" - never "1d", which Go cannot parse and which used to collapse a
// 24h window back to 15m the moment the form was submitted.
func durationParam(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return itoa(int(d/time.Minute)) + "m"
	default:
		return d.String()
	}
}

// lastWindow parses a rolling window length ("15m", "1h"). It refuses anything
// zero, negative or longer than a week: a guessing typo should fall back to the
// default window, not scan the whole store.
func lastWindow(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > 7*24*time.Hour {
		return 0, false
	}
	return d, true
}

// windowURL is the same page with the filters kept and from/to replaced (both
// empty removes them, which is what turns a frozen window back into a live
// one). Values are omitted when unchanged so a link stays short.
func windowURL(basePath string, in url.Values, from, to string) string {
	out := url.Values{}
	for k, v := range in {
		if k == "from" || k == "to" {
			continue
		}
		out[k] = v
	}
	if from != "" {
		out.Set("from", from)
	}
	if to != "" {
		out.Set("to", to)
	}
	if len(out) == 0 {
		return basePath
	}
	return basePath + "?" + out.Encode()
}

// liveURL is the URL the page polls to refresh itself: same filters, rolling
// window, never absolute times (those would freeze it on first poll) and never
// a cursor (that would pin the poll to one page of history).
func liveURL(basePath string, in url.Values, last string) string {
	out := url.Values{}
	for k, v := range in {
		if k == "from" || k == "to" || k == "cursor" {
			continue
		}
		out[k] = v
	}
	if last != "" {
		out.Set("last", last)
	}
	if len(out) == 0 {
		return basePath
	}
	return basePath + "?" + out.Encode()
}

// presetSpan is one clickable time range. Nobody should have to type a date to
// ask "what happened in the last hour".
type presetSpan struct {
	Label string
	Last  string
	URL   string
	On    bool
}

// Label is what a person reads; Last is what time.ParseDuration accepts. "7d"
// would not parse, so it posts 168h - the two used to disagree and silently fell
// back to the default window.
var presetSpans = []struct{ Label, Last string }{
	{"15m", "15m"}, {"1h", "1h"}, {"6h", "6h"}, {"24h", "24h"}, {"7d", "168h"},
}

// windowPresets builds the click targets, marking the one currently in use.
func windowPresets(basePath string, in url.Values) []presetSpan {
	current := in.Get("last")
	if current == "" && in.Get("from") == "" && in.Get("to") == "" {
		current = "15m"
	}
	out := make([]presetSpan, 0, len(presetSpans))
	for _, p := range presetSpans {
		out = append(out, presetSpan{
			Label: p.Label,
			Last:  p.Last,
			URL:   liveURL(basePath, in, p.Last),
			On:    current == p.Last || current == p.Label,
		})
	}
	return out
}

// activeFilter is one filter the reader can see and remove. A filter they cannot
// see is a filter that lies: "route=abt" returning nothing looked like a missing
// endpoint until the hidden "method=GET" was found.
type activeFilter struct {
	Label     string
	RemoveURL string
}

// filterChips lists the filters in force, each with the URL that drops it.
func filterChips(basePath string, in url.Values, last string) []activeFilter {
	// An empty value means "no filter"; the form posts them all so removing one
	// has to be a URL, not an empty field.
	labels := []struct{ key, prefix string }{
		{"service", "service "},
		{"route", "route contains "},
		{"method", "method "},
		{"outcome", "outcome "},
		{"q", "matches "},
	}
	var out []activeFilter
	for _, l := range labels {
		v := strings.TrimSpace(in.Get(l.key))
		if v == "" {
			continue
		}
		out = append(out, activeFilter{
			Label:     l.prefix + v,
			RemoveURL: withoutParam(basePath, in, l.key),
		})
	}
	if in.Get("all") == "1" {
		out = append(out, activeFilter{
			Label:     "background jobs included",
			RemoveURL: withoutParam(basePath, in, "all"),
		})
	}
	return out
}

// withoutParam returns the same page with one query parameter removed.
func withoutParam(basePath string, in url.Values, key string) string {
	out := url.Values{}
	for k, v := range in {
		if k == key || (key == "all" && k == "all") {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return basePath
	}
	return basePath + "?" + out.Encode()
}

// pageURL returns the same page with a cursor set (any previous cursor
// replaced), keeping every filter. Keyset, not offset: new arrivals shift an
// offset while a start_time boundary stays put, so a reader paging back never
// sees a row twice or misses one.
func pageURL(basePath string, in url.Values, cursor time.Time) string {
	out := url.Values{}
	for k, v := range in {
		if k == "cursor" {
			continue
		}
		out[k] = v
	}
	out.Set("cursor", cursor.Format(time.RFC3339Nano))
	return basePath + "?" + out.Encode()
}

// describeFilters is the sentence shown when a search matches nothing: it names
// what was asked for, so "not found" cannot be confused with "not recorded".
func describeFilters(in url.Values, last string) string {
	var parts []string
	for _, k := range []string{"q", "service", "route", "method", "outcome"} {
		if v := strings.TrimSpace(in.Get(k)); v != "" {
			name := k
			if k == "q" {
				name = "matches"
			} else if k == "route" {
				name = "route contains"
			}
			parts = append(parts, name+" \u201c"+v+"\u201d")
		}
	}
	if in.Get("all") == "1" {
		parts = append(parts, "background jobs included")
	}
	if len(parts) == 0 {
		return "No operations match."
	}
	return "No operations match " + strings.Join(parts, ", ") + "."
}

// Operations renders the searchable operations list.
func (u UI) Operations(w http.ResponseWriter, r *http.Request) {
	// Service scope comes from the path (/operations/{service}) so the URL reads
	// like the service page, with ?service= kept for older links and for the
	// form, which submits flat query parameters.
	service := r.PathValue("service")
	if service == "" {
		service = r.URL.Query().Get("service")
	}
	basePath := "/operations"
	title := "Operations"
	if service != "" {
		basePath = "/operations/" + service
		title = "Operations · " + service
	}

	text := strings.TrimSpace(r.URL.Query().Get("q"))
	q := store.Query{
		Service: service,
		Route:   r.URL.Query().Get("route"),
		Method:  r.URL.Query().Get("method"),
		Outcome: r.URL.Query().Get("outcome"),
		Text:    text,
		// Background jobs are opt-in: they are useful, but they are not requests.
		IncludeBackground: r.URL.Query().Get("all") == "1",
		Limit:             store.MaxLimit,
	}
	// Time range. Two ways to say it, and the difference is deliberate:
	//
	//	last=15m   a rolling window: it follows the clock, and the page updates
	//           itself in place (see livePollSeconds).
	//	from=..&to=..  a frozen window: an investigation, pinned to exact times.
	//
	// The default is rolling, because "what is happening now" is the question
	// this list exists to answer. Absolute times win when both are present, so a
	// link to an incident never drifts under the reader.
	to := u.clock()
	span, ok := lastWindow(r.URL.Query().Get("last"))
	if !ok {
		span = defaultWindow
	}
	from := to.Add(-span)
	live := true
	if t, ok := parseTime(r.URL.Query().Get("from")); ok {
		from, live = t, false
	}
	if t, ok := parseTime(r.URL.Query().Get("to")); ok {
		to, live = t, false
	}
	// A cursor pins the view to "older than this instant": paging, not a live
	// window. Frozen pages never auto-refresh, so the reader keeps their place
	// and the newest-first view is one click away.
	if t, ok := parseTime(r.URL.Query().Get("cursor")); ok {
		q.Cursor, live = t, false
	}
	last := durationParam(span)
	if !live {
		// A frozen window still needs a length for its labels.
		last = ""
	}
	q.From, q.To = from, to
	// A live view may be answered from the cache within its poll cadence; a
	// frozen one is evidence and always reads through.
	if live {
		q.CacheTTL = store.CacheTTLFor(span)
	}
	fromStr, toStr := inDisplay(from).Format(inputTimeLayout), inDisplay(to).Format(inputTimeLayout)

	// A live window also has to keep moving while it is open. Browsers without
	// JavaScript get a document-level refresh inside <noscript>; everyone else
	// gets an in-place update that swaps only the results, so the page does not
	// reload, the scroll position survives and nothing flashes. The cadence is
	// the window's own (pollIntervalFor): a 7d scan must not be re-asked every
	// 5s, and a pinned page does not advance at all.
	refresh, poll := 0, 0
	if live {
		refresh, poll = refreshSeconds, pollIntervalFor(span)
		if poll > refresh {
			refresh = poll
		}
	}

	// Every service in the window: once more than one service reports, a flat
	// list is a directory with no index.
	var strip []store.ServiceStat
	if stats, ok := u.Store.(store.Stats); ok {
		if all, serr := stats.ServiceStats(r.Context(), from, to); serr == nil {
			strip = all
		}
	}

	spans, err := u.Store.ListOperations(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The footer states the totals for the window it just showed, so the live
	// line and the table can never disagree.
	var totalErrors int
	for _, s := range spans {
		if s.Outcome == model.OutcomeError || s.Outcome == model.OutcomeServerError {
			totalErrors++
		}
	}

	// Paging is offered only when the page came back full: a short page is the
	// end of the list, and a link to an empty page reads as a bug.
	var olderURL string
	if len(spans) == q.Limit {
		olderURL = pageURL(basePath, r.URL.Query(), spans[len(spans)-1].StartTime)
	}

	render(w, r, operationsView, map[string]any{
		"Strip":    strip,
		"Title":    title,
		"Service":  service,
		"BasePath": basePath,
		"Spans":    spans,
		"Query":    q,
		"Outcomes": outcomes,
		"From":     fromStr,
		"To":       toStr,
		"Window":   to.Sub(from),
		// HumanDuration has a minutes rung ("15m"); the template's human helper
		// stops at hours, so a 15-minute window would read "15m0s".
		"WindowLabel": policy.HumanDuration(to.Sub(from)),

		"Text":           text,
		"Filters":        filterChips(basePath, r.URL.Query(), last),
		"EmptyReason":    describeFilters(r.URL.Query(), last),
		"ClearURL":       basePath + "?last=" + last,
		"Presets":        windowPresets(basePath, r.URL.Query()),
		"Live":           live,
		"Last":           last,
		"RefreshSeconds": refresh,
		"PollSeconds":    poll,
		"LiveURL":        liveURL(basePath, r.URL.Query(), last),
		"TotalSpans":     len(spans),
		"TotalErrors":    totalErrors,
		"PinnedURL":      windowURL(basePath, r.URL.Query(), fromStr, toStr),
		// NewestURL exists only on a cursor-pinned page: it is the same filters
		// back at the top of the list.
		"OlderURL":  olderURL,
		"NewestURL": withoutParam(basePath, r.URL.Query(), "cursor"),
		"HasCursor": !q.Cursor.IsZero(),
	})
}

// LoginPage renders the sign-in form. It is the one page outside the auth
// guard, so it has its own shell (no nav, no data).
func (u UI) LoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.User(r.Context()); ok {
		http.Redirect(w, r, "/operations", http.StatusFound)
		return
	}
	render(w, r, loginView, map[string]any{
		"Title": "Sign in",
		"Next":  r.URL.Query().Get("next"),
		"Error": r.URL.Query().Get("error") != "",
	})
}

// PoliciesPage shows the effective configuration after defaults, plus a preview
// of what each rule would match in recent traffic (build prompt §10.2: preview
// before activation, and show the effective config, not the file text).
func (u UI) PoliciesPage(w http.ResponseWriter, r *http.Request) {
	p := u.Policies.Policy()

	// Preview against the same window the operations page uses.
	to := u.clock()
	from := to.Add(-24 * time.Hour)
	spans, err := u.Store.ListOperations(r.Context(), store.Query{From: from, To: to, Limit: store.MaxLimit})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// measuredBytesPerSpan: 20.82 MiB for 2M spans, measured on ClickHouse
	// 26.10 (docs/review-2026-11-17.md). An estimate, labelled as one.
	const measuredBytesPerSpan = 11
	preview := u.Policies.Preview(spans, to.Sub(from), measuredBytesPerSpan)

	// One human-readable line per rule so the table says what it selects.
	matches := make(map[string]string, len(p.Rules))
	for _, r := range p.Rules {
		matches[r.Name] = describeMatch(r.Match)
	}

	render(w, r, policiesView, map[string]any{
		"Title":      "Policies",
		"Policy":     p,
		"Preview":    preview,
		"RuleMatch":  matches,
		"Sampled":    len(spans),
		"Defaults":   p.Defaults.Trace.Duration(),
		"Window":     to.Sub(from),
		"BytesPerDa": measuredBytesPerSpan,
		"Matches":    matches,
	})
}

// describeMatch renders a rule's selectors in one line.
func describeMatch(m policy.Match) string { return policy.DescribeMatch(m) }

// inputTimeLayout is what the time inputs show: compact and readable, not
// RFC3339, which is neither and overflows a small field.
const inputTimeLayout = "2006-01-02 15:04"

// parseTime accepts the display layout, RFC3339, and a bare date.
func parseTime(v string) (time.Time, bool) { return parseTimeInDisplay(v) }

type traceRow struct {
	Span   model.Span
	Offset time.Duration
	// Depth is 0 for the root; children are indented one level.
	Depth int
	// Prefix is the outline glyph string used inside the SVG waterfall, where each
	// label is its own text run.
	Prefix string
	// TreeHTML is the span name wrapped in one nested block per depth level.
	//
	// Glyph-based trees (├ │ └) were tried first and cannot work here: a
	// box-drawing character does not fill the line box, so consecutive rows show
	// a gap and the vertical lines read as disconnected marks. Nested blocks give
	// a continuous guide whose height is the row's own content.
	TreeHTML template.HTML
}

// traceHeadline describes the operation in the terms a person asked the
// question in: the HTTP request, not a 32-character trace id. The id stays, as a
// subtitle instead of the title.
func traceHeadline(spans []model.Span, traceID string, showRaw bool) (title, subtitle string, req *traceRequest) {
	var root model.Span
	found := false
	for _, s := range spans {
		if s.ParentSpanID == "" {
			root, found = s, true
			break
		}
	}
	if !found {
		return "Trace", traceID, nil
	}

	name := root.Name
	if root.HTTPRoute != "" {
		name = strings.TrimSpace(root.HTTPMethod + " " + root.HTTPRoute)
	}
	title = name
	subtitle = "trace " + traceID + " · " + root.ServiceName

	req = &traceRequest{
		Method:    root.HTTPMethod,
		Route:     root.HTTPRoute,
		Target:    root.Attributes["http.target"],
		Status:    root.StatusCode,
		ClientIP:  root.Attributes["http.client_ip"],
		Identity:  firstNonEmpty(root.Attributes["enduser.id"], root.Attributes["enduser.pseudo.id"]),
		Email:     root.Attributes["enduser.email"],
		Phone:     root.Attributes["enduser.phone_number"],
		Payload:   root.Attributes["http.request.body"],
		HasBody:   root.Attributes["http.request.body"] != "",
		Headers:   requestHeaders(root.Attributes, showRaw),
		UserAgent: root.Attributes["user_agent.original"],
		Service:   root.ServiceName,
	}
	if i := strings.IndexByte(req.Target, '?'); i >= 0 && i+1 < len(req.Target) {
		req.Query = req.Target[i+1:]
	}
	if req.Method == "" && req.Route == "" && req.Target == "" {
		req = nil
	}
	return title, subtitle, req
}

// traceRequest is the request metadata shown above the waterfall.
type traceRequest struct {
	// Identity is the account this request was about (enduser.id).
	Identity string
	Email    string
	Phone    string
	// Payload is the captured request body, already scrubbed at ingest.
	Payload string
	// HasBody distinguishes "no body was sent" from "the body was not captured",
	// so the page can say which it is instead of rendering nothing.
	HasBody bool
	// Query is the raw query string from http.target, for requests that carry
	// their data there instead of in a body.
	Query string
	// Headers are the incoming HTTP headers, minus the ones already shown.
	Headers   []attrRow
	Method    string
	Route     string
	Target    string
	Status    int
	ClientIP  string
	UserAgent string
	Service   string
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// requestHeaders pulls the captured headers out of the span attributes, sorted,
// and skips the ones already shown in the strip.
func requestHeaders(raw map[string]string, showRaw bool) []attrRow {
	attrs := raw
	if !showRaw {
		attrs = ingest.RedactCopy(raw)
	}
	skip := map[string]bool{"host": true}
	var out []attrRow
	for k, v := range attrs {
		const p = "http.request.header."
		if len(k) <= len(p) || k[:len(p)] != p {
			continue
		}
		name := k[len(p):]
		if skip[name] {
			continue
		}
		out = append(out, attrRow{Key: name, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// prettyPayload indents JSON so a payload is readable instead of one long line.
func prettyPayload(s string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(s, "…[truncated]"))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(trimmed), "", "  "); err != nil {
		return s
	}
	out := buf.String()
	if strings.HasSuffix(s, "…[truncated]") {
		out += "…[truncated]"
	}
	return out
}

// payloadFor returns the pretty-printed payload, or "" when there is none.
func payloadFor(req *traceRequest, showRaw bool) string {
	if req == nil || req.Payload == "" {
		return ""
	}
	body := req.Payload
	if !showRaw {
		body = ingest.RedactCopy(map[string]string{"http.request.body": body})["http.request.body"]
	}
	return prettyPayload(body)
}

// traceStamp is the top-right block: when it started, in the display zone.
func traceStamp(summary model.Summary, root model.Span) template.HTML {
	start := root.StartTime
	if start.IsZero() {
		return ""
	}
	stamp := inDisplay(start)
	_, offset := stamp.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	tz := fmt.Sprintf("%s UTC%s%02d:%02d", displayLocation.String(), sign, offset/3600, (offset%3600)/60)
	return template.HTML(fmt.Sprintf(
		`<span class="big">%s</span><span style="display:block;font-size:.85rem;color:#333">%s · %s</span><span class="tz">%s</span>`,
		template.HTMLEscapeString(stamp.Format("Mon 02 Jan 2006")),
		template.HTMLEscapeString(stamp.Format("15:04:05.000")),
		template.HTMLEscapeString(shortDur(summary.Duration)),
		template.HTMLEscapeString(tz)))
}

// treeHTML nests one block per depth level, so the guide line is drawn by CSS on
// each level and cannot leave the gaps a glyph-based tree produces.
func treeHTML(depth int, name string) template.HTML {
	var b strings.Builder
	levels := depth
	if levels < 0 {
		levels = 0
	}
	// Every level gets the same class: an earlier version marked the outermost as
	// "top" and zeroed its padding, which cancelled the indentation for every row.
	for i := 0; i < levels; i++ {
		b.WriteString(`<span class="lvl">`)
	}
	b.WriteString(template.HTMLEscapeString(name))
	for i := 0; i < levels; i++ {
		b.WriteString("</span>")
	}
	return template.HTML(b.String())
}

// displayName is the label used in the span list: the route for a request, the
// span name otherwise.
func displayName(s model.Span) string {
	if s.HTTPRoute != "" {
		return s.HTTPRoute
	}
	return s.Name
}

// spanDetail is one span plus its attributes, rendered as a block in call order.
type spanDetail struct {
	Name     string
	Depth    int
	Kind     string
	Outcome  string
	Duration string
	Rows     []attrRow
}

// attrRow is one span attribute, flattened so the template can render a table
// without iterating a map inside a range (which loses the span identity).
type attrRow struct {
	Span      string
	SpanShort string
	Key       string
	Value     string
}

// Trace renders one trace as a summary plus a waterfall table.
// errorCause is one failing span and everything the trace actually says about
// why. The point is to separate what the service reported from what we inferred,
// because a page that shows a 401 with no explanation reads as "no difference
// between an error and a success".
type errorCause struct {
	Span    string
	Outcome string
	Status  int
	// Reported is the service's own words: status message, error type or an
	// exception attribute. Empty when the service sent only a status code.
	Reported string
	// Inferred is derived from the status code, and labelled as such in the page.
	Inferred string
}

// traceErrors collects the failing spans, worst first, with what we can honestly
// say about each.
func traceErrors(spans []model.Span) []errorCause {
	var out []errorCause
	for _, sp := range spans {
		if !isFailure(sp) {
			continue
		}
		out = append(out, errorCause{
			Span:     displayName(sp),
			Outcome:  sp.Outcome,
			Status:   sp.StatusCode,
			Reported: reportedReason(sp),
			Inferred: inferCause(sp.StatusCode, sp.Outcome),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Status >= out[j].Status })
	return out
}

// isFailure includes client errors: a rejected login is the thing a person opens
// a trace to explain, and it was rendered in the same grey as a success.
func isFailure(s model.Span) bool {
	switch s.Outcome {
	case model.OutcomeError, model.OutcomeServerError, model.OutcomeClientError:
		return true
	}
	return s.StatusCode >= 400
}

// reportedReason is what the service itself said, in its own words. Nothing here
// is invented: an empty result means the span carried no reason at all.
func reportedReason(s model.Span) string {
	if msg := strings.TrimSpace(s.StatusMessage); msg != "" {
		return msg
	}
	if et := strings.TrimSpace(s.ErrorType); et != "" {
		return et
	}
	for _, k := range []string{"error.message", "exception.message", "otel.status_description"} {
		if v := strings.TrimSpace(s.Attributes[k]); v != "" {
			return v
		}
	}
	return ""
}

// inferCause reads the status code the way a person would. It is separate from
// reportedReason so the page can say which is which: an inference is a hint, not
// evidence, and the fix is for the service to send the real message.
func inferCause(status int, outcome string) string {
	switch {
	case status == 400:
		return "the service rejected the request as malformed"
	case status == 401:
		return "authentication failed — the credentials were rejected"
	case status == 403:
		return "the caller is not allowed to perform this"
	case status == 404:
		return "no such route or record"
	case status == 409:
		return "the request conflicts with the current state"
	case status == 422:
		return "the request body failed validation"
	case status == 429:
		return "the caller is being rate limited"
	case status >= 500:
		return "the service failed while handling the request"
	case outcome == model.OutcomeError:
		return "the operation failed"
	}
	return ""
}

// Trace renders one request's full story.
func (u UI) Trace(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceID")
	spans, err := u.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(spans) == 0 {
		http.NotFound(w, r)
		return
	}

	logs, err := u.Store.GetLogs(r.Context(), traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	summary := model.Summarize(spans)
	start := spans[0].StartTime
	for _, s := range spans {
		if s.StartTime.Before(start) {
			start = s.StartTime
		}
	}
	// Order by call hierarchy (depth-first from the roots), not by start time:
	// sorting by clock interleaves sibling calls, so a query appears before the
	// function that issued it and the stack has to be reconstructed by eye.
	rows := orderedRows(spans, start)
	var exact, correlated []model.LogEvent
	for _, l := range logs {
		if l.Exact {
			exact = append(exact, l)
		} else {
			correlated = append(correlated, l)
		}
	}
	for i := range rows {
		rows[i].TreeHTML = treeHTML(rows[i].Depth, displayName(rows[i].Span))
	}

	showRaw := viewerSeesRaw(r)
	var details []spanDetail
	for _, row := range rows {
		keys := make([]string, 0, len(row.Span.Attributes))
		for k := range row.Span.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := spanDetail{
			Name:     row.Span.Name,
			Depth:    row.Depth,
			Kind:     row.Span.Kind,
			Outcome:  row.Span.Outcome,
			Duration: policy.HumanDuration(row.Span.Duration),
		}
		attrs := row.Span.Attributes
		if !showRaw {
			attrs = ingest.RedactCopy(attrs)
		}
		for _, k := range keys {
			d.Rows = append(d.Rows, attrRow{Key: k, Value: attrs[k]})
		}
		details = append(details, d)
	}

	headline, subtitle, req := traceHeadline(spans, traceID, showRaw)
	render(w, r, traceView, map[string]any{
		"Details":     details,
		"Title":       headline,
		"Subtitle":    subtitle,
		"HeaderRight": traceStamp(summary, spans[0]),
		"Request":     req,
		"Payload":     payloadFor(req, showRaw),
		"ShowRaw":     showRaw,
		"TraceID":     traceID,
		"Rows":        rows,
		"Summary":     summary,
		"ExactLogs":   exact,
		"Correlated":  correlated,
		"Waterfall":   buildWaterfall(spans, 980),
		"Errors":      traceErrors(spans),
	})
}

// depthOf walks parent links to indent children. Unknown parents stay at depth
// 1 rather than being hidden: an orphan span is evidence, not noise.
func depthOf(spans []model.Span, s model.Span) int {
	if s.ParentSpanID == "" {
		return 0
	}
	byID := make(map[string]model.Span, len(spans))
	for _, sp := range spans {
		byID[sp.SpanID] = sp
	}
	depth := 1
	cur := s
	for i := 0; i < 20; i++ { // bound the walk; cycles must not hang the page
		parent, ok := byID[cur.ParentSpanID]
		if !ok {
			return depth
		}
		depth++
		cur = parent
		if cur.ParentSpanID == "" {
			return depth
		}
	}
	return depth
}

var funcs = template.FuncMap{
	"dur": func(d time.Duration) string {
		switch {
		case d < time.Microsecond:
			return d.String()
		case d < time.Millisecond:
			return d.Round(time.Microsecond).String()
		default:
			return d.Round(time.Millisecond).String()
		}
	},
	"ts": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return inDisplay(t).Format("2006-01-02 15:04:05.000")
	},
	// human renders a retention as a person would say it: "14d", not "336h0m0s".
	"human": func(d time.Duration) string {
		switch {
		case d <= 0:
			return "—"
		case d%(24*time.Hour) == 0:
			return itoa(int(d/(24*time.Hour))) + "d"
		case d%time.Hour == 0:
			return itoa(int(d/time.Hour)) + "h"
		default:
			return d.String()
		}
	},
	"pct":    func(f float64) template.CSS { return pctWidth(f) },
	"pctnum": func(f float64) string { return fmt.Sprintf("%.1f%%", f*100) },
	"bar":    func(n, max int64) template.CSS { return barWidth(n, max) },
	"class":  func(outcome string) string { return severityClass(outcome) },
	"share":  func(e store.EndpointStat, grand time.Duration) float64 { return shareOf(e, grand) },
	"inc":    func(i int) int { return i + 1 },
	"short":  shortDur,
	"verdictState": func(e store.EndpointStat, idx map[string]Verdict) string {
		return idx[e.Service+"|"+e.Method+"|"+e.Route].State
	},
	"verdictLabel": func(e store.EndpointStat, idx map[string]Verdict) string {
		return idx[e.Service+"|"+e.Method+"|"+e.Route].Label
	},
	"div": func(a, b float64) float64 {
		if b == 0 {
			return 0
		}
		return a / b
	},
	"pctFrac": func(f float64) template.CSS { return pctWidth(f) },
	"ns":      func(v int64) string { return nsDuration(v) },
	"indent": func(n int) template.CSS {
		// CSS width, not nested spans: keeps the table a table.
		px := n * 16
		return template.CSS("padding-left:" + itoa(px) + "px")
	},
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// templates are parsed once at package init: a broken template must fail at
// startup, not render half a page with a 200 (review P4).
var (
	operationsView    = template.Must(template.Must(pageBase.Clone()).Parse(operationsTemplate))
	servicesView      = template.Must(template.Must(pageBase.Clone()).Parse(servicesTemplate))
	incidentsView     = template.Must(template.Must(pageBase.Clone()).Parse(incidentsTemplate))
	serviceDetailView = template.Must(template.Must(pageBase.Clone()).Parse(serviceDetailTemplate))
	profilesView      = template.Must(template.Must(pageBase.Clone()).Parse(profilesTemplate))
	profileDetailView = template.Must(template.Must(pageBase.Clone()).Parse(profileDetailTemplate))
	traceView         = template.Must(template.Must(pageBase.Clone()).Parse(traceTemplate))
	policiesView      = template.Must(template.Must(pageBase.Clone()).Parse(policiesTemplate))
	loginView         = template.Must(template.New("page").Parse(loginTemplate))
)

func render(w http.ResponseWriter, r *http.Request, t *template.Template, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Zone"]; !ok {
		data["Zone"] = displayZoneLabel
	}
	// The signed-in viewer's name, for the header. Absent when auth is off.
	if user, ok := auth.User(r.Context()); ok {
		data["User"] = user
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "page", data); err != nil {
		// The header is already sent; log-worthy, but never a silent 200 with a
		// truncated body.
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

var pageBase = template.Must(template.New("page").Funcs(funcs).Parse(pageShell))

const pageShell = `{{/* One period control for every page that has a window, so the click
targets are identical wherever the reader is. */}}
{{define "period"}}<p class="presets"><span class="k">period</span>
  {{range .Presets}}<a href="{{.URL}}" class="{{if .On}}on{{end}}">{{.Label}}</a>{{end}}
  {{if .Live}}<a class="clear" href="{{.PinnedURL}}">freeze</a>{{else}}<a class="clear" href="{{.LiveURL}}">back to live</a>{{end}}
</p>{{end}}
{{define "page"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — leutik-tarik</title>
{{/* A live window advances only by re-asking the server, and this UI runs no
     JavaScript (ADR 0002), so the refresh is the document-level one. */}}
{{if .RefreshSeconds}}<noscript><meta http-equiv="refresh" content="{{.RefreshSeconds}}"></noscript>{{end}}
<style>
html,body{margin:0;padding:0}
.brandbar{display:flex;align-items:center;gap:1.4rem;flex-wrap:wrap;border-bottom:1px solid #ececec;padding-bottom:.55rem;margin-bottom:.9rem}
.brand{display:inline-flex;align-items:center;gap:.45rem;text-decoration:none;color:#111}
.wordmark{font-weight:700;letter-spacing:-.01em;font-size:1.05rem}
.wordmark .dash{color:#b00020}
.brandbar nav a{margin-right:1.1rem;text-decoration:none;color:#06c}
.brandbar nav a:hover{text-decoration:underline}
.who{margin-left:auto;display:inline-flex;align-items:center;gap:.5rem;font-size:.8rem;color:#555}
.logoutform{display:inline;margin:0}
.logoutform button{background:none;border:0;color:#06c;cursor:pointer;font:inherit;padding:0;text-decoration:underline}
/* Whole-row links without JavaScript: the anchor stretches over its row. */
tr.rowlink{position:relative}
tr.rowlink:hover td{background:#f6f9fc}
tr.rowlink a.rowhit{position:absolute;inset:0;text-indent:-9999px;overflow:hidden}
tr.rowlink:focus-within td{background:#eaf2fb;outline:1px solid #9cc0e8}
.strip{display:flex;flex-wrap:wrap;gap:.4rem;margin:.1rem 0 .9rem}
.strip a{display:inline-flex;align-items:center;gap:.4rem;padding:.2rem .6rem;border:1px solid #e0e4e8;border-radius:12px;font-size:.8rem;text-decoration:none;color:#234}
.strip a.on{background:#111;color:#fff;border-color:#111}
.svc{display:inline-block;margin-left:.45rem;padding:.02rem .35rem;border-radius:3px;background:#f2f4f6;color:#5b6770;font-size:.72rem}
.strip a .n{color:#888}
.strip a.on .n{color:#cfcfcf}
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;color:#111;padding:1.25rem;box-sizing:border-box}
.wrap{max-width:1600px;margin:0 auto}
/* Anything wider than the viewport scrolls here instead of being clipped. */
.content{overflow-x:auto;-webkit-overflow-scrolling:touch;padding-bottom:.5rem}
.content table{min-width:680px}
.content svg{min-width:680px}
table{border-collapse:collapse;width:100%;font-size:.875rem}
th,td{border-bottom:1px solid #ddd;padding:.35rem .55rem;text-align:left;vertical-align:top}
th{background:#f5f5f5;font-weight:600}
a{color:#06c}
nav{margin-bottom:1rem}nav a{margin-right:1rem}
code{background:#f5f5f5;padding:.1rem .3rem}
.bad{color:#b00020}
.warn{color:#8a5b00}
.dim{color:#666}
.reqstrip{margin:.2rem 0 .6rem;min-width:0}
.reqstrip td{border:0;padding:.1rem 1.2rem .1rem 0;vertical-align:top}
.payload{margin:.4rem 0 .8rem}
.nobody{margin:0;padding:.5rem .7rem;background:#fbfbfb;border:1px dashed #d9dde1;border-radius:4px;color:#555;font-size:.85rem}
.nobody code{background:#f0f2f4;padding:.05rem .3rem}
.payload .k{font-size:.68rem;text-transform:uppercase;letter-spacing:.04em;color:#888;margin-bottom:.15rem}
.payload pre{
  margin:0;padding:.6rem .75rem;background:#f7f8f9;border:1px solid #e3e6e8;border-radius:4px;
  font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.8rem;line-height:1.45;
  overflow-x:auto;max-height:22rem;
}
.headers{margin:.2rem 0 .8rem;font-size:.85rem}
.headers summary{cursor:pointer;color:#06c}
.reqstrip .k{display:block;font-size:.68rem;text-transform:uppercase;letter-spacing:.04em;color:#888}
.reqstrip b{font-size:.9rem;font-weight:600;word-break:break-word}
.summary{display:flex;gap:1.5rem;flex-wrap:wrap;margin:.5rem 0 1rem}
.summary div{min-width:9rem}
.summary b{display:block;font-size:1.05rem;overflow-wrap:anywhere}
.summary div{min-width:0;max-width:100%}
.bar{display:inline-block;width:70px;height:8px;background:#eee;border-radius:4px;vertical-align:middle;overflow:hidden}
.bar .fill{display:block;height:8px;background:#1a7f37}
.bar .fill.bad-bg{background:#b00020}
.legend{font-size:.8rem;color:#666;margin:.3rem 0 .8rem}
.zone{font-size:.75rem;color:#777;margin:.1rem 0 .8rem}
.rawon{color:#8a5b00;font-weight:600}
.pagetop{display:flex;justify-content:space-between;align-items:flex-start;gap:1rem;flex-wrap:wrap}
.pagetop h1{margin:.2rem 0 .25rem}
.sub{margin:0;font-size:.85rem;color:#555}
.sub code{background:#f2f2f2;padding:.05rem .3rem;font-size:.8rem}
.topright{text-align:right;font-size:.85rem;color:#333;white-space:nowrap}
.topright .big{display:block;font-size:1rem;font-weight:600}
.topright .tz{display:block;font-size:.75rem;color:#777}
@media (max-width:700px){.topright{text-align:left}}
.find{display:flex;gap:.5rem;align-items:center;margin:.2rem 0 .5rem;flex-wrap:wrap}
.find input{flex:1 1 20rem;min-width:0;padding:.5rem .6rem;font-size:.95rem;border:1px solid #d0d5da;border-radius:5px;font-family:inherit}
.find input:focus{outline:2px solid #06c;border-color:#06c}
.find button{padding:.5rem 1rem;background:#111;color:#fff;border:0;border-radius:5px;font-size:.9rem;font-weight:600;cursor:pointer}
.find button:hover{background:#333}
.find .clear{font-size:.85rem;color:#666}
.errbox{border:1px solid #f0c7cd;background:#fdf6f7;border-left:4px solid #b00020;border-radius:5px;padding:.55rem .75rem;margin:.2rem 0 .8rem}
.errhead{margin:0 0 .25rem;display:flex;gap:.5rem;align-items:center;flex-wrap:wrap}
.errbadge{font-size:.72rem;font-weight:700;letter-spacing:.02em;text-transform:uppercase;padding:.1rem .45rem;border-radius:3px}
.errbadge.bad{background:#b00020;color:#fff}
.errbadge.warn{background:#8a5b00;color:#fff}
.errwhy{margin:0;font-size:.88rem;line-height:1.45}
.chips{display:flex;gap:.35rem;align-items:center;flex-wrap:wrap;font-size:.8rem;color:#666;margin:0 0 .5rem}
.chips .k{text-transform:uppercase;letter-spacing:.04em;font-size:.68rem;color:#888;margin-right:.2rem}
.chip{display:inline-flex;align-items:center;gap:.35rem;padding:.1rem .5rem;background:#eef3f8;border:1px solid #d6e2ee;border-radius:12px;color:#234}
.chip a{text-decoration:none;color:#567;font-weight:700}
.chip a:hover{color:#b00020}
.presets{display:flex;gap:.35rem;align-items:center;flex-wrap:wrap;font-size:.8rem;color:#666;margin:0 0 .6rem}
.presets .k{text-transform:uppercase;letter-spacing:.04em;font-size:.68rem;color:#888;margin-right:.2rem}
.presets a{padding:.15rem .55rem;border:1px solid #e0e4e8;border-radius:12px;text-decoration:none;color:#234}
.presets a:hover{border-color:#9cc0e8}
.presets a.on{background:#111;color:#fff;border-color:#111}
.presets .pintoggle{margin-left:.6rem;padding-left:.6rem;border-left:1px solid #e6e6e6}
.presets .pintoggle input{padding:.15rem .35rem;font-size:.78rem;border:1px solid #e0e4e8;border-radius:4px}
.presets .pintoggle label{color:#888}
.morefilters{margin:.1rem 0 .7rem;font-size:.85rem}
.morefilters summary{cursor:pointer;color:#06c}
.morefilters input,.morefilters select{padding:.3rem .45rem;border:1px solid #e0e4e8;border-radius:4px;font-size:.85rem;margin:.15rem .3rem .15rem 0}
/* The live region swaps in place, so the only motion is a brief fade: a jump
   cut would read as "did it change?", and a full reload would lose the scroll. */
#live{transition:opacity .14s ease}
#live.livefade{opacity:.45}
@media (prefers-reduced-motion:reduce){#live{transition:none}}
/* The tree glyphs must touch between rows: any vertical padding or leading above
   1 leaves a gap and the bars read as disconnected marks. */
table td.tree{padding-top:.15rem;padding-bottom:.15rem}
.tree .lvl{
  display:block;
  /* Visible at a glance: #dfe3e6 disappeared in a downscaled screenshot, which
     is how most people will look at this. */
  border-left:1px solid #b9c0c7;
  padding-left:.75em;
  margin-left:.2em;
}

.code{display:inline-block;padding:0 .35rem;border-radius:3px;background:#eef;color:#234;font-variant-numeric:tabular-nums}
.code.bad{background:#fdecef;color:#b00020}
.good{color:#1a7f37}
.code.warn{background:#fdf6e3;color:#8a5b00}
.spanblock{border-left:2px solid #e6e6e6;margin:.25rem 0 .25rem 0;padding:.15rem 0 .15rem .6rem}
.spanblock.root{border-left:2px solid #bbb}
.ind{display:inline-block}
table.attrs{min-width:0;width:auto;margin:.25rem 0 .4rem}
table.attrs td{padding:.15rem .5rem;border-bottom:1px dotted #eee;vertical-align:top}
table.attrs td.key{white-space:nowrap;color:#444}
table.attrs td.val{word-break:break-word;max-width:70ch}
@media (max-width:700px){
  body{padding:.75rem}
  nav{display:flex;flex-wrap:wrap;gap:.25rem .9rem}
  nav a{margin:0}
  h1{font-size:1.4rem;word-break:break-word}
  h2{font-size:1.15rem}
  .summary{flex-direction:column;gap:.6rem}
  .summary div{min-width:0;max-width:100%}
  .summary b{overflow-wrap:anywhere;word-break:break-word}
  table{font-size:.8rem}
  th,td{padding:.3rem .4rem}
}
</style>
</head>
<body>
<div class="wrap">
<header class="brandbar">
  <a class="brand" href="/operations" aria-label="leutik-tarik home">
    <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true">
      <rect x="1" y="9" width="3" height="4" rx="1" fill="#111"/>
      <rect x="6" y="5" width="3" height="12" rx="1" fill="#111"/>
      <rect x="11" y="2" width="3" height="18" rx="1" fill="#b00020"/>
      <rect x="16" y="7" width="3" height="8" rx="1" fill="#111"/>
    </svg>
    <span class="wordmark">leutik<span class="dash">-</span>tarik</span>
  </a>
  <nav>
    <a href="/operations">Operations</a>
    <a href="/services">Services</a>
    <a href="/incidents">Incidents</a>
    <a href="/profiles">Profiles</a>
    <a href="/policies">Policies</a>
  </nav>
  {{if .User}}<span class="who">{{.User}} · <form method="post" action="/logout" class="logoutform"><button type="submit">logout</button></form></span>{{end}}
</header>
<div class="pagetop">
  <div>
    <h1>{{.Title}}</h1>
    {{if .Subtitle}}<p class="sub">{{.Subtitle}}</p>{{end}}
  </div>
  {{if .HeaderRight}}<div class="topright">{{.HeaderRight}}</div>{{end}}
</div>
<p class="zone" id="zone">All times shown in {{.Zone}}{{if .Service}} · scoped to {{.Service}} — <a href="/operations">show all services</a>{{end}}{{if .ShowRaw}} · <span class="rawon">raw values visible — your account is allowlisted</span>{{end}}</p>
<div class="content">
{{template "content" .}}
</div>
</div>
<script>
/* In-place update of the live region. The page is still server-rendered: this
   fetches the same URL, takes the #live section out of the response and swaps
   only that, so scroll position, focus in the filter form and the rest of the
   document are untouched. Nothing polls while the tab is hidden. */
(function () {
  var el = document.getElementById('live');
  if (!el || !el.dataset.liveUrl || !window.fetch || !window.DOMParser) return;
  var ms = (parseInt(el.dataset.liveInterval, 10) || 15) * 1000;
  var busy = false, pending = false;

  function swap() {
    if (busy) { pending = true; return; }
    busy = true;
    fetch(el.dataset.liveUrl, { credentials: 'same-origin', headers: { 'X-Live': '1' } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
      .then(function (html) {
        var next = new DOMParser().parseFromString(html, 'text/html').getElementById('live');
        if (!next) return;
        el.classList.add('livefade');
        setTimeout(function () {
          el.innerHTML = next.innerHTML;
          if (next.dataset.liveInterval) el.dataset.liveInterval = next.dataset.liveInterval;
          el.classList.remove('livefade');
        }, 140);
      })
      .catch(function () { /* keep what is on screen; the next tick retries */ })
      .then(function () { busy = false; if (pending) { pending = false; swap(); } });
  }

  setInterval(function () { if (!document.hidden) swap(); }, ms);
  document.addEventListener('visibilitychange', function () { if (!document.hidden) swap(); });
})();
</script>
</body>
</html>{{end}}`

const loginTemplate = `{{define "page"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in — leutik-tarik</title>
<style>
html,body{margin:0;padding:0}
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;color:#111;background:#fafafa;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:1.25rem;box-sizing:border-box}
.card{width:100%;max-width:340px}
.brand{display:inline-flex;align-items:center;gap:.45rem;text-decoration:none;color:#111;margin-bottom:1.1rem}
.wordmark{font-weight:700;letter-spacing:-.01em;font-size:1.15rem}
.wordmark .dash{color:#b00020}
form{background:#fff;border:1px solid #e3e6e8;border-radius:8px;padding:1.25rem}
label{display:block;font-size:.8rem;color:#555;margin:.6rem 0 .25rem}
input{width:100%;box-sizing:border-box;padding:.5rem .6rem;border:1px solid #d0d5da;border-radius:5px;font-size:.95rem;font-family:inherit}
input:focus{outline:2px solid #06c;border-color:#06c}
button{margin-top:1rem;width:100%;padding:.55rem;background:#111;color:#fff;border:0;border-radius:5px;font-size:.95rem;font-weight:600;cursor:pointer}
button:hover{background:#333}
.error{background:#fdecef;color:#b00020;border:1px solid #f5c6cb;border-radius:5px;padding:.5rem .65rem;font-size:.85rem;margin-bottom:.75rem}
.hint{font-size:.78rem;color:#777;margin-top:.9rem;line-height:1.45}
</style>
</head>
<body>
<div class="card">
  <a class="brand" href="/operations" aria-label="leutik-tarik">
    <svg width="24" height="24" viewBox="0 0 22 22" aria-hidden="true">
      <rect x="1" y="9" width="3" height="4" rx="1" fill="#111"/>
      <rect x="6" y="5" width="3" height="12" rx="1" fill="#111"/>
      <rect x="11" y="2" width="3" height="18" rx="1" fill="#b00020"/>
      <rect x="16" y="7" width="3" height="8" rx="1" fill="#111"/>
    </svg>
    <span class="wordmark">leutik<span class="dash">-</span>tarik</span>
  </a>
  <form method="post" action="/login">
    {{if .Error}}<div class="error">Wrong username or password.</div>{{end}}
    {{if .Next}}<input type="hidden" name="next" value="{{.Next}}">{{end}}
    <label for="username">Username</label>
    <input id="username" name="username" type="text" autocomplete="username" autofocus required>
    <label for="password">Password</label>
    <input id="password" name="password" type="password" autocomplete="current-password" required>
    <button type="submit">Sign in</button>
  </form>
  <p class="hint">Credentials are in the operator credential file. Sessions last 30 days.</p>
</div>
</body>
</html>{{end}}`

const profilesTemplate = `{{define "content"}}
<p class="dim">Blobs in <code>{{.Dir}}</code>, metadata in ClickHouse. POST a pprof profile to <code>/v1/profiles?service=NAME&amp;type=cpu</code>.</p>

<table>
<thead><tr><th>Started</th><th>Service</th><th>Type</th><th>Size</th><th></th></tr></thead>
<tbody>
{{range .Profiles}}
<tr>
  <td>{{ts .StartedAt}}</td>
  <td>{{.Service}}</td>
  <td>{{.Type}}</td>
  <td>{{.Bytes}} B</td>
  <td><a href="/profiles/{{.ID}}">flamegraph</a></td>
</tr>
{{else}}
<tr><td colspan="5" class="dim">No profiles yet. A Go service can post its own with <code>curl --data-binary @cpu.pb.gz '.../v1/profiles?service=NAME&amp;type=cpu'</code>.</td></tr>
{{end}}
</tbody>
</table>
{{end}}`

const profileDetailTemplate = `{{define "content"}}
<p class="dim">{{.Meta.Service}} · {{.Meta.Type}} · {{.Meta.Bytes}} B · started {{ts .Meta.StartedAt}}</p>
{{if .Error}}
<p class="bad">This profile could not be parsed: {{.Error}}</p>
<p class="dim">The blob is stored and downloadable; only the rendering failed.</p>
{{else}}
<p class="dim">{{.Flame.Frames}} frames · total {{.Flame.Total}} {{.Flame.Unit}}{{if .Flame.Truncated}} · capped at depth 40{{end}}</p>
{{.Flame.HTML}}
<p class="dim">Width is cost, each row is stack depth, hover a bar for the frame. This is a profile of a time window, not of a single request.</p>

<h2>Hot functions by self time</h2>
<p class="dim">Self time is time spent <i>in</i> the function itself, excluding what it called — the ranking to act on. Cumulative includes children.</p>
<table>
<thead><tr><th>#</th><th>Function</th><th>Self</th><th>Self %</th><th>Cumulative</th></tr></thead>
<tbody>
{{range $i, $f := .Flame.Top}}
<tr>
  <td class="dim">{{inc $i}}</td>
  <td><code>{{$f.Name}}</code></td>
  <td><b>{{ns $f.Self}}</b></td>
  <td>
    <span class="bar"><span class="fill bad-bg" style="{{pctFrac (div $f.SelfPct 100)}}"></span></span>
    {{printf "%.1f%%" $f.SelfPct}}
  </td>
  <td class="dim">{{ns $f.Cum}}</td>
</tr>
{{else}}
<tr><td colspan="5" class="dim">No self time attributed — the profile may contain only samples without user frames.</td></tr>
{{end}}
</tbody>
</table>
{{end}}
<a href="/profiles">← all profiles</a>
{{end}}`

const servicesTemplate = `{{define "content"}}
{{if .Flagged}}
<h2>{{len .Flagged}} endpoint(s) worth checking</h2>
<p class="dim">Derived from all hits in the window: median above {{short .Config.SlowMedian}}, p95 above {{short .Config.SlowP95}}, or ≥5% failures. Needs at least {{.Config.MinSamples}} hits before judging, so a single slow request cannot raise a flag.</p>
<table>
<thead><tr><th>Endpoint</th><th>Why</th><th>Hits</th><th>Median</th><th>p95</th><th>Errors</th></tr></thead>
<tbody>
{{range .Flagged}}
<tr>
  <td><b>{{if .Endpoint.Method}}{{.Endpoint.Method}} {{end}}{{.Endpoint.Route}}</b><br><span class="dim">{{.Endpoint.Service}}</span></td>
  <td class="{{if eq .Verdict.State "failing"}}bad{{else}}warn{{end}}">{{.Verdict.Reason}}</td>
  <td>{{.Endpoint.Count}}</td>
  <td>{{short .Endpoint.P50}}</td>
  <td>{{short .Endpoint.P95}}</td>
  <td class="{{if gt .Endpoint.Errors 0}}bad{{end}}">{{.Endpoint.Errors}}</td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<h2>Nothing worth checking</h2>
<p class="dim">No endpoint exceeded the thresholds in this window (median ≤ {{short .Config.SlowMedian}}, p95 ≤ {{short .Config.SlowP95}}, &lt;5% failures, ≥{{.Config.MinSamples}} hits).</p>
{{end}}

{{template "period" .}}
<p class="dim">{{.WindowLabel}} window · {{.TotalSpans}} operations · {{.TotalErrors}} failed · from {{.From}} to {{.To}}</p>

<table>
<thead><tr><th>Service</th><th>Operations</th><th>Errors</th><th>Error rate</th><th>p50</th><th>p95</th><th>Routes</th><th>Last seen</th></tr></thead>
<tbody>
{{range .Rows}}
<tr class="rowlink">
  <td><a class="rowhit" href="/services/{{.Service}}">open service {{.Service}}</a><a href="/services/{{.Service}}">{{.Service}}</a></td>
  <td>{{.Spans}}</td>
  <td class="{{if gt .Errors 0}}bad{{end}}">{{.Errors}}</td>
  <td>
    <span class="bar"><span class="fill {{if gt .Errors 0}}bad-bg{{end}}" style="{{pct .ErrorRate}}"></span></span>
    {{pctnum .ErrorRate}}
  </td>
  <td>{{dur .P50}}</td>
  <td>{{dur .P95}}</td>
  <td>{{.Routes}}</td>
  <td class="dim">{{ts .LastSeen}}</td>
</tr>
{{else}}
<tr><td colspan="8" class="dim">No operations in this window. Point a service at /v1/traces, or widen the window with ?from=YYYY-MM-DD HH:MM.</td></tr>
{{end}}
</tbody>
</table>

<h2>Top endpoints by total time</h2>
<p class="dim">Ranked by the time this endpoint consumed in the window (count × duration), not by its worst single request. This is the order to tune in: the top row is where the latency budget actually goes.</p>
<table>
<thead><tr><th>#</th><th>Endpoint</th><th>Service</th><th>Requests</th><th>Total time</th><th>Share</th><th>Median</th><th>p95</th><th>Errors</th><th>Verdict</th></tr></thead>
<tbody>
{{range $i, $e := .Endpoints}}
<tr class="rowlink">
  <td><a class="rowhit" href="/operations/{{$e.Service}}?route={{$e.Route}}&method={{$e.Method}}">{{$e.Method}} {{$e.Route}} in {{$e.Service}}</a><span class="dim">{{inc $i}}</span></td>
  <td>{{if $e.Method}}{{$e.Method}} {{end}}{{$e.Route}}</td>
  <td>{{$e.Service}}</td>
  <td>{{$e.Count}}</td>
  <td><b>{{dur $e.Total}}</b></td>
  <td>
    <span class="bar"><span class="fill" style="{{pct (share $e $.GrandTotal)}}"></span></span>
    {{pctnum (share $e $.GrandTotal)}}
  </td>
  <td>{{dur $e.P50}}</td>
  <td>{{dur $e.P95}}</td>
  <td class="{{if gt $e.Errors 0}}bad{{end}}">{{$e.Errors}}</td>
  <td class="{{if eq (verdictState $e $.Judgements) "slow"}}warn{{else if eq (verdictState $e $.Judgements) "ok"}}dim{{end}}">{{verdictLabel $e $.Judgements}}</td>
</tr>
{{else}}
<tr><td colspan="9" class="dim">No HTTP endpoints in this window.</td></tr>
{{end}}
</tbody>
</table>
{{end}}`

const servicesTemplateExtra = `{{define "_unused"}}{{end}}`

const serviceDetailTemplate = `{{define "content"}}
{{template "period" .}}
<p class="dim">{{human .Window}} window · {{.Stat.Spans}} operations · {{.Stat.Errors}} failed · p50 {{dur .Stat.P50}} · p95 {{dur .Stat.P95}} · {{.Stat.Routes}} routes · from {{.From}} to {{.To}}</p>

<h2>Worth checking</h2>
{{if or .Bad5 .Bad4}}
<p class="warn">This service has failed responses in the window: {{len .Bad5}} 5xx fingerprints, {{len .Bad4}} 4xx fingerprints.</p>
{{else}}
<p class="dim">No failed responses in this window.</p>
{{end}}

<h2>Server errors (5xx) <span class="dim">— our side failed</span></h2>
<table>
<thead><tr><th>Count</th><th>Request</th><th>Status</th><th>Last seen</th><th></th></tr></thead>
<tbody>
{{range .Bad5}}
<tr>
  <td class="bad"><b>{{.Count}}</b></td>
  <td><b>{{.Method}}</b> {{.Route}}</td>
  <td><span class="code bad">{{.StatusCode}}</span></td>
  <td class="dim">{{ts .LastSeen}}</td>
  <td><a href="/traces/{{.ExampleTrace}}">example</a></td>
</tr>
{{else}}
<tr><td colspan="5" class="dim">None.</td></tr>
{{end}}
</tbody>
</table>

<h2>Bad requests (4xx) <span class="dim">— the caller's side failed</span></h2>
<table>
<thead><tr><th>Count</th><th>Request</th><th>Status</th><th>Last seen</th><th></th></tr></thead>
<tbody>
{{range .Bad4}}
<tr>
  <td class="warn"><b>{{.Count}}</b></td>
  <td><b>{{.Method}}</b> {{.Route}}</td>
  <td><span class="code warn">{{.StatusCode}}</span></td>
  <td class="dim">{{ts .LastSeen}}</td>
  <td><a href="/traces/{{.ExampleTrace}}">example</a></td>
</tr>
{{else}}
<tr><td colspan="5" class="dim">None.</td></tr>
{{end}}
</tbody>
</table>

<h2>Endpoints by total time</h2>
<table>
<thead><tr><th>#</th><th>Request</th><th>Requests</th><th>Total</th><th>Median</th><th>p95</th><th>Errors</th><th>Verdict</th></tr></thead>
<tbody>
{{range $i, $e := .Endpoints}}
<tr class="rowlink">
  <td><a class="rowhit" href="/operations/{{$e.Service}}?route={{$e.Route}}&method={{$e.Method}}">{{$e.Method}} {{$e.Route}} in {{$e.Service}}</a><span class="dim">{{inc $i}}</span></td>
  <td><b>{{$e.Method}}</b> {{$e.Route}}</td>
  <td>{{$e.Count}}</td>
  <td><b>{{dur $e.Total}}</b></td>
  <td>{{dur $e.P50}}</td>
  <td>{{dur $e.P95}}</td>
  <td class="{{if gt $e.Errors 0}}bad{{end}}">{{$e.Errors}}</td>
  <td class="{{if eq (verdictState $e $.Judgements) "slow"}}warn{{else if eq (verdictState $e $.Judgements) "ok"}}dim{{end}}">{{verdictLabel $e $.Judgements}}</td>
</tr>
{{else}}
<tr><td colspan="8" class="dim">No HTTP endpoints in this window.</td></tr>
{{end}}
</tbody>
</table>

<p><a href="/services">← all services</a> · <a href="/operations/{{.Service}}">operations for this service</a></p>
{{end}}`

const incidentsTemplate = `{{define "content"}}
{{template "period" .}}
<p class="dim">{{human .Window}} window · {{.Total}} failed operations across {{len .Groups}} fingerprints · from {{.From}} to {{.To}}</p>

<table>
<thead><tr><th>Count</th><th>Service</th><th>Route</th><th>Outcome</th><th>Error</th><th>Message</th><th>Traces</th><th>First seen</th><th>Last seen</th></tr></thead>
<tbody>
{{range .Groups}}
<tr class="rowlink">
  <td><a class="rowhit" href="/operations/{{.Service}}?route={{.Route}}&outcome={{.Outcome}}">{{.Outcome}} {{.Route}} in {{.Service}}</a>
    <span class="bar"><span class="fill bad-bg" style="{{bar .Count $.Max}}"></span></span>
    <b>{{.Count}}</b>
  </td>
  <td>{{.Service}}</td>
  <td>{{if .Route}}{{.Route}}{{else}}<span class="dim">—</span>{{end}}</td>
  <td class="{{class .Outcome}}">{{.Outcome}}</td>
  <td>{{if .ErrorType}}{{.ErrorType}}{{else}}<span class="dim">—</span>{{end}}</td>
  <td class="dim">{{if .StatusMessage}}{{.StatusMessage}}{{else}}—{{end}}</td>
  <td>{{.Traces}}</td>
  <td class="dim">{{ts .FirstSeen}}</td>
  <td class="dim">{{ts .LastSeen}}</td>
</tr>
{{else}}
<tr><td colspan="9" class="dim">No failures in this window.</td></tr>
{{end}}
</tbody>
</table>
{{end}}`

const policiesTemplate = `{{define "content"}}
<p class="dim">Active policy version <b>{{.Policy.Version}}</b> · preview against the last {{human .Window}} ({{.Sampled}} sampled operations) · storage estimate at {{.BytesPerDa}} B/span (measured)</p>

<table>
<thead><tr><th>Rule</th><th>Priority</th><th>Match</th><th>Keep (trace)</th><th>Matches (24h)</th><th>Est. bytes/day</th><th>Note</th></tr></thead>
<tbody>
<tr>
  <td><b>defaults</b></td>
  <td>—</td>
  <td class="dim">everything not matched by a rule</td>
  <td>{{human .Defaults}}</td>
  <td>—</td>
  <td>—</td>
  <td></td>
</tr>
{{range .Preview}}
<tr>
  <td>{{.Name}}</td>
  <td>{{.Priority}}</td>
  <td class="dim">{{.Match}}</td>
  <td>{{human .Retention}}</td>
  <td>{{.Matches}}</td>
  <td>{{.BytesPerDay}}</td>
  <td class="warn">{{if .ShadowedBy}}cannot take effect — claimed by {{.ShadowedBy}}{{end}}</td>
</tr>
{{end}}
</tbody>
</table>

<details>
<summary>Accessible table / policy source</summary>
<pre>{{.Policy.Source}}</pre>
</details>
{{end}}`

const operationsTemplate = `{{define "content"}}
{{if .Strip}}
<div class="strip">
  <a href="/operations" class="{{if not .Service}}on{{end}}">all services</a>
  {{range .Strip}}
  <a href="/operations/{{.Service}}" class="{{if eq .Service $.Service}}on{{end}}">{{.Service}} <span class="n">{{.Spans}}</span></a>
  {{end}}
</div>
{{end}}

{{/* One box. The viewer arrives holding a media token, a trace id or a customer
     id and should not have to work out which field it belongs in. */}}
<form method="get" action="{{.BasePath}}">
  <p class="find">
    <input type="search" name="q" value="{{.Text}}" size="46"
      placeholder="paste a media token, customer id, trace id, route or error — or leave empty to see everything">
    <button type="submit">Find</button>
    {{if .Text}}<a class="clear" href="{{.LiveURL}}">clear</a>{{end}}
  </p>
  {{/* Filters in force, each removable. Anything hiding in "more filters" shows
       up here, because an invisible filter turns a typo into "not found". */}}
  {{if .Filters}}<p class="chips">
    <span class="k">filters</span>
    {{range .Filters}}<span class="chip">{{.Label}} <a href="{{.RemoveURL}}" title="remove this filter">×</a></span>{{end}}
    <a class="clear" href="{{.ClearURL}}">clear all</a>
  </p>{{end}}
  {{/* Click, do not type: a date field is the slowest way to ask "the last
       hour?". A typed range still works, but only when you mean to pin one. */}}
  <p class="presets">
    <span class="k">period</span>
    {{range .Presets}}<a href="{{.URL}}" class="{{if .On}}on{{end}}">{{.Label}}</a>{{end}}
    <span class="pintoggle">
      <input type="hidden" name="last" value="{{.Last}}">
      <label>pin <input type="text" name="from" size="16" placeholder="from" value="{{if not .Live}}{{.From}}{{end}}"></label>
      <label><input type="text" name="to" size="16" placeholder="to" value="{{if not .Live}}{{.To}}{{end}}"></label>
    </span>
  </p>
  <details class="morefilters">
    <summary>more filters</summary>
    <p>
      <input type="text" name="service" placeholder="service" value="{{.Query.Service}}">
      <input type="text" name="route" placeholder="route contains… e.g. period-pass" value="{{.Query.Route}}">
      <input type="text" name="method" placeholder="method" value="{{.Query.Method}}">
      <select name="outcome">
        <option value="">all outcomes</option>
        {{range .Outcomes}}<option value="{{.}}" {{if eq . $.Query.Outcome}}selected{{end}}>{{.}}</option>{{end}}
      </select>
      <label><input type="checkbox" name="all" value="1" {{if .Query.IncludeBackground}}checked{{end}}> include background jobs</label>
    </p>
  </details>
</form>

{{/* Everything the clock moves lives in here and is swapped as one unit. */}}
<section id="live"
  {{if .Live}}data-live-url="{{.LiveURL}}" data-live-interval="{{.PollSeconds}}"{{end}}>
<p class="dim">{{.WindowLabel}} window · {{.TotalSpans}} operations · {{.TotalErrors}} failed · from {{.From}} to {{.To}}
{{if .Live}} · <b>live</b>{{if .PollSeconds}}, updates every {{.PollSeconds}}s{{end}} — <a href="{{.PinnedURL}}">freeze this window</a>{{else}} · frozen — <a href="{{.LiveURL}}">back to live</a>{{end}}</p>
<table>
<thead><tr><th>Time</th><th>Request</th><th>Duration</th><th>Outcome</th></tr></thead>
<tbody>
{{range .Spans}}
<tr class="rowlink">
  <td><a class="rowhit" href="/traces/{{.TraceID}}">open trace {{.TraceID}}</a>{{ts .StartTime}}</td>
  <td>
    {{if .HTTPMethod}}<b>{{.HTTPMethod}}</b>{{end}} {{if .HTTPRoute}}{{.HTTPRoute}}{{else}}{{.Name}}{{end}}
    {{if .StatusCode}}<span class="code {{if ge .StatusCode 500}}bad{{else if ge .StatusCode 400}}warn{{end}}">{{.StatusCode}}</span>{{end}}
    {{/* The service is only ambiguous when the list spans services: on a scoped
         page every row would repeat the same name. */}}
    {{if not $.Service}}<span class="svc">{{.ServiceName}}</span>{{end}}
  </td>
  <td>{{dur .Duration}}</td>
  <td class="{{if or (eq .Outcome "server_error") (eq .Outcome "error")}}bad{{else if eq .Outcome "client_error"}}warn{{else if or (eq .Outcome "success") (eq .Outcome "ok")}}good{{end}}">{{.Outcome}}</td>
</tr>
{{else}}
<tr><td colspan="4" class="dim">{{.EmptyReason}}
      Check the filters above — a filter on method or outcome excludes whole
      families of requests. <a href="{{.ClearURL}}">clear all filters</a>, or
      widen the period.</td></tr>
{{end}}
</tbody>
</table>
{{if or .OlderURL .HasCursor}}
<p class="dim">
  {{if .HasCursor}}<a href="{{.NewestURL}}">↑ newest</a>{{if .OlderURL}} · {{end}}{{end}}
  {{if .OlderURL}}<a href="{{.OlderURL}}">older →</a>{{end}}
</p>
{{end}}
</section>
{{end}}`

const traceTemplate = `{{define "content"}}
{{/* The failing span first: someone opened this trace because a request failed,
     and it used to be one grey row among many. */}}
{{range .Errors}}
<div class="errbox">
  <p class="errhead">
    <span class="errbadge {{if ge .Status 500}}bad{{else}}warn{{end}}">
      {{if .Status}}{{.Status}} · {{end}}{{.Outcome}}
    </span>
    <b>{{.Span}}</b>
  </p>
  <p class="errwhy">
    {{if .Reported}}<b>Reported by the service:</b> {{.Reported}}
    {{else if .Inferred}}<b>Probably:</b> {{.Inferred}}.
      <span class="dim">Inferred from the status code — the service sent no message,
      error type or logs with this span.</span>
    {{else}}<span class="dim">No reason was recorded for this failure.</span>{{end}}
  </p>
</div>
{{end}}

<p>Trace ID: <code>{{.TraceID}}</code></p>

<table class="summary">
<tr>
  <td><b class="{{if or (eq .Summary.Outcome "server_error") (eq .Summary.Outcome "error")}}bad{{else if eq .Summary.Outcome "client_error"}}warn{{end}}">{{.Summary.Outcome}}</b><span class="dim">trace outcome</span></td>
  <td><b>{{.Summary.ErrorSpans}} / {{.Summary.Spans}}</b><span class="dim">error spans / spans</span></td>
  <td><b>{{dur .Summary.Duration}}</b><span class="dim">wall time</span></td>
  <td><b>{{.Summary.Slowest.Name}}</b><span class="dim">slowest span ({{dur .Summary.Slowest.Duration}})</span></td>
  <td><b>{{range $i, $s := .Summary.Services}}{{if $i}}, {{end}}{{$s}}{{end}}</b><span class="dim">services</span></td>
</tr>
</table>

{{if .Request}}
<table class="reqstrip">
<tr>
  {{if .Request.Method}}<td><span class="k">method</span><b>{{.Request.Method}}</b></td>{{end}}
  {{if .Request.Route}}<td><span class="k">route</span><b>{{.Request.Route}}</b></td>{{end}}
  {{if .Request.Status}}<td><span class="k">status</span><b class="{{if ge .Request.Status 500}}bad{{else if ge .Request.Status 400}}warn{{else}}good{{end}}">{{.Request.Status}}</b></td>{{end}}
  {{if .Request.Identity}}<td><span class="k">account</span><b>{{.Request.Identity}}</b></td>{{end}}
  {{if .Request.Email}}<td><span class="k">email</span><b>{{.Request.Email}}</b></td>{{end}}
  {{if .Request.Phone}}<td><span class="k">phone</span><b>{{.Request.Phone}}</b></td>{{end}}
  {{if .Request.ClientIP}}<td><span class="k">client</span><b>{{.Request.ClientIP}}</b></td>{{end}}
  {{if .Request.UserAgent}}<td><span class="k">user agent</span><b>{{.Request.UserAgent}}</b></td>{{end}}
  {{if .Request.Target}}<td><span class="k">target</span><b>{{.Request.Target}}</b></td>{{end}}
</tr>
</table>
{{end}}

{{if .Request}}
<div class="payload">
  <div class="k">request body</div>
  {{if .Payload}}
  <pre>{{.Payload}}</pre>
  {{else}}
  <p class="nobody">This request has no body payload{{if or (eq .Request.Method "GET") (eq .Request.Method "HEAD")}} — {{.Request.Method}} requests normally carry none{{end}}.
    {{if .Request.Query}}<br>Its data is in the query string: <code>{{.Request.Query}}</code>{{else}}The caller sent no body and no query string for this request.{{end}}
  </p>
  {{end}}
</div>
{{end}}

{{if .Request.Headers}}
<details class="headers">
  <summary>{{len .Request.Headers}} request headers</summary>
  <table class="attrs">
  <tbody>
  {{range .Request.Headers}}
    <tr><td class="key"><code>{{.Key}}</code></td><td class="val">{{.Value}}</td></tr>
  {{end}}
  </tbody>
  </table>
</details>
{{end}}

{{.Waterfall.HTML}}

<table>
<thead><tr><th>Span</th><th>Service</th><th>Kind</th><th>Detail</th><th>Offset</th><th>Duration</th><th>Outcome</th></tr></thead>
<tbody>
{{range .Rows}}
<tr>
  <td class="tree">{{.TreeHTML}}</td>
  <td>{{.Span.ServiceName}}</td>
  <td>{{.Span.Kind}}</td>
  <td class="dim">{{if .Span.DBSystem}}{{.Span.DBSystem}} {{.Span.DBOperation}}{{if .Span.DBQuerySummary}} · {{.Span.DBQuerySummary}}{{end}}{{else if .Span.RPCSystem}}{{.Span.RPCSystem}} {{.Span.RPCService}}/{{.Span.RPCMethod}}{{else if .Span.MessagingSystem}}{{.Span.MessagingSystem}} {{.Span.MessagingOperation}} {{.Span.MessagingDestination}}{{end}}{{if .Span.StatusMessage}} · {{.Span.StatusMessage}}{{end}}</td>
  <td>{{dur .Offset}}</td>
  <td>{{dur .Span.Duration}}</td>
  <td class="{{if or (eq .Span.Outcome "server_error") (eq .Span.Outcome "error")}}bad{{else if eq .Span.Outcome "client_error"}}warn{{end}}">{{.Span.Outcome}}</td>
</tr>
{{end}}
</tbody>
</table>

{{if or .ExactLogs .Correlated}}
<h2>Log snippets</h2>
<p class="dim">Captured because this operation triggered a snapshot. Not a log stream: a bounded window around the event.</p>

{{if .ExactLogs}}
<h3>Direct evidence <span class="dim">({{len .ExactLogs}} lines carrying this trace id)</span></h3>
<table>
<thead><tr><th>Time</th><th>Service</th><th>Severity</th><th>Message</th></tr></thead>
<tbody>
{{range .ExactLogs}}
<tr>
  <td>{{ts .Time}}</td>
  <td>{{.Service}}</td>
  <td class="{{if or (eq .Severity "error") (eq .Severity "fatal")}}bad{{else if eq .Severity "warn"}}warn{{end}}">{{.Severity}}</td>
  <td>{{.Body}}</td>
</tr>
{{end}}
</tbody>
</table>
{{end}}

{{if .Correlated}}
<h3>Time-correlated candidates <span class="dim">({{len .Correlated}} lines without a trace id — same service, same window)</span></h3>
<table>
<thead><tr><th>Time</th><th>Service</th><th>Severity</th><th>Message</th></tr></thead>
<tbody>
{{range .Correlated}}
<tr>
  <td>{{ts .Time}}</td>
  <td>{{.Service}}</td>
  <td class="{{if or (eq .Severity "error") (eq .Severity "fatal")}}bad{{else if eq .Severity "warn"}}warn{{end}}">{{.Severity}}</td>
  <td>{{.Body}}</td>
</tr>
{{end}}
</tbody>
</table>
{{end}}
{{else}}
<p class="dim">No log snapshot for this operation. Nothing is captured unless a trigger fires.</p>
{{end}}

{{if .Details}}
<h2>Span data</h2>
<p class="dim">Attributes per span, in call order. A database call appears underneath the function that issued it, so the statement is attached to its caller rather than listed apart.</p>
{{range .Details}}
<details class="spanblock{{if eq .Depth 0}} root{{end}}" {{if lt .Depth 3}}open{{end}}>
  <summary>
    <span class="ind" style="{{indent .Depth}}"></span>
    <b>{{.Name}}</b>
    <span class="dim">{{.Duration}}</span>
    {{if .Kind}}<span class="dim">· {{.Kind}}</span>{{end}}
    {{if eq .Outcome "error"}}<span class="bad">· error</span>{{else if eq .Outcome "server_error"}}<span class="bad">· failed</span>{{end}}
  </summary>
  {{if .Rows}}
  <table class="attrs">
  <tbody>
  {{range .Rows}}
    <tr><td class="key"><code>{{.Key}}</code></td><td class="val">{{.Value}}</td></tr>
  {{end}}
  </tbody>
  </table>
  {{else}}
  <p class="dim">No attributes recorded.</p>
  {{end}}
</details>
{{end}}
{{end}}

<details>
<summary>Accessible table / raw fields</summary>
<table>
<thead><tr><th>Span ID</th><th>Parent</th><th>Type</th><th>Start</th><th>Code</th><th>Links</th></tr></thead>
<tbody>
{{range .Rows}}
<tr>
  <td><code>{{.Span.SpanID}}</code></td>
  <td>{{if .Span.ParentSpanID}}<code>{{.Span.ParentSpanID}}</code>{{else}}—{{end}}</td>
  <td>{{.Span.OperationType}}</td>
  <td>{{ts .Span.StartTime}}</td>
  <td>{{if .Span.StatusCode}}{{.Span.StatusCode}}{{else}}—{{end}}</td>
  <td>{{len .Span.Links}}</td>
</tr>
{{end}}
</tbody>
</table>
</details>
{{end}}`
