// Package ui renders server-side HTML views. Zero browser JavaScript.
package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

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

	q := store.Query{
		Service: service,
		Route:   r.URL.Query().Get("route"),
		Method:  r.URL.Query().Get("method"),
		Outcome: r.URL.Query().Get("outcome"),
		// Background jobs are opt-in: they are useful, but they are not requests.
		IncludeBackground: r.URL.Query().Get("all") == "1",
		Limit:             store.MaxLimit,
	}
	// Time range: default to the last 24h so the page never scans everything.
	to := u.clock()
	from := to.Add(-24 * time.Hour)
	if t, ok := parseTime(r.URL.Query().Get("from")); ok {
		from = t
	}
	if t, ok := parseTime(r.URL.Query().Get("to")); ok {
		to = t
	}
	q.From, q.To = from, to

	spans, err := u.Store.ListOperations(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, operationsView, map[string]any{
		"Title":    title,
		"Service":  service,
		"BasePath": basePath,
		"Spans":    spans,
		"Query":    q,
		"Outcomes": outcomes,
		"From":     inDisplay(from).Format(inputTimeLayout),
		"To":       inDisplay(to).Format(inputTimeLayout),
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

	render(w, policiesView, map[string]any{
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
func traceHeadline(spans []model.Span, traceID string) (title, subtitle string, req *traceRequest) {
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
		UserAgent: root.Attributes["user_agent.original"],
		Service:   root.ServiceName,
	}
	if req.Method == "" && req.Route == "" && req.Target == "" {
		req = nil
	}
	return title, subtitle, req
}

// traceRequest is the request metadata shown above the waterfall.
type traceRequest struct {
	Method    string
	Route     string
	Target    string
	Status    int
	ClientIP  string
	UserAgent string
	Service   string
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
		for _, k := range keys {
			d.Rows = append(d.Rows, attrRow{Key: k, Value: row.Span.Attributes[k]})
		}
		details = append(details, d)
	}

	headline, subtitle, req := traceHeadline(spans, traceID)
	render(w, traceView, map[string]any{
		"Details":     details,
		"Title":       headline,
		"Subtitle":    subtitle,
		"HeaderRight": traceStamp(summary, spans[0]),
		"Request":     req,
		"TraceID":     traceID,
		"Rows":        rows,
		"Summary":     summary,
		"ExactLogs":   exact,
		"Correlated":  correlated,
		"Waterfall":   buildWaterfall(spans, 980),
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
)

func render(w http.ResponseWriter, t *template.Template, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Zone"]; !ok {
		data["Zone"] = displayZoneLabel
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "page", data); err != nil {
		// The header is already sent; log-worthy, but never a silent 200 with a
		// truncated body.
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

var pageBase = template.Must(template.New("page").Funcs(funcs).Parse(pageShell))

const pageShell = `{{define "page"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — leutik-tarik</title>
<style>
html,body{margin:0;padding:0}
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
.pagetop{display:flex;justify-content:space-between;align-items:flex-start;gap:1rem;flex-wrap:wrap}
.pagetop h1{margin:.2rem 0 .25rem}
.sub{margin:0;font-size:.85rem;color:#555}
.sub code{background:#f2f2f2;padding:.05rem .3rem;font-size:.8rem}
.topright{text-align:right;font-size:.85rem;color:#333;white-space:nowrap}
.topright .big{display:block;font-size:1rem;font-weight:600}
.topright .tz{display:block;font-size:.75rem;color:#777}
@media (max-width:700px){.topright{text-align:left}}
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
<nav><a href="/operations">Operations</a><a href="/services">Services</a><a href="/incidents">Incidents</a><a href="/profiles">Profiles</a><a href="/policies">Policies</a></nav>
<div class="pagetop">
  <div>
    <h1>{{.Title}}</h1>
    {{if .Subtitle}}<p class="sub">{{.Subtitle}}</p>{{end}}
  </div>
  {{if .HeaderRight}}<div class="topright">{{.HeaderRight}}</div>{{end}}
</div>
<p class="zone" id="zone">All times shown in {{.Zone}}{{if .Service}} · scoped to {{.Service}} — <a href="/operations">show all services</a>{{end}}</p>
<div class="content">
{{template "content" .}}
</div>
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

<p class="dim">{{human .Window}} window · {{.TotalSpans}} operations · {{.TotalErrors}} failed · from {{.From}} to {{.To}}</p>

<table>
<thead><tr><th>Service</th><th>Operations</th><th>Errors</th><th>Error rate</th><th>p50</th><th>p95</th><th>Routes</th><th>Last seen</th></tr></thead>
<tbody>
{{range .Rows}}
<tr>
  <td><a href="/services/{{.Service}}">{{.Service}}</a></td>
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
<tr>
  <td class="dim">{{inc $i}}</td>
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
<p class="dim">{{human .Window}} window · {{.Stat.Spans}} operations · {{.Stat.Errors}} failed · p50 {{dur .Stat.P50}} · p95 {{dur .Stat.P95}} · {{.Stat.Routes}} routes</p>

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
<tr>
  <td class="dim">{{inc $i}}</td>
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
<p class="dim">{{human .Window}} window · {{.Total}} failed operations across {{len .Groups}} fingerprints</p>

<table>
<thead><tr><th>Count</th><th>Service</th><th>Route</th><th>Outcome</th><th>Error</th><th>Message</th><th>Traces</th><th>First seen</th><th>Last seen</th></tr></thead>
<tbody>
{{range .Groups}}
<tr>
  <td>
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
<form method="get" action="{{.BasePath}}">
  <input type="text" name="service" placeholder="service" value="{{.Query.Service}}">
  <input type="text" name="route" placeholder="route" value="{{.Query.Route}}">
  <input type="text" name="method" placeholder="method" value="{{.Query.Method}}">
  <select name="outcome">
    <option value="">all outcomes</option>
    {{range .Outcomes}}<option value="{{.}}" {{if eq . $.Query.Outcome}}selected{{end}}>{{.}}</option>{{end}}
  </select>
  <label>from <input type="text" name="from" size="18" placeholder="YYYY-MM-DD HH:MM" value="{{.From}}"></label>
  <label>to <input type="text" name="to" size="18" placeholder="YYYY-MM-DD HH:MM" value="{{.To}}"></label>
  <label><input type="checkbox" name="all" value="1" {{if .Query.IncludeBackground}}checked{{end}}> include background jobs</label>
  <button type="submit">Search</button>
</form>
<table>
<thead><tr><th>Time</th><th>Request</th><th>Duration</th><th>Outcome</th><th>Trace</th></tr></thead>
<tbody>
{{range .Spans}}
<tr>
  <td>{{ts .StartTime}}</td>
  <td>
    {{if .HTTPMethod}}<b>{{.HTTPMethod}}</b>{{end}} {{if .HTTPRoute}}{{.HTTPRoute}}{{else}}{{.Name}}{{end}}
    {{if .StatusCode}}<span class="code {{if ge .StatusCode 500}}bad{{else if ge .StatusCode 400}}warn{{end}}">{{.StatusCode}}</span>{{end}}
  </td>
  <td>{{dur .Duration}}</td>
  <td class="{{if or (eq .Outcome "server_error") (eq .Outcome "error")}}bad{{else if eq .Outcome "client_error"}}warn{{else if or (eq .Outcome "success") (eq .Outcome "ok")}}good{{end}}">{{.Outcome}}</td>
  <td><a href="/traces/{{.TraceID}}">trace</a></td>
</tr>
{{else}}
<tr><td colspan="5" class="dim">No operations in this window.</td></tr>
{{end}}
</tbody>
</table>
{{end}}`

const traceTemplate = `{{define "content"}}
<p>Trace ID: <code>{{.TraceID}}</code></p>

<table class="summary">
<tr>
  <td><b class="{{if or (eq .Summary.Outcome "server_error") (eq .Summary.Outcome "error")}}bad{{end}}">{{.Summary.Outcome}}</b><span class="dim">trace outcome</span></td>
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
  {{if .Request.ClientIP}}<td><span class="k">client</span><b>{{.Request.ClientIP}}</b></td>{{end}}
  {{if .Request.UserAgent}}<td><span class="k">user agent</span><b>{{.Request.UserAgent}}</b></td>{{end}}
  {{if .Request.Target}}<td><span class="k">target</span><b>{{.Request.Target}}</b></td>{{end}}
</tr>
</table>
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
