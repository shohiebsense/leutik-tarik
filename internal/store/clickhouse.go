package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// ClickHouse talks to ClickHouse over its HTTP interface. The native driver is
// deliberately not used: inserts and reads here are plain SQL, and one fewer
// dependency is one fewer thing to keep compatible (build prompt §4).
type ClickHouse struct {
	base   string
	client *http.Client
}

// NewClickHouse returns a store writing to the ClickHouse HTTP endpoint, e.g.
// http://127.0.0.1:8123.
func NewClickHouse(baseURL string) (*ClickHouse, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("clickhouse url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("clickhouse url needs scheme and host, got %q", baseURL)
	}
	return &ClickHouse{
		base: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			// Inserts on a large batch can be slow; reads are bounded by the
			// query itself. One timeout for both, generous but finite.
			Timeout: 60 * time.Second,
		},
	}, nil
}

// Ping verifies the endpoint answers. It deliberately does not go through
// query(): the reply to `SELECT 1` is not a row set we want to decode.
func (c *ClickHouse) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/ping", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: http %d", resp.StatusCode)
	}
	return nil
}

func (c *ClickHouse) Insert(ctx context.Context, records []model.Record) error {
	if len(records) == 0 {
		return nil
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, r := range records {
		if err := enc.Encode(toRow(r)); err != nil {
			return fmt.Errorf("encode row: %w", err)
		}
	}

	// Retry idempotency: the token is derived from the payload, so a repeated
	// delivery of the same batch is dropped by ClickHouse instead of doubling
	// the rows. Requires non_replicated_deduplication_window (migration 002).
	sum := sha256.Sum256(body.Bytes())
	token := hex.EncodeToString(sum[:16])

	insertURL := fmt.Sprintf("%s/?query=%s&insert_deduplication_token=%s",
		c.base,
		url.QueryEscape("INSERT INTO spans FORMAT JSONEachRow"),
		url.QueryEscape(token))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, insertURL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("insert: http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (c *ClickHouse) ListOperations(ctx context.Context, q Query) ([]model.Span, error) {
	var where []string
	var args []any
	where = append(where, "parent_span_id = ''")
	if q.Service != "" {
		where = append(where, "service_name = ?")
		args = append(args, q.Service)
	}
	if q.Route != "" {
		where = append(where, "http_route = ?")
		args = append(args, q.Route)
	}
	if q.Method != "" {
		where = append(where, "http_method = ?")
		args = append(args, q.Method)
	}
	if q.Outcome != "" {
		where = append(where, "outcome = ?")
		args = append(args, q.Outcome)
	}
	if !q.From.IsZero() {
		where = append(where, "start_time >= ?")
		args = append(args, chTime(q.From))
	}
	if !q.To.IsZero() {
		where = append(where, "start_time <= ?")
		args = append(args, chTime(q.To))
	}
	if !q.Cursor.IsZero() {
		where = append(where, "start_time < ?")
		args = append(args, chTime(q.Cursor))
	}

	// ReplacingMergeTree can hold duplicate rows until a merge, so collapse
	// them here. LIMIT 1 BY is cheaper than FINAL (measured 1 vs seconds at
	// scale) and needs no extra pass.
	sql := fmt.Sprintf(`SELECT %s FROM spans WHERE %s ORDER BY start_time DESC LIMIT 1 BY trace_id, span_id LIMIT %d`,
		rowColumns, strings.Join(where, " AND "), q.limit())

	return c.query(ctx, sql, args)
}

func (c *ClickHouse) GetTrace(ctx context.Context, traceID string) ([]model.Span, error) {
	// Narrow read, so FINAL is affordable here and gives exact dedup.
	sql := fmt.Sprintf(`SELECT %s FROM spans FINAL WHERE trace_id = ? ORDER BY start_time LIMIT %d`,
		rowColumns, MaxLimit)
	return c.query(ctx, sql, []any{traceID})
}

// rowColumns is the projection both reads use; kept in one place so a schema
// change cannot half-apply.
const rowColumns = `trace_id, span_id, parent_span_id,
	service_name, service_version, environment,
	name, kind, operation_type,
	http_method, http_route, status_code,
	rpc_system, rpc_service, rpc_method, grpc_status_code,
	db_system, db_operation, db_query_summary,
	messaging_system, messaging_destination, messaging_operation,
	host_name, pod_name, namespace, container_name, peer_service,
	start_time, end_time, duration_ms,
	error_type, outcome, status_message,
	policy_id, policy_version, sampled, source, completeness, redactions, ingested_at,
	retention_seconds, attributes`

// query runs a SELECT with positional parameters and decodes JSONEachRow. The
// FORMAT clause is added here so no caller can forget it and get TSV back.
func (c *ClickHouse) query(ctx context.Context, sql string, args []any) ([]model.Span, error) {
	if !strings.Contains(sql, "FORMAT ") {
		sql += " FORMAT JSONEachRow"
	}
	params := url.Values{}
	params.Set("query", sql)
	for i, a := range args {
		params.Set(fmt.Sprintf("param_%s", paramName(i+1)), fmt.Sprint(a))
	}
	// Placeholders in the SQL are named (param_x), which ClickHouse requires for
	// HTTP parameter binding.
	sql = bindNamed(sql, len(args))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/?"+params.Encode(),
		strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	params.Set("query", sql)
	req.URL.RawQuery = params.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("query: http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return decodeRows(resp.Body)
}

func paramName(i int) string { return "p" + strconv.Itoa(i) }

// bindNamed rewrites `?` placeholders into ClickHouse named parameters, in
// order of appearance.
func bindNamed(sql string, n int) string {
	if n == 0 {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + n*8)
	i := 0
	for _, r := range sql {
		if r == '?' && i < n {
			i++
			b.WriteString("{")
			b.WriteString(paramName(i))
			b.WriteString(":String}")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func chTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000000000")
}

// jsonRow mirrors the SELECT list, with ClickHouse's JSONEachRow type mapping.
type jsonRow struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id"`

	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	Environment    string `json:"environment"`

	Name          string `json:"name"`
	Kind          string `json:"kind"`
	OperationType string `json:"operation_type"`

	HTTPMethod string `json:"http_method"`
	HTTPRoute  string `json:"http_route"`
	StatusCode int32  `json:"status_code"`

	RPCSystem      string `json:"rpc_system"`
	RPCService     string `json:"rpc_service"`
	RPCMethod      string `json:"rpc_method"`
	GRPCStatusCode int32  `json:"grpc_status_code"`

	DBSystem       string `json:"db_system"`
	DBOperation    string `json:"db_operation"`
	DBQuerySummary string `json:"db_query_summary"`

	MessagingSystem      string `json:"messaging_system"`
	MessagingDestination string `json:"messaging_destination"`
	MessagingOperation   string `json:"messaging_operation"`

	HostName      string `json:"host_name"`
	PodName       string `json:"pod_name"`
	Namespace     string `json:"namespace"`
	ContainerName string `json:"container_name"`
	PeerService   string `json:"peer_service"`

	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	DurationMS float64 `json:"duration_ms"`

	ErrorType     string `json:"error_type"`
	Outcome       string `json:"outcome"`
	StatusMessage string `json:"status_message"`

	PolicyID      string `json:"policy_id"`
	PolicyVersion uint32 `json:"policy_version"`
	Sampled       uint8  `json:"sampled"`
	Source        string `json:"source"`
	Completeness  string `json:"completeness"`
	Redactions    uint32 `json:"redactions"`
	IngestedAt    string `json:"ingested_at"`

	RetentionSeconds uint32            `json:"retention_seconds"`
	Attributes       map[string]string `json:"attributes"`
}

// decodeRows streams JSONEachRow into spans.
func decodeRows(body io.Reader) ([]model.Span, error) {
	dec := json.NewDecoder(body)
	var out []model.Span
	for {
		var row jsonRow
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, fmt.Errorf("decode row: %w", err)
		}
		out = append(out, row.span())
	}
}

func (r jsonRow) span() model.Span {
	return model.Span{
		TraceID:              r.TraceID,
		SpanID:               r.SpanID,
		ParentSpanID:         r.ParentSpanID,
		ServiceName:          r.ServiceName,
		ServiceVersion:       r.ServiceVersion,
		Environment:          r.Environment,
		Name:                 r.Name,
		Kind:                 r.Kind,
		OperationType:        r.OperationType,
		HTTPMethod:           r.HTTPMethod,
		HTTPRoute:            r.HTTPRoute,
		StatusCode:           int(r.StatusCode),
		RPCSystem:            r.RPCSystem,
		RPCService:           r.RPCService,
		RPCMethod:            r.RPCMethod,
		GRPCStatusCode:       int(r.GRPCStatusCode),
		DBSystem:             r.DBSystem,
		DBOperation:          r.DBOperation,
		DBQuerySummary:       r.DBQuerySummary,
		MessagingSystem:      r.MessagingSystem,
		MessagingDestination: r.MessagingDestination,
		MessagingOperation:   r.MessagingOperation,
		HostName:             r.HostName,
		PodName:              r.PodName,
		Namespace:            r.Namespace,
		ContainerName:        r.ContainerName,
		PeerService:          r.PeerService,
		StartTime:            parseCHTime(r.StartTime),
		EndTime:              parseCHTime(r.EndTime),
		Duration:             time.Duration(r.DurationMS * float64(time.Millisecond)),
		ErrorType:            r.ErrorType,
		Outcome:              r.Outcome,
		StatusMessage:        r.StatusMessage,
		Attributes:           r.Attributes,
	}
}

func parseCHTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// insertRow is the JSONEachRow shape written to ClickHouse.
type insertRow struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id"`

	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	Environment    string `json:"environment"`

	Name          string `json:"name"`
	Kind          string `json:"kind"`
	OperationType string `json:"operation_type"`

	HTTPMethod string `json:"http_method"`
	HTTPRoute  string `json:"http_route"`
	StatusCode int32  `json:"status_code"`

	RPCSystem      string `json:"rpc_system"`
	RPCService     string `json:"rpc_service"`
	RPCMethod      string `json:"rpc_method"`
	GRPCStatusCode int32  `json:"grpc_status_code"`

	DBSystem       string `json:"db_system"`
	DBOperation    string `json:"db_operation"`
	DBQuerySummary string `json:"db_query_summary"`

	MessagingSystem      string `json:"messaging_system"`
	MessagingDestination string `json:"messaging_destination"`
	MessagingOperation   string `json:"messaging_operation"`

	HostName      string `json:"host_name"`
	PodName       string `json:"pod_name"`
	Namespace     string `json:"namespace"`
	ContainerName string `json:"container_name"`
	PeerService   string `json:"peer_service"`

	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	DurationMS float64 `json:"duration_ms"`

	ErrorType     string `json:"error_type"`
	Outcome       string `json:"outcome"`
	StatusMessage string `json:"status_message"`

	PolicyID      string `json:"policy_id"`
	PolicyVersion uint32 `json:"policy_version"`
	Sampled       uint8  `json:"sampled"`
	Source        string `json:"source"`
	Completeness  string `json:"completeness"`
	Redactions    uint32 `json:"redactions"`
	IngestedAt    string `json:"ingested_at"`

	RetentionSeconds uint32            `json:"retention_seconds"`
	Attributes       map[string]string `json:"attributes"`
}

func toRow(r model.Record) insertRow {
	s := r.Span
	sampled := uint8(0)
	if r.Decision.Sampled {
		sampled = 1
	}
	retention := uint32(r.Decision.Retention / time.Second)
	if retention == 0 {
		retention = uint32(14 * 24 * time.Hour / time.Second)
	}
	attrs := s.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return insertRow{
		TraceID:              s.TraceID,
		SpanID:               s.SpanID,
		ParentSpanID:         s.ParentSpanID,
		ServiceName:          s.ServiceName,
		ServiceVersion:       s.ServiceVersion,
		Environment:          s.Environment,
		Name:                 s.Name,
		Kind:                 s.Kind,
		OperationType:        s.OperationType,
		HTTPMethod:           s.HTTPMethod,
		HTTPRoute:            s.HTTPRoute,
		StatusCode:           int32(s.StatusCode),
		RPCSystem:            s.RPCSystem,
		RPCService:           s.RPCService,
		RPCMethod:            s.RPCMethod,
		GRPCStatusCode:       int32(s.GRPCStatusCode),
		DBSystem:             s.DBSystem,
		DBOperation:          s.DBOperation,
		DBQuerySummary:       s.DBQuerySummary,
		MessagingSystem:      s.MessagingSystem,
		MessagingDestination: s.MessagingDestination,
		MessagingOperation:   s.MessagingOperation,
		HostName:             s.HostName,
		PodName:              s.PodName,
		Namespace:            s.Namespace,
		ContainerName:        s.ContainerName,
		PeerService:          s.PeerService,
		StartTime:            chTime(s.StartTime),
		EndTime:              chTime(s.EndTime),
		DurationMS:           float64(s.Duration) / float64(time.Millisecond),
		ErrorType:            s.ErrorType,
		Outcome:              s.Outcome,
		StatusMessage:        s.StatusMessage,
		PolicyID:             r.Decision.PolicyID,
		PolicyVersion:        r.Decision.PolicyVersion,
		Sampled:              sampled,
		Source:               r.Source,
		Completeness:         r.Completeness,
		Redactions:           uint32(r.Decision.Redactions),
		IngestedAt:           chTime(r.IngestedAt),
		RetentionSeconds:     retention,
		Attributes:           attrs,
	}
}
