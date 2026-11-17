package server_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/logsnap"
	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/server"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

const traceIDLogs = "feedfacefeedfacefeedfacefeedface"

func logBody(nanos int64, sev, text, trace string) string {
	traceField := ""
	if trace != "" {
		traceField = fmt.Sprintf(`, "traceId": %q`, trace)
	}
	return fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payment"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"%d","severityText":%q,"body":{"stringValue":%q}%s}]}]}]}`,
		nanos, sev, text, traceField)
}

// A snapshot writes only when a trigger fires, and it must keep every distinct
// line even when several share a timestamp.
func TestLogSnapshotOnTrigger(t *testing.T) {
	s := store.NewMemory()
	ring := logsnap.NewRing(logsnap.Limits{})
	h := server.NewWithOptions(server.Options{
		Store: s, Logs: ring,
		Trigger:      logsnap.DefaultTrigger(),
		LogRetention: 7 * 24 * time.Hour,
	})

	now := time.Now().UTC()
	nano := func(offset time.Duration) int64 { return now.Add(offset).UnixNano() }

	// One line carrying the trace id, two without, two sharing a timestamp.
	for _, body := range []string{
		logBody(nano(0), "ERROR", "db connect failed: timeout", traceIDLogs),
		logBody(nano(0), "warn", "pool exhausted", ""),
		logBody(nano(time.Millisecond), "info", "retrying in 500ms", ""),
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("logs status = %d body=%s", rec.Code, rec.Body.String())
		}
	}

	// Nothing is stored before a trigger: the ring only holds it.
	if logs, _ := s.GetLogs(t.Context(), traceIDLogs); len(logs) != 0 {
		t.Fatalf("logs stored without a trigger: %d", len(logs))
	}

	// A 500 on the same service and window fires the snapshot.
	span := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payment"}}]},"scopeSpans":[{"spans":[{"traceId":%q,"spanId":"1111111111111111","name":"POST /v1/payments/{id}","kind":2,"startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[{"key":"http.request.method","value":{"stringValue":"POST"}},{"key":"http.route","value":{"stringValue":"/v1/payments/{id}"}},{"key":"http.response.status_code","value":{"intValue":"500"}}]}]}]}]}`,
		traceIDLogs, nano(0), nano(time.Millisecond))
	rec := httptest.NewRecorder()
	traceReq := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte(span)))
	traceReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, traceReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("traces status = %d body=%s", rec.Code, rec.Body.String())
	}

	logs, err := s.GetLogs(t.Context(), traceIDLogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("want 3 captured lines, got %d: %+v", len(logs), logs)
	}

	var exact, correlated int
	for _, l := range logs {
		if l.Exact {
			exact++
			if l.Body != "db connect failed: timeout" {
				t.Errorf("exact line = %q", l.Body)
			}
		} else {
			correlated++
			// Correlated lines are stored under the triggering trace so the
			// detail page can find them, while still reporting exact=false.
			if l.TraceID != traceIDLogs {
				t.Errorf("correlated line not attributed to the trace: %q", l.TraceID)
			}
		}
		if l.Retention != 7*24*time.Hour {
			t.Errorf("retention = %v", l.Retention)
		}
	}
	if exact != 1 || correlated != 2 {
		t.Errorf("exact=%d correlated=%d, want 1/2", exact, correlated)
	}
}

// An operation that does not trigger must never cause a snapshot.
func TestNoSnapshotWithoutTrigger(t *testing.T) {
	s := store.NewMemory()
	ring := logsnap.NewRing(logsnap.Limits{})
	h := server.NewWithOptions(server.Options{
		Store: s, Logs: ring, Trigger: logsnap.DefaultTrigger(), LogRetention: time.Hour,
	})

	now := time.Now().UTC()
	rec := httptest.NewRecorder()
	logReq := httptest.NewRequest(http.MethodPost, "/v1/logs",
		strings.NewReader(logBody(now.UnixNano(), "info", "all good", "")))
	logReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, logReq)

	// 200 OK, fast: nothing to investigate.
	okSpan := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payment"}}]},"scopeSpans":[{"spans":[{"traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanId":"2222222222222222","name":"GET /v1/ok","kind":2,"startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}},{"key":"http.response.status_code","value":{"intValue":"200"}}]}]}]}]}`,
		now.UnixNano(), now.Add(5*time.Millisecond).UnixNano())
	rec = httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(okSpan))
	okReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, okReq)

	logs, _ := s.GetLogs(t.Context(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if len(logs) != 0 {
		t.Errorf("a healthy request captured %d log lines", len(logs))
	}
	var _ model.LogEvent
}
