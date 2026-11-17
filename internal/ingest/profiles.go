package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// ProfilesHandler accepts a pprof profile and stores it as an artefact.
//
// A profile is not a stream: it is one bounded window, posted by a service (or
// by the agent on its behalf). Nothing here samples on its own — capture policy
// is a separate concern (build prompt §9.3).
type ProfilesHandler struct {
	Store        store.Store
	MaxBodyBytes int64
	Logf         func(format string, args ...any)
}

func (h ProfilesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ps, ok := h.Store.(store.Profiles)
	if !ok {
		http.Error(w, "this store cannot hold profiles", http.StatusNotImplemented)
		return
	}

	max := h.MaxBodyBytes
	if max <= 0 {
		max = 64 << 20
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
	if len(body) == 0 {
		http.Error(w, "empty profile body", http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	service := q.Get("service")
	if service == "" {
		service = "unknown"
	}
	profileType := q.Get("type")
	if profileType == "" {
		profileType = "cpu"
	}
	started := time.Now().UTC()
	if v := q.Get("started"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			started = t.UTC()
		}
	}

	meta := store.ProfileMeta{
		ID:        newProfileID(),
		Service:   service,
		Type:      profileType,
		StartedAt: started,
		Retention: 14 * 24 * time.Hour,
	}
	if err := ps.InsertProfile(r.Context(), meta, body); err != nil {
		http.Error(w, "store profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logf("profile stored id=%s service=%s type=%s bytes=%d", meta.ID, service, profileType, len(body))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(meta.ID + "\n"))
}

func (h ProfilesHandler) logf(format string, args ...any) {
	if h.Logf == nil {
		logfDefault(format, args...)
		return
	}
	h.Logf(format, args...)
}

// newProfileID returns a random 128-bit id used as the blob filename.
func newProfileID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
