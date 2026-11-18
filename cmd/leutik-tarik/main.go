// Command leutik-tarik runs the ingest + query/UI service.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // distroless has no tzdata; without this Asia/Jakarta silently becomes UTC

	"github.com/shohiebsense/leutik-tarik/internal/auth"
	"github.com/shohiebsense/leutik-tarik/internal/logsnap"
	"github.com/shohiebsense/leutik-tarik/internal/policy"
	"github.com/shohiebsense/leutik-tarik/internal/server"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func main() {
	// Loopback by default: this is an internal telemetry store with no auth yet,
	// so it must not be reachable from the LAN by accident (review P6).
	addr := flag.String("addr", envOr("LEUTIK_ADDR", "127.0.0.1:8080"), "listen address")
	migrate := flag.Bool("migrate", false, "apply pending schema migrations and exit")
	flag.Parse()

	if *migrate {
		runMigrations()
		return
	}

	st, label := pickStore()
	policies := pickPolicy()
	authOpt := pickAuth()

	opts := server.Options{Store: st, Policies: policies, Auth: authOpt}
	if os.Getenv("LEUTIK_LOG_CAPTURE") != "off" {
		opts.Logs = logsnap.NewRing(logsnap.Limits{})
		opts.Trigger = logsnap.DefaultTrigger()
		opts.LogRetention = 7 * 24 * time.Hour
		log.Printf("log capture on: bounded ring per service, snapshot on error or >%s", opts.Trigger.SlowThreshold)
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: server.NewWithOptions(opts),
		// Bounded, so a slow client cannot hold a connection open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("leutik-tarik listening on %s (%s)", *addr, label)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	// Graceful shutdown: in-flight requests get bounded time to finish rather
	// than being cut mid-insert (build prompt §11).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// pickPolicy loads the retention policy. A malformed policy is fatal: silently
// falling back to defaults would change how long evidence is kept without
// telling anyone.
// pickPolicy loads the retention policy. A *broken* policy is fatal: silently
// falling back would change how long evidence is kept without telling anyone. A
// *missing* policy is not an error — a fresh install has no config yet — so it
// falls back to the built-in defaults and says so loudly.
func pickPolicy() *policy.Engine {
	path := os.Getenv("LEUTIK_POLICY_PATH")
	explicit := path != ""
	if path == "" {
		path = "config/policy.yaml"
		if _, err := os.Stat(path); err != nil {
			path = "config/policy.example.yaml"
		}
	}

	p, err := policy.Load(path)
	if err != nil {
		if !explicit && os.IsNotExist(err) {
			def := policy.Default().Policy()
			log.Printf("no policy file found at %s; using built-in defaults (trace retention %s). Set LEUTIK_POLICY_PATH to change it.",
				path, def.Defaults.Trace.Duration())
			return policy.Default()
		}
		log.Fatalf("policy %s: %v", path, err)
	}
	log.Printf("policy %s loaded: version %d, %d rules, default trace retention %s",
		path, p.Version, len(p.Rules), p.Defaults.Trace.Duration())
	return policy.New(p)
}

// pickAuth loads accounts and builds the in-app auth gate. A missing users file
// is not fatal (a fresh install has no accounts yet): authentication then stays
// off, and the server runs as it always has. A present-but-broken file is
// fatal, like a broken policy.
func pickAuth() *auth.Auth {
	path := os.Getenv("LEUTIK_USERS_PATH")
	if path == "" {
		path = "config/users.yaml"
	}
	accounts, err := auth.LoadAccounts(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("no users file at %s; authentication is OFF (set LEUTIK_USERS_PATH to enable)", path)
			return nil
		}
		log.Fatalf("users %s: %v", path, err)
	}
	secret := os.Getenv("LEUTIK_SESSION_SECRET")
	if secret == "" {
		log.Printf("LEUTIK_SESSION_SECRET not set; sign-ins will not survive a restart")
	}
	token := os.Getenv("LEUTIK_INGEST_TOKEN")
	if token == "" {
		log.Printf("LEUTIK_INGEST_TOKEN not set; ingest is UNAUTHENTICATED")
	}
	log.Printf("auth on: accounts from %s (session cookie + ingest bearer token)", path)
	return auth.New(accounts, auth.NewSession(secret), auth.NewIngest(token))
}

// pickStore uses ClickHouse when configured and the in-memory store otherwise,
// so the service runs on a laptop with no dependencies.
func pickStore() (store.Store, string) {
	url := os.Getenv("LEUTIK_CLICKHOUSE_URL")
	if url == "" {
		return store.NewMemory(), "memory store, data is lost on exit"
	}
	ch, err := store.NewClickHouse(url)
	if err != nil {
		log.Fatalf("clickhouse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ch.Ping(ctx); err != nil {
		// Fail loudly: silently falling back to memory would look like it works
		// while quietly storing nothing durable.
		log.Fatalf("clickhouse at %s unreachable: %v", url, err)
	}
	return ch, "clickhouse " + url
}

// runMigrations applies pending DDL. Separate from startup on purpose: a
// service that migrates itself on boot makes a bad deploy unrecoverable.
func runMigrations() {
	url := os.Getenv("LEUTIK_CLICKHOUSE_URL")
	if url == "" {
		log.Fatal("-migrate needs LEUTIK_CLICKHOUSE_URL")
	}
	ch, err := store.NewClickHouse(url)
	if err != nil {
		log.Fatalf("clickhouse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ch.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations up to date")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
