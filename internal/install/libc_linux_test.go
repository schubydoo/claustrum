//go:build linux

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDetectLibcHonoursThePackageVar is the ONE test that drives the production
// path: detectLibc() itself, running the `ldd` at lddPath, with nothing else
// injected. Every other ldd-bound test calls detectLibcWith directly and hands it
// a timeout, so all of them stay green if the single production call site stops
// reading LddProbeTimeout.
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
	useLddStub(t, dir, stub)

	old := LddProbeTimeout
	t.Cleanup(func() { LddProbeTimeout = old })

	// The shipped 5 s bound: the 1 s stub answers in time and its answer stands.
	if got := detectLibc(); got != "musl" {
		t.Errorf("detectLibc() at the shipped bound = %q, want musl — a 1s ldd answers "+
			"inside 5s and its output must decide", got)
	}

	// Shrunk below the stub's latency: the probe is cut off and the fallback
	// stands. A call site that ignores LddProbeTimeout fails HERE.
	LddProbeTimeout = 50 * time.Millisecond
	if got := detectLibc(); got != "glibc" {
		t.Errorf("detectLibc() with a 50ms bound = %q, want the glibc fallback — "+
			"detectLibc must pass LddProbeTimeout through, not a hardcoded value", got)
	}
}

// TestLddRunsInTheRootFolder pins the working folder of the ldd probe: the stub
// records its folder, and the folder must be "/".
func TestLddRunsInTheRootFolder(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "cwd")
	stub := "#!/bin/sh\npwd > '" + rec + "'\necho 'ldd (GNU libc) 2.39'\n"
	useLddStub(t, dir, stub)
	// Start from a folder other than "/".
	t.Chdir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runLddVersion(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "/\n" {
		t.Errorf("ldd ran in %q, want \"/\"", got)
	}
}

// detectLibc's fallback branch: when `ldd` can't be executed, it probes for the
// musl loader and otherwise reports glibc. An lddPath with no file forces that
// branch.
func TestDetectLibcLddMissing(t *testing.T) {
	old := lddPath
	t.Cleanup(func() { lddPath = old })
	lddPath = filepath.Join(t.TempDir(), "ldd") // no file there
	if got := detectLibc(); got != "glibc" && got != "musl" {
		t.Errorf("detectLibc() with no ldd = %q, want glibc or musl", got)
	}
}

// TestLddPathIsUsrBin pins the one ldd that the probe runs.
func TestLddPathIsUsrBin(t *testing.T) {
	if lddPath != "/usr/bin/ldd" {
		t.Errorf("lddPath = %q, want /usr/bin/ldd", lddPath)
	}
}

// TestLddRunsLddPathNotPATH runs the probe with one stub at lddPath and another
// stub named ldd first on PATH. Only the stub at lddPath runs.
func TestLddRunsLddPathNotPATH(t *testing.T) {
	dir, pathDir := t.TempDir(), t.TempDir()
	rec := filepath.Join(dir, "ran")
	useLddStub(t, dir, "#!/bin/sh\necho fixed >> '"+rec+"'\necho 'ldd (GNU libc) 2.39'\n")
	onPath := "#!/bin/sh\necho path >> '" + rec + "'\necho 'ldd (GNU libc) 2.39'\n"
	if err := os.WriteFile(filepath.Join(pathDir, "ldd"), []byte(onPath), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runLddVersion(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fixed\n" {
		t.Errorf("the stubs that ran wrote %q, want only the stub at lddPath", got)
	}
}

// TestLddGetsTheFixedEnvironment pins the whole environment of the probe. The stub
// writes `env | sort`, and the set must be the measured one: LC_ALL=C and
// PATH=/usr/bin:/bin. The lines that the shell of the stub sets itself (PWD, SHLVL
// and _ under bash) are left out, so the test does not depend on which shell
// /bin/sh is.
func TestLddGetsTheFixedEnvironment(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "env")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("CLAUSTRUM_OPERATOR_VAR", "opvalue")
	useLddStub(t, dir, "#!/bin/sh\nenv | sort > '"+rec+"'\necho 'ldd (GNU libc) 2.39'\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runLddVersion(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
		if strings.HasPrefix(line, "PWD=") || strings.HasPrefix(line, "SHLVL=") || strings.HasPrefix(line, "_=") {
			continue
		}
		kept = append(kept, line)
	}
	if got, want := strings.Join(kept, "\n"), "LC_ALL=C\nPATH=/usr/bin:/bin"; got != want {
		// Name the variables only: their values can hold secrets of the test host.
		names := make([]string, 0, len(kept))
		for _, line := range kept {
			name, _, _ := strings.Cut(line, "=")
			names = append(names, name)
		}
		t.Errorf("ldd saw the variables %q, want exactly LC_ALL=C and PATH=/usr/bin:/bin", names)
	}
}
