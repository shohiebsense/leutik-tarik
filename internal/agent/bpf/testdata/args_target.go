//go:build ignore

// Fixture for the argument-register test: calls a named function with a known
// value in a loop, so a probe can assert it read the right register.
package main

import (
	"fmt"
	"time"
)

//go:noinline
func mark(n int) {
	_ = n
}

func main() {
	fmt.Println("args target ready")
	for i := 0; i < 2000; i++ {
		mark(4242)
		time.Sleep(20 * time.Millisecond)
	}
}
