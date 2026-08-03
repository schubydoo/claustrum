//go:build linux

package main

import "path/filepath"

// detectLibc probes the C library on linux, where a musl-vs-glibc distinction
// decides which CLI build to fetch. See classifyLibc for the rule.
func detectLibc() string {
	return detectLibcWith(lddProbeTimeout, runLddVersion, filepath.Glob)
}
