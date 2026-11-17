//go:build linux

package bpf

import _ "embed"

// probeObject is the compiled program, committed so a normal build needs no
// clang (Apple's clang cannot target bpf at all).
//
//go:embed probe.bpf.o
var probeObject []byte
