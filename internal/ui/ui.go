// Package ui renders server-side HTML views. Zero browser JavaScript.
package ui

import (
	"fmt"
	"html/template"
	"net/http"
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
	q := store.Query{
		Service: r.URL.Query().Get("service"),
		Route:   r.URL.Query().Get("route"),
		Method:  r.URL.Query().Get("method"),
		Outcome: r.URL.Query().Get("outcome"),
		Limit:   store.MaxLimit,
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
		"Title":    "Operations",
		"Spans":    spans,
		"Query":    q,
		"Outcomes": outcomes,
		"From":     from.Format(inputTimeLayout),
		"To":       to.Format(inputTimeLayout),
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
func parseTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{inputTimeLayout, time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type traceRow struct {
	Span   model.Span
	Offset time.Duration
	// Depth is 0 for the root; children are indented one level.
	Depth int
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
	rows := make([]traceRow, len(spans))
	for i, s := range spans {
		rows[i] = traceRow{
			Span:   s,
			Offset: s.StartTime.Sub(start),
			Depth:  depthOf(spans, s),
		}
	}
	var exact, correlated []model.LogEvent
	for _, l := range logs {
		if l.Exact {
			exact = append(exact, l)
		} else {
			correlated = append(correlated, l)
		}
	}
	render(w, traceView, map[string]any{
		"Title":      "Trace " + traceID,
		"TraceID":    traceID,
		"Rows":       rows,
		"Summary":    summary,
		"ExactLogs":  exact,
		"Correlated": correlated,
		"Waterfall":  buildWaterfall(spans, 980),
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
		return t.Format("2006-01-02 15:04:05.000")
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
	profilesView      = template.Must(template.Must(pageBase.Clone()).Parse(profilesTemplate))
	profileDetailView = template.Must(template.Must(pageBase.Clone()).Parse(profileDetailTemplate))
	traceView         = template.Must(template.Must(pageBase.Clone()).Parse(traceTemplate))
	policiesView      = template.Must(template.Must(pageBase.Clone()).Parse(policiesTemplate))
)

func render(w http.ResponseWriter, t *template.Template, data map[string]any) {
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
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:1.5rem;color:#111}
table{border-collapse:collapse;width:100%;font-size:.875rem}
th,td{border-bottom:1px solid #ddd;padding:.35rem .55rem;text-align:left;vertical-align:top}
th{background:#f5f5f5;font-weight:600}
a{color:#06c}
nav{margin-bottom:1rem}nav a{margin-right:1rem}
code{background:#f5f5f5;padding:.1rem .3rem}
.bad{color:#b00020}
.warn{color:#8a5b00}
.dim{color:#666}
.summary{display:flex;gap:1.5rem;flex-wrap:wrap;margin:.5rem 0 1rem}
.summary div{min-width:9rem}
.summary b{display:block;font-size:1.05rem}
.bar{display:inline-block;width:70px;height:8px;background:#eee;border-radius:4px;vertical-align:middle;overflow:hidden}
.bar .fill{display:block;height:8px;background:#1a7f37}
.bar .fill.bad-bg{background:#b00020}
.legend{font-size:.8rem;color:#666;margin:.3rem 0 .8rem}
</style>
</head>
<body>
<nav><a href="/operations">Operations</a><a href="/services">Services</a><a href="/incidents">Incidents</a><a href="/profiles">Profiles</a><a href="/policies">Policies</a></nav>
<h1>{{.Title}}</h1>
{{template "content" .}}
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
  <td><a href="/operations?service={{.Service}}">{{.Service}}</a></td>
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
<form method="get" action="/operations">
  <input type="text" name="service" placeholder="service" value="{{.Query.Service}}">
  <input type="text" name="route" placeholder="route" value="{{.Query.Route}}">
  <input type="text" name="method" placeholder="method" value="{{.Query.Method}}">
  <select name="outcome">
    <option value="">all outcomes</option>
    {{range .Outcomes}}<option value="{{.}}" {{if eq . $.Query.Outcome}}selected{{end}}>{{.}}</option>{{end}}
  </select>
  <label>from <input type="text" name="from" size="18" placeholder="YYYY-MM-DD HH:MM" value="{{.From}}"></label>
  <label>to <input type="text" name="to" size="18" placeholder="YYYY-MM-DD HH:MM" value="{{.To}}"></label>
  <button type="submit">Search</button>
</form>
<table>
<thead><tr><th>Time</th><th>Service</th><th>Method</th><th>Route</th><th>Status</th><th>Duration</th><th>Outcome</th><th>Trace</th></tr></thead>
<tbody>
{{range .Spans}}
<tr>
  <td>{{ts .StartTime}}</td>
  <td>{{.ServiceName}}</td>
  <td>{{.HTTPMethod}}</td>
  <td>{{if .HTTPRoute}}{{.HTTPRoute}}{{else}}{{.Name}}{{end}}</td>
  <td>{{if .StatusCode}}{{.StatusCode}}{{else}}—{{end}}</td>
  <td>{{dur .Duration}}</td>
  <td class="{{if or (eq .Outcome "server_error") (eq .Outcome "error")}}bad{{else if eq .Outcome "client_error"}}warn{{end}}">{{.Outcome}}</td>
  <td><a href="/traces/{{.TraceID}}">trace</a></td>
</tr>
{{else}}
<tr><td colspan="8" class="dim">No operations in this window.</td></tr>
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

{{.Waterfall.HTML}}

<table>
<thead><tr><th>Span</th><th>Service</th><th>Kind</th><th>Detail</th><th>Offset</th><th>Duration</th><th>Outcome</th></tr></thead>
<tbody>
{{range .Rows}}
<tr>
  <td style="{{indent .Depth}}">{{.Span.Name}}</td>
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
