// Package ingest accepts OTLP/HTTP trace exports (JSON and protobuf, optionally
// gzip-compressed) and persists them as normalized model.Span records.
package ingest

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/logsnap"
	"github.com/shohiebsense/leutik-tarik/internal/model"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// DefaultMaxBodyBytes follows the OTLP specification's recommended 64 MiB cap.
const DefaultMaxBodyBytes int64 = 64 << 20

// Handler is the OTLP/HTTP trace receiver.
type Handler struct {
	Store store.Store

	// Policies decides retention per span. Nil means the conservative default
	// (14 days, no rules), so an unconfigured install still behaves predictably.
	Policies *policy.Engine

	// Logs is the bounded buffer a snapshot is taken from. Nil disables log
	// capture entirely (the default, because it is opt-in).
	Logs *logsnap.Ring
	// Trigger decides which spans are worth a snapshot.
	Trigger logsnap.Trigger
	// LogRetention is how long a captured log row lives.
	LogRetention time.Duration

	// MaxBodyBytes caps the *decompressed* request body. Zero uses
	// DefaultMaxBodyBytes. Bounded on purpose: the build prompt lists
	// compression bombs and oversized attributes as attack surface.
	MaxBodyBytes int64

	// Logf defaults to log.Printf. Called once per request with the observed
	// size and encoding, so the limit can be tuned from real traffic instead
	// of guessed.
	Logf func(format string, args ...any)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	max := h.MaxBodyBytes
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)

	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	body, err := readBody(r, encoding, max)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}

	kind := wireKind(r.Header.Get("Content-Type"))
	raw, err := decode(kind, body)
	if err != nil {
		http.Error(w, "invalid otlp payload: "+err.Error()+contentTypeHint(kind, body), http.StatusBadRequest)
		return
	}

	records, stats := normalize(raw, time.Now().UTC(), h.Policies)
	if len(records) > 0 {
		if err := h.Store.Insert(r.Context(), records); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	h.snapshotLogs(r, records)

	h.logf("otlp ingest format=%s encoding=%s decompressed_bytes=%d spans=%d dropped_attrs=%d dropped_spans=%d redacted=%d",
		kind, encodingOrDefault(encoding), len(body), len(records), stats.droppedAttrs, stats.droppedSpans, stats.redacted)

	writeSuccess(w, kind)
}

// snapshotLogs turns a triggered span into a bounded log snapshot. This is the
// only path that writes log rows: no trigger, no storage (build prompt §8.2).
func (h Handler) snapshotLogs(r *http.Request, records []model.Record) {
	if h.Logs == nil || len(records) == 0 {
		return
	}
	trigger := h.Trigger
	if trigger.MaxEvents == 0 {
		trigger = logsnap.DefaultTrigger()
	}
	retention := h.LogRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}

	var captured []model.LogEvent
	for _, rec := range records {
		fires, reason := trigger.Fires(rec.Span)
		if !fires {
			continue
		}
		from, to := trigger.Window(rec.Span)
		events := h.Logs.Snapshot(rec.Span.ServiceName, rec.Span.TraceID, from, to, trigger.MaxEvents)
		for _, e := range events {
			e.Retention = retention
			e.Source = "ring:" + reason
			// A line with no trace id still belongs to this snapshot, so it is
			// stored under the triggering trace and keeps exact=false. Without
			// this the row would be unreachable by trace id and the UI would
			// silently drop it.
			if e.TraceID == "" {
				e.TraceID = rec.Span.TraceID
			}
			captured = append(captured, e)
		}
	}
	if len(captured) == 0 {
		return
	}
	if err := h.Store.InsertLogs(r.Context(), captured); err != nil {
		// Telemetry loss is reported, never silent, and never fatal to ingest.
		h.logf("log snapshot failed: %v", err)
		return
	}
	h.logf("log snapshot captured=%d", len(captured))
}

// contentTypeHint turns the most common (and most confusing) mistake into a
// sentence: OTLP's default encoding is protobuf, so JSON sent without a
// Content-Type header is parsed as protobuf and fails obscurely.
func contentTypeHint(kind wire, body []byte) string {
	if kind == wireProtobuf && len(bytes.TrimSpace(body)) > 0 && bytes.TrimSpace(body)[0] == '{' {
		return " (the body looks like JSON; set Content-Type: application/json, because OTLP defaults to protobuf)"
	}
	return ""
}

var errTooLarge = errors.New("request body exceeds limit")

// readBody reads at most max bytes, decompressing gzip when requested. The
// decompressed read is capped too, so a small gzip bomb cannot expand past max.
func readBody(r *http.Request, encoding string, max int64) ([]byte, error) {
	var src io.Reader = r.Body
	switch encoding {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer zr.Close()
		src = zr
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", encoding)
	}

	body, err := io.ReadAll(io.LimitReader(src, max+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errTooLarge
		}
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errTooLarge
	}
	return body, nil
}

type wire int

const (
	wireProtobuf wire = iota
	wireJSON
)

// wireKind maps Content-Type to a decoder. Per the OTLP spec the default
// encoding is protobuf, so an absent or unknown type means protobuf.
func wireKind(contentType string) wire {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct == "application/json" {
		return wireJSON
	}
	return wireProtobuf
}

func (k wire) String() string {
	if k == wireJSON {
		return "json"
	}
	return "protobuf"
}

func encodingOrDefault(e string) string {
	if e == "" {
		return "none"
	}
	return e
}

// logfDefault is the package-level fallback used by both handlers.
func logfDefault(format string, args ...any) { log.Printf(format, args...) }

func (h Handler) logf(format string, args ...any) {
	if h.Logf == nil {
		log.Printf(format, args...)
		return
	}
	h.Logf(format, args...)
}

// writeSuccess answers in the encoding of the request, as OTLP requires.
func writeSuccess(w http.ResponseWriter, kind wire) {
	if kind == wireJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(marshalEmptyResponse())
}
