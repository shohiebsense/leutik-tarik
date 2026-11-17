//go:build ignore

// Fixture for the uprobe integration test: a Go binary with a known symbol
// (main.fire) that keeps firing so a probe can be attached after start-up.
package main

import (
	"fmt"
	"time"
)

//go:noinline
func fire(n int) int {
	return n*2 + 1
}

func main() {
	fmt.Println("target ready")
	for i := 0; i < 2000; i++ {
		fire(i)
		time.Sleep(20 * time.Millisecond)
	}
}
