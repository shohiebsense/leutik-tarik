// Package server wires ingest, store, and UI into one HTTP handler.
package server

import (
	"net/http"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/ingest"
	"github.com/shohiebsense/leutik-tarik/internal/logsnap"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/store"
	"github.com/shohiebsense/leutik-tarik/internal/ui"
)

// Options carries the wiring for one server instance.
type Options struct {
	Store    store.Store
	Policies *policy.Engine
	// Logs enables bounded log capture. Nil means logs are not accepted.
	Logs    *logsnap.Ring
	Trigger logsnap.Trigger
	// LogRetention is how long a captured log row lives.
	LogRetention time.Duration
}

// New returns the HTTP handler wiring ingest, store, policy and UI.
func New(s store.Store, policies *policy.Engine) http.Handler {
	return NewWithOptions(Options{Store: s, Policies: policies})
}

// NewWithOptions is New with log capture configured.
func NewWithOptions(o Options) http.Handler {
	s := o.Store
	policies := o.Policies
	if policies == nil {
		policies = policy.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/traces", ingest.Handler{
		Store: s, Policies: policies,
		Logs: o.Logs, Trigger: o.Trigger, LogRetention: o.LogRetention,
	})
	if o.Logs != nil {
		mux.Handle("/v1/logs", ingest.LogsHandler{
			Ring: o.Logs, Retention: o.LogRetention,
		})
	}
	u := ui.UI{Store: s, Policies: policies}
	mux.HandleFunc("/policies", u.PoliciesPage)
	mux.HandleFunc("/services", u.Services)
	mux.HandleFunc("/incidents", u.Incidents)
	mux.HandleFunc("/profiles", u.Profiles)
	mux.HandleFunc("/profiles/{profileID}", u.ProfileDetail)
	mux.Handle("/v1/profiles", ingest.ProfilesHandler{Store: s, MaxBodyBytes: 64 << 20})
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
