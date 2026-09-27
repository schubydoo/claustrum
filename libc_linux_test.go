//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDetectLibcHonoursThePackageVar is the ONE test that drives the production
// path: detectLibc() itself, resolving a real `ldd` through PATH, with nothing
// injected. Every other ldd-bound test calls detectLibcWith directly and hands it
// a timeout, so all of them stay green if the single production call site stops
// reading lddProbeTimeout.
//
// The discriminating arm is the SHRUNK one: a hardcoded value at the call site
// ignores the tiny bound, so the stub completes and reports musl where the test
// demands the glibc fallback.
func TestDetectLibcHonoursThePackageVar(t *testing.T) {
	// Mask the loader glob rather than skipping on it. A glibc development host can
	// carry /lib/ld-musl-x86_64.so.1 from an unrelated package. When the bound
	// kills ldd its output is dropped and the loader glob decides. An unmasked
	// glob on such a host reports musl where the shrunk arm demands glibc.
	oldGlob := lddGlob
	t.Cleanup(func() { lddGlob = oldGlob })
	lddGlob = func(string) ([]string, error) { return nil, nil }

	dir := t.TempDir()
	// Prints a musl banner after 1 s. The exit code is not consulted since
	// 3ef9370, so exit 0 here is incidental. The bound kills the stub's whole
	// process group, so the `sleep` child dies with it and nothing leaks.
	stub := "#!/bin/sh\nsleep 1\necho 'musl libc (x86_64)'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ldd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	old := lddProbeTimeout
	t.Cleanup(func() { lddProbeTimeout = old })

	// The shipped 5 s bound: the 1 s stub answers in time and its answer stands.
	if got := detectLibc(); got != "musl" {
		t.Errorf("detectLibc() at the shipped bound = %q, want musl — a 1s ldd answers "+
			"inside 5s and its output must decide", got)
	}

	// Shrunk below the stub's latency: the probe is cut off and the fallback
	// stands. A call site that ignores lddProbeTimeout fails HERE.
	lddProbeTimeout = 50 * time.Millisecond
	if got := detectLibc(); got != "glibc" {
		t.Errorf("detectLibc() with a 50ms bound = %q, want the glibc fallback — "+
			"detectLibc must pass lddProbeTimeout through, not a hardcoded value", got)
	}
}
