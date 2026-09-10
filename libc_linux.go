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
	return detectLibcWith(lddProbeTimeout, runLddVersion, lddGlob)
}

// lddGlob is a seam, for the same reason detectLibcWith takes a glob at all: the
// musl fallback is otherwise unreachable on a glibc host. Since build 3ef9370 the
// glob is consulted only when `ldd` produced no output (see classifyLibc), so on
// any host whose `ldd` answers — every real one — production never reaches the
// fallback, and the seam is what lets a test drive that branch. Production never
// reassigns it.
//
// Declared BELOW detectLibc deliberately. Above it, this comment butted straight
// against detectLibc's own doc comment with no blank line, so godoc attached all
// twelve lines to the variable and detectLibc shipped undocumented — while the
// merged block read as one paragraph in which "decides which CLI build to fetch"
// and "lddGlob is a seam" were the same thought.
var lddGlob = filepath.Glob
