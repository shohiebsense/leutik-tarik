// Package server wires ingest, store, and UI into one HTTP handler.
package server

import (
	"net/http"

	"github.com/shohiebsense/leutik-tarik/internal/ingest"
	"github.com/shohiebsense/leutik-tarik/internal/store"
	"github.com/shohiebsense/leutik-tarik/internal/ui"
)

// New returns the HTTP handler wiring ingest, store, and UI.
func New(s store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/traces", ingest.Handler{Store: s})
	u := ui.UI{Store: s}
	mux.HandleFunc("/operations", u.Operations)
	mux.HandleFunc("/traces/{traceID}", u.Trace)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/operations", http.StatusFound)
	})
	return mux
}
