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
}

var outcomes = []string{
	model.OutcomeSuccess,
	model.OutcomeClientError,
	model.OutcomeServerError,
	model.OutcomeError,
	model.OutcomeUnknown,
}

// Operations renders the searchable operations list.
func (u UI) Operations(w http.ResponseWriter, r *http.Request) {
	q := store.Query{
		Service: r.URL.Query().Get("service"),
		Route:   r.URL.Query().Get("route"),
		Method:  r.URL.Query().Get("method"),
		Outcome: r.URL.Query().Get("outcome"),
		Limit:   100,
	}
	spans, err := u.Store.ListOperations(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, operationsTemplate, map[string]any{
		"Title":    "Operations",
		"Spans":    spans,
		"Query":    q,
		"Outcomes": outcomes,
	})
}

type traceRow struct {
	Span   model.Span
	Offset time.Duration
}

// Trace renders one trace as a waterfall table.
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
	start := spans[0].StartTime
	for _, s := range spans {
		if s.StartTime.Before(start) {
			start = s.StartTime
		}
	}
	rows := make([]traceRow, len(spans))
	for i, s := range spans {
		rows[i] = traceRow{Span: s, Offset: s.StartTime.Sub(start)}
	}
	render(w, traceTemplate, map[string]any{
		"Title":   "Trace " + traceID,
		"TraceID": traceID,
		"Rows":    rows,
	})
}

var funcs = template.FuncMap{
	"dur": func(d time.Duration) string {
		switch {
		case d < time.Microsecond:
			return d.String()
		case d < time.Millisecond:
			return d.Round(time.Microsecond).String()
		case d < time.Second:
			return d.Round(time.Millisecond).String()
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
}

func render(w http.ResponseWriter, content string, data map[string]any) {
	t := template.New("base").Funcs(funcs)
	if _, err := t.Parse(pageShell); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := t.Parse(content); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "page", data); err != nil {
		// Header already written; nothing useful to send.
		_ = err
	}
}

const pageShell = `{{define "page"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — leutik-tarik</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:1.5rem;color:#111}
table{border-collapse:collapse;width:100%;font-size:.9rem}
th,td{border-bottom:1px solid #ddd;padding:.4rem .6rem;text-align:left}
th{background:#f5f5f5}
a{color:#06c}
nav{margin-bottom:1rem}nav a{margin-right:1rem}
code{background:#f5f5f5;padding:.1rem .3rem}
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
  <td>{{.Outcome}}</td>
  <td><a href="/traces/{{.TraceID}}">trace</a></td>
</tr>
{{end}}
</tbody>
</table>
{{end}}`

const traceTemplate = `{{define "content"}}
<p>Trace ID: <code>{{.TraceID}}</code></p>
<table>
<thead><tr><th>Name</th><th>Service</th><th>Kind</th><th>Offset</th><th>Duration</th><th>Status</th></tr></thead>
<tbody>
{{range .Rows}}
<tr>
  <td>{{.Span.Name}}</td>
  <td>{{.Span.ServiceName}}</td>
  <td>{{.Span.Kind}}</td>
  <td>{{dur .Offset}}</td>
  <td>{{dur .Span.Duration}}</td>
  <td>{{if .Span.StatusCode}}{{.Span.StatusCode}}{{else}}{{.Span.Outcome}}{{end}}</td>
</tr>
{{end}}
</tbody>
</table>
{{end}}`
