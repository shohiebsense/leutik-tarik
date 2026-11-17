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

	opts := server.Options{Store: st, Policies: policies}
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
func pickPolicy() *policy.Engine {
	path := os.Getenv("LEUTIK_POLICY_PATH")
	if path == "" {
		path = "config/policy.yaml"
		if _, err := os.Stat(path); err != nil {
			path = "config/policy.example.yaml"
		}
	}
	p, err := policy.Load(path)
	if err != nil {
		log.Fatalf("policy %s: %v", path, err)
	}
	log.Printf("policy %s loaded: version %d, %d rules, default trace retention %s",
		path, p.Version, len(p.Rules), p.Defaults.Trace.Duration())
	return policy.New(p)
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
