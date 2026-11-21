// Machine query API: the same data the HTML pages render, as JSON, guarded by
// the ingest bearer token. Read-only: it can answer "what ran, how long, did
// it fail" for e2e runners and scripts that cannot sign in as a viewer.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// spanJSON is model.Span flattened for the wire: the fields a script needs,
// RFC3339 times, no internals.
type spanJSON struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Service      string `json:"service"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Method       string `json:"method"`
	Route        string `json:"route"`
	Status       int    `json:"status"`
	Start        string `json:"start"`
	DurationMs   int64  `json:"durationMs"`
}

func toSpanJSON(s model.Span) spanJSON {
	return spanJSON{
		TraceID: s.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID,
		Service: s.ServiceName, Name: s.Name, Kind: s.Kind,
		Method: s.HTTPMethod, Route: s.HTTPRoute, Status: s.StatusCode,
		Start: s.StartTime.Format(time.RFC3339Nano),
		DurationMs: s.Duration.Milliseconds(),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

// parseQueryWindow reads last= (rolling duration) or from=/to= (RFC3339),
// defaulting to a 15m rolling window, same spirit as the operations page.
func parseQueryWindow(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	from, to := now.Add(-15*time.Minute), now
	if s := r.URL.Query().Get("last"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return from, to, err
		}
		from = now.Add(-d)
		return from, to, nil
	}
	if s := r.URL.Query().Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return from, to, err
		}
		from = t
	}
	if s := r.URL.Query().Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return from, to, err
		}
		to = t
	}
	return from, to, nil
}

// QueryOperationsHandler serves GET /v1/operations as JSON. Same filters as the
// operations page: service, route, method, outcome, text, all=1, limit.
type QueryOperationsHandler struct {
	Store store.Store
	Now   func() time.Time
}

func (h QueryOperationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := h.Now
	if now == nil {
		now = time.Now
	}
	from, to, err := parseQueryWindow(r, now())
	if err != nil {
		badRequest(w, "bad time window: "+err.Error())
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > store.MaxLimit {
			badRequest(w, "limit must be 1.."+strconv.Itoa(store.MaxLimit))
			return
		}
		limit = n
	}
	q := store.Query{
		Service: r.URL.Query().Get("service"),
		Route:   r.URL.Query().Get("route"),
		Method:  r.URL.Query().Get("method"),
		Outcome: r.URL.Query().Get("outcome"),
		Text:    r.URL.Query().Get("text"),
		IncludeBackground: r.URL.Query().Get("all") == "1",
		From:    from,
		To:      to,
		Limit:   limit,
	}
	spans, err := h.Store.ListOperations(r.Context(), q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]spanJSON, len(spans))
	for i, s := range spans {
		out[i] = toSpanJSON(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339),
		"count": len(out), "operations": out,
	})
}

// QueryTraceHandler serves GET /v1/traces/{traceID} as JSON: every span of one
// request, the raw material of the waterfall page.
type QueryTraceHandler struct {
	Store store.Store
}

func (h QueryTraceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("traceID")
	if id == "" {
		badRequest(w, "missing traceID")
		return
	}
	spans, err := h.Store.GetTrace(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(spans) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	out := make([]spanJSON, len(spans))
	for i, s := range spans {
		out[i] = toSpanJSON(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"traceId": id, "count": len(out), "spans": out})
}
