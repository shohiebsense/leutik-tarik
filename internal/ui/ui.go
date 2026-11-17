// Package ui renders server-side HTML views. Zero browser JavaScript.
package ui

import (
	"html/template"
	"net/http"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// UI serves the operations list and trace detail pages.
type UI struct {
	Store store.Store

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
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
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
		"From":     from.Format(time.RFC3339),
		"To":       to.Format(time.RFC3339),
	})
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
	render(w, traceView, map[string]any{
		"Title":   "Trace " + traceID,
		"TraceID": traceID,
		"Rows":    rows,
		"Summary": summary,
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
	operationsView = template.Must(template.Must(pageBase.Clone()).Parse(operationsTemplate))
	traceView      = template.Must(template.Must(pageBase.Clone()).Parse(traceTemplate))
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
</style>
</head>
<body>
<nav><a href="/operations">Operations</a></nav>
<h1>{{.Title}}</h1>
{{template "content" .}}
</body>
</html>{{end}}`

const operationsTemplate = `{{define "content"}}
<form method="get" action="/operations">
  <input type="text" name="service" placeholder="service" value="{{.Query.Service}}">
  <input type="text" name="route" placeholder="route" value="{{.Query.Route}}">
  <input type="text" name="method" placeholder="method" value="{{.Query.Method}}">
  <select name="outcome">
    <option value="">all outcomes</option>
    {{range .Outcomes}}<option value="{{.}}" {{if eq . $.Query.Outcome}}selected{{end}}>{{.}}</option>{{end}}
  </select>
  <label>from <input type="text" name="from" size="22" value="{{.From}}"></label>
  <label>to <input type="text" name="to" size="22" value="{{.To}}"></label>
  <button type="submit">Search</button>
</form>
<table>
<thead><tr><th>Time</th><th>Service</th><th>Method</th><th>Route</th><th>Status</th><th>Duration</th><th>Outcome</th><th></th></tr></thead>
<tbody>
{{range .Spans}}
<tr>
  <td>{{ts .StartTime}}</td>
  <td>{{.ServiceName}}</td>
  <td>{{.HTTPMethod}}</td>
  <td>{{.HTTPRoute}}</td>
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
