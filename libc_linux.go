//go:build linux

package main

import "path/filepath"

// detectLibc probes the C library on linux, where a musl-vs-glibc distinction
// decides which CLI build to fetch. See classifyLibc for the rule.
//
// ⚠️ "decides which CLI build to fetch" is a claim about the DRIVER, not about the
// reference daemon — the parity harness cannot settle it. See
// docs/ARCHITECTURE.md → Driver claims and their provenance.
func detectLibc() string {
	return detectLibcWith(lddProbeTimeout, runLddVersion, filepath.Glob)
}
