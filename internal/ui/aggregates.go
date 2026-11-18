package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// Services renders the RED view: rate, errors and duration per service.
//
// This is the page that answers "which service is broken" without opening a
// single trace.
func (u UI) Services(w http.ResponseWriter, r *http.Request) {
	stats, ok := u.Store.(store.Stats)
	if !ok {
		http.Error(w, "this store cannot aggregate", http.StatusNotImplemented)
		return
	}
	from, to, win := u.window(r, time.Hour)
	rows, err := stats.ServiceStats(r.Context(), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	endpoints, err := stats.EndpointStats(r.Context(), from, to, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var grandTotal time.Duration
	for _, e := range endpoints {
		grandTotal += e.Total
	}

	cfg := u.Verdicts()
	flagged := cfg.Flag(endpoints)

	// Worst error rate first, so the broken thing is at the top.
	var totalSpans, totalErrors int64
	for _, s := range rows {
		totalSpans += s.Spans
		totalErrors += s.Errors
	}
	render(w, r, servicesView, merge(map[string]any{
		"Title":       "Services",
		"Rows":        rows,
		"TotalSpans":  totalSpans,
		"TotalErrors": totalErrors,
		"Endpoints":   endpoints,
		"GrandTotal":  grandTotal,
		"Share":       shareOf,
		"Flagged":     flagged,
		"Config":      cfg,
		"Judgements":  judgementIndex(cfg, endpoints),
	}, win))
}

// ServiceDetail is one service's page: its numbers, its slowest endpoints and
// its failed responses. The operations list no longer carries a Service column,
// so this page is where service context lives.
func (u UI) ServiceDetail(w http.ResponseWriter, r *http.Request) {
	stats, ok := u.Store.(store.Stats)
	if !ok {
		http.Error(w, "this store cannot aggregate", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("service")
	from, to, win := u.window(r, time.Hour)

	all, err := stats.ServiceStats(r.Context(), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var stat store.ServiceStat
	var found bool
	for _, s := range all {
		if s.Service == name {
			stat, found = s, true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	endpoints, err := stats.EndpointStats(r.Context(), from, to, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var mine []store.EndpointStat
	for _, e := range endpoints {
		if e.Service == name {
			mine = append(mine, e)
		}
	}

	failures, err := stats.HTTPFailures(r.Context(), name, from, to, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var bad4, bad5 []store.HTTPFailure
	for _, f := range failures {
		if f.Class == "5xx" {
			bad5 = append(bad5, f)
		} else {
			bad4 = append(bad4, f)
		}
	}

	render(w, r, serviceDetailView, merge(map[string]any{
		"Title":      name,
		"Service":    name,
		"Stat":       stat,
		"Endpoints":  mine,
		"Bad4":       bad4,
		"Bad5":       bad5,
		"Judgements": judgementIndex(u.Verdicts(), mine),
	}, win))
}

// Incidents groups failures by fingerprint so frequency is visible.
func (u UI) Incidents(w http.ResponseWriter, r *http.Request) {
	stats, ok := u.Store.(store.Stats)
	if !ok {
		http.Error(w, "this store cannot aggregate", http.StatusNotImplemented)
		return
	}
	from, to, win := u.window(r, time.Hour)
	groups, err := stats.ErrorGroups(r.Context(), from, to, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var total int64
	max := int64(0)
	for _, g := range groups {
		total += g.Count
		if g.Count > max {
			max = g.Count
		}
	}
	render(w, r, incidentsView, merge(map[string]any{
		"Title":   "Incidents",
		"Groups":  groups,
		"Total":   total,
		"Max":     max,
		"Classes": severityClass,
	}, win))
}

// Verdicts returns the configured thresholds, falling back to the defaults.
func (u UI) Verdicts() VerdictConfig {
	if u.VerdictCfg == nil {
		return VerdictConfigFromEnv()
	}
	return *u.VerdictCfg
}

// judgementIndex lets the endpoint table show the verdict per row.
func judgementIndex(cfg VerdictConfig, endpoints []store.EndpointStat) map[string]Verdict {
	out := make(map[string]Verdict, len(endpoints))
	for _, e := range endpoints {
		out[e.Service+"|"+e.Method+"|"+e.Route] = cfg.Judge(e)
	}
	return out
}

// shareOf renders one endpoint's slice of the total latency budget.
func shareOf(e store.EndpointStat, grand time.Duration) float64 {
	if grand <= 0 {
		return 0
	}
	return float64(e.Total) / float64(grand)
}

// severityClass maps an outcome to the CSS class used for emphasis.
func severityClass(outcome string) string {
	switch outcome {
	case "error", "server_error":
		return "bad"
	case "client_error":
		return "warn"
	default:
		return "dim"
	}
}

// window reads the range for an aggregation view: a rolling `last` window
// (default one hour, because a day of raw spans is rarely what a RED view
// wants) or absolute from/to for a pinned investigation. It returns the display
// data too, so a page cannot show a range in one zone and query in another -
// that is exactly how /services ended up printing UTC while every other page
// printed Jakarta.
func (u UI) window(r *http.Request, def time.Duration) (from, to time.Time, meta map[string]any) {
	to = u.clock()
	span, ok := lastWindow(r.URL.Query().Get("last"))
	if !ok {
		span = def
	}
	from = to.Add(-span)
	live := true
	if t, ok := parseTime(r.URL.Query().Get("from")); ok {
		from, live = t, false
	}
	if t, ok := parseTime(r.URL.Query().Get("to")); ok {
		to, live = t, false
	}
	last := ""
	if live {
		last = durationParam(span)
	}
	base := r.URL.Path
	query := r.URL.Query()
	return from, to, map[string]any{
		"From":        inDisplay(from).Format(inputTimeLayout),
		"To":          inDisplay(to).Format(inputTimeLayout),
		"Window":      to.Sub(from),
		"WindowLabel": policy.HumanDuration(to.Sub(from)),
		"Live":        live,
		"Last":        last,
		"Presets":     windowPresets(base, query),
		"LiveURL":     liveURL(base, query, last),
		"PinnedURL":   windowURL(base, query, inDisplay(from).Format(inputTimeLayout), inDisplay(to).Format(inputTimeLayout)),
	}
}

// merge flattens a window's display data into a page's template data.
func merge(data, meta map[string]any) map[string]any {
	for k, v := range meta {
		data[k] = v
	}
	return data
}

// barWidth renders a proportional inline bar without client-side code.
func barWidth(count, max int64) template.CSS {
	if max <= 0 {
		return template.CSS("width:0%")
	}
	frac := float64(count) / float64(max)
	if frac > 1 {
		frac = 1
	}
	return template.CSS(fmt.Sprintf("width:%.0f%%", frac*100))
}

var _ = template.CSS("")
