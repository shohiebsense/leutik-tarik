//go:build !linux

package main

import "fmt"

// runHTTPProbe reports the platform limitation instead of doing nothing.
func runHTTPProbe(cfg probeConfig) error {
	return fmt.Errorf("HTTP probing requires Linux; this host is not supported")
}
