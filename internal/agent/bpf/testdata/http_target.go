//go:build ignore

// Fixture: a plain net/http server with no instrumentation of any kind. If a
// probe can read its status codes, automatic capture works without app changes.
package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("down\n"))
	})
	http.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("fine\n"))
	})
	fmt.Println("listening on 127.0.0.1:18099")
	_ = http.ListenAndServe("127.0.0.1:18099", nil)
}
