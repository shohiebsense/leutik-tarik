// Package server wires ingest, store, and UI into one HTTP handler.
package server

import (
	"net/http"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/auth"
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
	// Auth guards the HTML routes with a session and the ingest routes with a
	// bearer token. Nil disables authentication (the pre-auth behaviour).
	Auth *auth.Auth
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

// NewWithOptions is New with log capture and authentication configured.
func NewWithOptions(o Options) http.Handler {
	s := o.Store
	policies := o.Policies
	if policies == nil {
		policies = policy.Default()
	}

	a := o.Auth
	requireUI := func(h http.HandlerFunc) http.HandlerFunc { return h }
	requireIngest := func(h http.Handler) http.Handler { return h }
	if a != nil {
		requireUI = a.RequireUI
		requireIngest = a.RequireIngest
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/traces", requireIngest(ingest.Handler{
		Store: s, Policies: policies,
		Logs: o.Logs, Trigger: o.Trigger, LogRetention: o.LogRetention,
	}))
	if o.Logs != nil {
		mux.Handle("/v1/logs", requireIngest(ingest.LogsHandler{
			Ring: o.Logs, Retention: o.LogRetention,
		}))
	}
	// The HTML pages read through a short TTL cache (their live views re-ask the
	// same whole-window query on a fixed cadence, and every tab paid the full
	// scan again); the machine API reads the store directly, so scripts and e2e
	// runners always see fresh data.
	uiStore := s
	if readable, ok := s.(interface {
		store.Store
		store.Stats
	}); ok {
		uiStore = store.NewCachedReads(readable)
	}
	u := ui.UI{Store: uiStore, Policies: policies}
	// Machine query API: read-only JSON views guarded by the ingest token.
	mux.Handle("GET /v1/operations", requireIngest(QueryOperationsHandler{Store: s}))
	mux.Handle("GET /v1/traces/{traceID}", requireIngest(QueryTraceHandler{Store: s}))

	mux.HandleFunc("/policies", requireUI(u.PoliciesPage))
	mux.HandleFunc("/services", requireUI(u.Services))
	mux.HandleFunc("/services/{service}", requireUI(u.ServiceDetail))
	mux.HandleFunc("/incidents", requireUI(u.Incidents))
	mux.HandleFunc("/profiles", requireUI(u.Profiles))
	mux.HandleFunc("/profiles/{profileID}", requireUI(u.ProfileDetail))
	mux.Handle("/v1/profiles", requireIngest(ingest.ProfilesHandler{Store: s, MaxBodyBytes: 64 << 20}))
	mux.HandleFunc("/operations", requireUI(u.Operations))
	mux.HandleFunc("/operations/{service}", requireUI(u.Operations))
	mux.HandleFunc("/traces/{traceID}", requireUI(u.Trace))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Sign-in and sign-out exist only when authentication is on. With no
	// accounts loaded, /login just goes home.
	if a != nil && a.Enabled() {
		mux.HandleFunc("GET /login", u.LoginPage)
		mux.HandleFunc("POST /login", a.LoginPOST)
		mux.HandleFunc("POST /logout", a.Logout)
	} else {
		mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/operations", http.StatusFound)
		})
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/operations", http.StatusFound)
	})
	return mux
}
