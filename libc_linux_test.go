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
// injected. Every other D14 test calls detectLibcWith directly and hands it a
// timeout, so all of them stay green if the single production call site stops
// reading lddProbeTimeout.
//
// 🔴 That gap was measured, not imagined. Replacing libc_linux.go's
//
//	detectLibcWith(lddProbeTimeout, runLddVersion, filepath.Glob)
//
// with a hardcoded 5s passes `go vet`, `golangci-lint` and the entire -race suite:
// D14's flip is undone in production while the flag, the config key, the
// precedence resolver and lddCtx all still behave perfectly, because nothing
// connected them to the shipped code path. gitTimeout and filesReadRegularOnly do
// not have this hole — they are read on paths the socket goldens traverse. This
// one is linux-only and no integration test drives it.
//
// The discriminating arm is the OPTED-IN one: a hardcoded value at the call site
// ignores the tiny deadline, so the stub completes and reports musl where the test
// demands the glibc fallback.
func TestDetectLibcHonoursThePackageVar(t *testing.T) {
	// Mask the loader glob rather than skipping on it. This glibc development host
	// HAS /lib/ld-musl-x86_64.so.1, installed by an unrelated package. Since build
	// 3ef9370 ldd runs on every call, so masking is not what makes ldd execute — it
	// makes the OPTED-IN arm deterministic. When the deadline kills ldd its output
	// is empty and the loader glob decides the fallback, and an unmasked glob would
	// match on this host and report musl where the arm demands the glibc fallback.
	// A skip here would have been the same coverage hole mustMkfifo exists to
	// prevent, hidden behind a plausible guard.
	oldGlob := lddGlob
	t.Cleanup(func() { lddGlob = oldGlob })
	lddGlob = func(string) ([]string, error) { return nil, nil }

	dir := t.TempDir()
	// Prints a musl banner: the default arm waits for it and reports musl. The exit
	// code is not consulted since 3ef9370, so exit 0 here is incidental.
	// sleep 1, not longer: exec.CommandContext kills the script but `sleep` is its
	// child and survives holding the output pipe, so the opted-in arm waits for it
	// either way. One second self-exits and leaks nothing; raising it leaks a
	// process per run, exactly as slowCLI warns.
	stub := "#!/bin/sh\nsleep 1\necho 'musl libc (x86_64)'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ldd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	old := lddProbeTimeout
	t.Cleanup(func() { lddProbeTimeout = old })

	// Default: nothing armed, so the 1s stub is waited for and its answer stands.
	lddProbeTimeout = 0
	if got := detectLibc(); got != "musl" {
		t.Errorf("detectLibc() at the shipped default = %q, want musl — a 1s ldd must be "+
			"waited for; if this says glibc the deadline is armed when it must not be", got)
	}

	// Opted in below the stub's latency: the probe is cut off and the fallback
	// stands. A call site that ignores lddProbeTimeout fails HERE.
	lddProbeTimeout = 50 * time.Millisecond
	if got := detectLibc(); got != "glibc" {
		t.Errorf("detectLibc() with a 50ms deadline = %q, want the glibc fallback — "+
			"detectLibc must pass lddProbeTimeout through, not a hardcoded value", got)
	}
}
