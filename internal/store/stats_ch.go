package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ServiceStats aggregates the RED view per service.
//
// The window is applied in the subquery so duplicates are collapsed (LIMIT 1 BY)
// before counting; otherwise a redelivered span would inflate the numbers.
func (c *ClickHouse) ServiceStats(ctx context.Context, from, to time.Time) ([]ServiceStat, error) {
	sql := `SELECT service_name,
			count() AS spans,
			countIf(outcome IN ('error', 'server_error')) AS errors,
			quantile(0.5)(duration_ms) AS p50_ms,
			quantile(0.95)(duration_ms) AS p95_ms,
			max(start_time) AS last_seen,
			uniqExact(http_route) AS routes
		FROM (SELECT * FROM spans WHERE start_time >= ? AND start_time <= ? LIMIT 1 BY trace_id, span_id)
		WHERE parent_span_id = ''
		GROUP BY service_name
		ORDER BY errors DESC, spans DESC`
	rows, err := c.queryAggregate(ctx, sql, []any{chTime(from), chTime(to)})
	if err != nil {
		return nil, err
	}
	var out []ServiceStat
	for _, raw := range rows {
		var r struct {
			Service  string  `json:"service_name"`
			Spans    int64   `json:"spans"`
			Errors   int64   `json:"errors"`
			P50      float64 `json:"p50_ms"`
			P95      float64 `json:"p95_ms"`
			LastSeen string  `json:"last_seen"`
			Routes   int64   `json:"routes"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, ServiceStat{
			Service:  r.Service,
			Spans:    r.Spans,
			Errors:   r.Errors,
			P50:      time.Duration(r.P50 * float64(time.Millisecond)),
			P95:      time.Duration(r.P95 * float64(time.Millisecond)),
			LastSeen: parseCHTime(r.LastSeen),
			Routes:   r.Routes,
		})
	}
	return out, nil
}

// ErrorGroups groups failures into fingerprints (service, route, outcome, error
// type, message) so a repeated problem is one row with a count.
func (c *ClickHouse) ErrorGroups(ctx context.Context, from, to time.Time, limit int) ([]ErrorGroup, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sql := fmt.Sprintf(`SELECT service_name, http_route, outcome, error_type, status_message,
			count() AS n, uniqExact(trace_id) AS traces,
			min(start_time) AS first_seen, max(start_time) AS last_seen
		FROM (SELECT * FROM spans WHERE start_time >= ? AND start_time <= ?
		      AND outcome IN ('error', 'server_error', 'client_error')
		      LIMIT 1 BY trace_id, span_id)
		GROUP BY service_name, http_route, outcome, error_type, status_message
		ORDER BY n DESC
		LIMIT %d`, limit)

	rows, err := c.queryAggregate(ctx, sql, []any{chTime(from), chTime(to)})
	if err != nil {
		return nil, err
	}
	var out []ErrorGroup
	for _, raw := range rows {
		var r struct {
			Service   string `json:"service_name"`
			Route     string `json:"http_route"`
			Outcome   string `json:"outcome"`
			ErrorType string `json:"error_type"`
			Message   string `json:"status_message"`
			N         int64  `json:"n"`
			Traces    int64  `json:"traces"`
			FirstSeen string `json:"first_seen"`
			LastSeen  string `json:"last_seen"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, ErrorGroup{
			Service: r.Service, Route: r.Route, Outcome: r.Outcome,
			ErrorType: r.ErrorType, StatusMessage: r.Message,
			Count: r.N, Traces: r.Traces,
			FirstSeen: parseCHTime(r.FirstSeen), LastSeen: parseCHTime(r.LastSeen),
		})
	}
	return out, nil
}

// queryAggregate runs a parameterised SELECT and returns raw JSONEachRow lines.
func (c *ClickHouse) queryAggregate(ctx context.Context, sql string, args []any) ([][]byte, error) {
	body, err := c.rawQueryParams(ctx, sql, args)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// EndpointStats ranks endpoints by total time in the window.
func (c *ClickHouse) EndpointStats(ctx context.Context, from, to time.Time, limit int) ([]EndpointStat, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sql := fmt.Sprintf(`SELECT service_name, http_route, http_method,
			count() AS n,
			countIf(outcome IN ('error', 'server_error')) AS errors,
			quantile(0.5)(duration_ms) AS p50_ms,
			quantile(0.95)(duration_ms) AS p95_ms,
			avg(duration_ms) AS avg_ms,
			sum(duration_ms) AS total_ms
		FROM (SELECT * FROM spans WHERE start_time >= ? AND start_time <= ? LIMIT 1 BY trace_id, span_id)
		WHERE parent_span_id = '' AND http_route != ''
		GROUP BY service_name, http_route, http_method
		ORDER BY total_ms DESC
		LIMIT %d`, limit)

	rows, err := c.queryAggregate(ctx, sql, []any{chTime(from), chTime(to)})
	if err != nil {
		return nil, err
	}
	ms := func(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
	var out []EndpointStat
	for _, raw := range rows {
		var r struct {
			Service string  `json:"service_name"`
			Route   string  `json:"http_route"`
			Method  string  `json:"http_method"`
			N       int64   `json:"n"`
			Errors  int64   `json:"errors"`
			P50     float64 `json:"p50_ms"`
			P95     float64 `json:"p95_ms"`
			Avg     float64 `json:"avg_ms"`
			Total   float64 `json:"total_ms"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, EndpointStat{
			Service: r.Service, Route: r.Route, Method: r.Method,
			Count: r.N, Errors: r.Errors,
			P50: ms(r.P50), P95: ms(r.P95), Avg: ms(r.Avg), Total: ms(r.Total),
		})
	}
	return out, nil
}

// HTTPFailures lists failed responses for one service (empty service = all),
// split into 4xx and 5xx by the caller via Class.
func (c *ClickHouse) HTTPFailures(ctx context.Context, service string, from, to time.Time, limit int) ([]HTTPFailure, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Placeholders are bound in order of appearance across the whole statement,
	// so the subquery's two come first and the service filter's third, if any.
	sql := fmt.Sprintf(`SELECT service_name, http_route, http_method, status_code,
			count() AS n, max(start_time) AS last_seen, any(trace_id) AS example
		FROM (SELECT * FROM spans WHERE start_time >= ? AND start_time <= ?
		      LIMIT 1 BY trace_id, span_id)
		WHERE status_code >= 400 AND http_route != '' %s
		GROUP BY service_name, http_route, http_method, status_code
		ORDER BY n DESC
		LIMIT %d`,
		map[bool]string{true: "AND service_name = ?", false: ""}[service != ""], limit)

	args := []any{chTime(from), chTime(to)}
	if service != "" {
		args = append(args, service)
	}
	rows, err := c.queryAggregate(ctx, sql, args)
	if err != nil {
		return nil, err
	}
	var out []HTTPFailure
	for _, raw := range rows {
		var r struct {
			Service  string `json:"service_name"`
			Route    string `json:"http_route"`
			Method   string `json:"http_method"`
			Status   int    `json:"status_code"`
			N        int64  `json:"n"`
			LastSeen string `json:"last_seen"`
			Example  string `json:"example"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, HTTPFailure{
			Service: r.Service, Route: r.Route, Method: r.Method,
			StatusCode: r.Status, Class: statusClass(r.Status),
			Count: r.N, LastSeen: parseCHTime(r.LastSeen), ExampleTrace: r.Example,
		})
	}
	return out, nil
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	default:
		return ""
	}
}
