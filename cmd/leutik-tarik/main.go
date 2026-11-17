// Command leutik-tarik runs the ingest + query/UI service.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/shohiebsense/leutik-tarik/internal/server"
	"github.com/shohiebsense/leutik-tarik/internal/store"
)

func main() {
	// Loopback by default: this is an internal telemetry store with no auth yet,
	// so it must not be reachable from the LAN by accident (review P6).
	addr := flag.String("addr", envOr("LEUTIK_ADDR", "127.0.0.1:8080"), "listen address")
	flag.Parse()

	s := store.NewMemory()
	log.Printf("leutik-tarik listening on %s (memory store)", *addr)
	if err := http.ListenAndServe(*addr, server.New(s)); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
