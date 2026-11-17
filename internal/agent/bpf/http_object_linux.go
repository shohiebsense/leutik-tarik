//go:build linux

package bpf

import _ "embed"

// httpObject is the compiled HTTP probe.
//
//go:embed http.bpf.o
var httpObject []byte
