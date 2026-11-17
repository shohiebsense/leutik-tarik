//go:build linux

package bpf

import _ "embed"

// argsObject is the compiled argument-reading program.
//
//go:embed args.bpf.o
var argsObject []byte
