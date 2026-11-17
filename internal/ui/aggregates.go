package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

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
	from, to := u.window(r)
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
	render(w, servicesView, map[string]any{
		"Title":       "Services",
		"Rows":        rows,
		"From":        from.Format(inputTimeLayout),
		"To":          to.Format(inputTimeLayout),
		"Window":      to.Sub(from),
		"TotalSpans":  totalSpans,
		"TotalErrors": totalErrors,
		"Endpoints":   endpoints,
		"GrandTotal":  grandTotal,
		"Share":       shareOf,
		"Flagged":     flagged,
		"Config":      cfg,
		"Judgements":  judgementIndex(cfg, endpoints),
	})
}

// Incidents groups failures by fingerprint so frequency is visible.
func (u UI) Incidents(w http.ResponseWriter, r *http.Request) {
	stats, ok := u.Store.(store.Stats)
	if !ok {
		http.Error(w, "this store cannot aggregate", http.StatusNotImplemented)
		return
	}
	from, to := u.window(r)
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
	render(w, incidentsView, map[string]any{
		"Title":   "Incidents",
		"Groups":  groups,
		"From":    from.Format(inputTimeLayout),
		"To":      to.Format(inputTimeLayout),
		"Window":  to.Sub(from),
		"Total":   total,
		"Max":     max,
		"Classes": severityClass,
	})
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

// window reads the from/to query parameters, defaulting to the last hour for
// aggregation views (a day of raw spans is rarely what a RED view wants).
func (u UI) window(r *http.Request) (from, to time.Time) {
	to = u.clock()
	from = to.Add(-time.Hour)
	if t, ok := parseTime(r.URL.Query().Get("from")); ok {
		from = t
	}
	if t, ok := parseTime(r.URL.Query().Get("to")); ok {
		to = t
	}
	return from, to
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
