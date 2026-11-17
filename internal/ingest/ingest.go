// Package ingest accepts OTLP/HTTP trace exports (JSON and protobuf, optionally
// gzip-compressed) and persists them as normalized model.Span records.
package ingest

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// DefaultMaxBodyBytes follows the OTLP specification's recommended 64 MiB cap.
const DefaultMaxBodyBytes int64 = 64 << 20

// Handler is the OTLP/HTTP trace receiver.
type Handler struct {
	Store store.Store

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
		http.Error(w, "invalid otlp payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	records, stats := normalize(raw, time.Now().UTC())
	if len(records) > 0 {
		if err := h.Store.Insert(r.Context(), records); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	h.logf("otlp ingest format=%s encoding=%s decompressed_bytes=%d spans=%d dropped_attrs=%d redacted=%d",
		kind, encodingOrDefault(encoding), len(body), len(records), stats.droppedAttrs, stats.redacted)

	writeSuccess(w, kind)
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
