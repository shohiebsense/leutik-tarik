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

	"github.com/shohiebsense/leutik-tarik/internal/server"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func main() {
	// Loopback by default: this is an internal telemetry store with no auth yet,
	// so it must not be reachable from the LAN by accident (review P6).
	addr := flag.String("addr", envOr("LEUTIK_ADDR", "127.0.0.1:8080"), "listen address")
	flag.Parse()

	st, label := pickStore()

	srv := &http.Server{
		Addr:    *addr,
		Handler: server.New(st),
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
