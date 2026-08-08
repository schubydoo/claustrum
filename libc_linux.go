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
// musl branch is otherwise unreachable on a glibc host, and — the direction that
// matters here — the ldd branch is unreachable on any host that HAS the loader.
// This host does: `/lib/ld-musl-x86_64.so.1` exists on a glibc Debian because some
// package installs it, so without this seam the one test that drives detectLibc's
// production path skips silently and proves nothing. Production never reassigns it.
//
// Declared BELOW detectLibc deliberately. Above it, this comment butted straight
// against detectLibc's own doc comment with no blank line, so godoc attached all
// twelve lines to the variable and detectLibc shipped undocumented — while the
// merged block read as one paragraph in which "decides which CLI build to fetch"
// and "lddGlob is a seam" were the same thought.
var lddGlob = filepath.Glob
