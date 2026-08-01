//go:build unix

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// extractLoginPATH runs the login shell to resolve an interactive PATH and
// installs it into the daemon's environment. It is best-effort, so the contract
// under test is narrow: it must not panic, and it must leave PATH non-empty
// (either the freshly-extracted value or the pre-existing one on any failure).
func TestExtractLoginPATH(t *testing.T) {
	// Snapshot PATH via t.Setenv so the framework restores it after the test,
	// since extractLoginPATH mutates the process environment directly.
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")

	extractLoginPATH()

	if os.Getenv("PATH") == "" {
		t.Error("extractLoginPATH left PATH empty")
	}
}

// With SHELL unset, extractLoginPATH falls back to /bin/sh rather than failing.
func TestExtractLoginPATHDefaultShell(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", "")

	extractLoginPATH() // must not panic; exercises the empty-SHELL default branch

	if os.Getenv("PATH") == "" {
		t.Error("extractLoginPATH (default shell) left PATH empty")
	}
}

// When the login shell produces no PATH sentinel (e.g. SHELL ignores the
// command), extractLoginPATH logs the not-found line and leaves PATH intact.
func TestExtractLoginPATHNoSentinel(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	t.Setenv("PATH", os.Getenv("PATH"))
	// A CONTROLLED stub that emits nothing, not a system binary.
	//
	// This used to be "/bin/true", which worked only by accident and stopped
	// working the moment safeLoginShell landed: macOS has no /bin/true (it ships
	// /usr/bin/true), so $SHELL became non-executable, safeLoginShell correctly
	// fell back to /bin/bash, and bash emitted a REAL sentinel — the macOS leg
	// failed with "Extracted shell PATH (1008 chars)" where a not-found log was
	// expected. The stub removes the dependency on which system binaries a
	// platform happens to place where.
	t.Setenv("SHELL", writeFakeShell(t, "")) // ignores args, emits nothing

	extractLoginPATH()

	// The reference wraps the failure and appends the shell's own output, so the
	// log says WHY it could not parse anything. Measured at 5db5e4a:
	// "[shellenv] Failed to extract PATH from login shell: PATH sentinel not
	// found in shell output: no sentinel here".
	if !strings.Contains(buf.String(), "[shellenv] Failed to extract PATH from login shell: PATH sentinel not found in shell output:") {
		t.Errorf("expected the wrapped sentinel-not-found log, got %q", buf.String())
	}
}

// writeFakeShell drops an executable #!/bin/sh stub at a temp path and points
// SHELL at it, so extractLoginPATH runs a script with fully-controlled output.
// The stub ignores the -l/-i/-c args extractLoginPATH passes.
func writeFakeShell(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakeshell.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A successful login shell that emits the sentinel at column 0 (index 0) with a
// distinct PATH must install exactly that value. This pins two conditions the
// existing "PATH non-empty" assertion can't: the `i < 0` boundary (a `i <= 0`
// mutant would skip the col-0 match) and the `path != ""` guard (a negated guard
// would refuse to set a non-empty value). Asserting the *new* value — not just
// non-emptiness — is what makes both observable.
func TestExtractLoginPATHInstallsExtractedValue(t *testing.T) {
	const want = "/zzz/claustrum/extracted/bin"
	const daemonPATH = "/original/path/should/be/replaced"
	// Sentinel printed at the very start of the line → strings.Index == 0.
	shell := writeFakeShell(t, "printf '%s%s\\n' '"+pathSentinel+"' '"+want+"'")

	t.Setenv("PATH", daemonPATH)
	t.Setenv("SHELL", shell)
	t.Cleanup(resetLoginPATHForTest)

	extractLoginPATH()

	if got := currentLoginPATH(); got != want {
		t.Errorf("loginPATH = %q, want %q (column-0 sentinel must record the extracted value)", got, want)
	}
	// And it must NOT be installed into the daemon's own environment. The
	// reference keeps the two apart; mutating os.Environ made the daemon resolve
	// its own tools through the user's login PATH — measured with a fake `git`
	// reachable only from the login PATH, which claustrum then ran instead of the
	// real one.
	if got := os.Getenv("PATH"); got != daemonPATH {
		t.Errorf("daemon PATH = %q, want it untouched at %q", got, daemonPATH)
	}
}

// The login-PATH cap is the reference's 4s, not a rounder number of our own.
// Probe-measured against 5db5e4a: with a login shell that sleeps 6s the
// reference answers the first process.spawn after 4.01s, claustrum at 10s
// answered after 5.82s. The constant is the whole behaviour here — a slow login
// shell changes both spawn latency and the PATH every child inherits — so lock
// the value, not just the fact that a timeout exists.
func TestLoginPATHTimeoutMatchesReference(t *testing.T) {
	if loginPATHTimeout != 4*time.Second {
		t.Errorf("loginPATHTimeout = %v, want 4s (reference-measured at 5db5e4a)", loginPATHTimeout)
	}
}

// A stalling login shell must not block extractLoginPATH forever. The internal
// context timeout kills the subprocess and the function returns. This pins the
// exec.CommandContext usage; without it the function would hang indefinitely.
func TestExtractLoginPATHDoesNotHang(t *testing.T) {
	old := loginPATHTimeout
	loginPATHTimeout = 500 * time.Millisecond
	t.Cleanup(func() { loginPATHTimeout = old })

	shell := writeFakeShell(t, "sleep 9999")
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", os.Getenv("PATH"))

	done := make(chan struct{})
	go func() { extractLoginPATH(); close(done) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("extractLoginPATH did not return — internal timeout not working")
	}
}

// When the login shell exits non-zero, extractLoginPATH logs the error branch.
// Pins the `err != nil` guard: a negated guard would log only on success and stay
// silent here.
func TestExtractLoginPATHLogsShellError(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	shell := writeFakeShell(t, "exit 7") // non-zero exit → CombinedOutput err != nil

	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", shell)

	extractLoginPATH()

	if !strings.Contains(buf.String(), "[shellenv] Shell command exited with error") {
		t.Errorf("non-zero-exit shell did not log the error branch; got %q", buf.String())
	}
}

// $SHELL is used only when it is an executable file. A non-executable value
// falls through to the candidate list rather than being handed to exec, which
// would fail the extraction outright and leave children with the daemon's bare
// PATH. Measured at 5db5e4a with a non-executable $SHELL: the reference still
// logged "Extracted shell PATH (262 chars)" while claustrum logged
// "fork/exec …: permission denied" and gave up.
func TestSafeLoginShellRejectsNonExecutableShell(t *testing.T) {
	dir := t.TempDir()

	exe := filepath.Join(dir, "good")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", exe)
	if got := safeLoginShell(); got != exe {
		t.Errorf("safeLoginShell() = %q, want the executable $SHELL %q", got, exe)
	}

	noexec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noexec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", noexec)
	got := safeLoginShell()
	if got == noexec {
		t.Fatal("safeLoginShell() returned a non-executable $SHELL")
	}
	if !isExecutableFile(got) {
		t.Errorf("safeLoginShell() = %q, which is not executable", got)
	}

	// A directory is not a shell either, and neither is a missing path.
	t.Setenv("SHELL", dir)
	if got := safeLoginShell(); got == dir {
		t.Error("safeLoginShell() accepted a directory as $SHELL")
	}
	t.Setenv("SHELL", filepath.Join(dir, "does-not-exist"))
	if got := safeLoginShell(); !isExecutableFile(got) {
		t.Errorf("safeLoginShell() with a missing $SHELL = %q, want a usable fallback", got)
	}

	// Empty $SHELL folds into the same path.
	t.Setenv("SHELL", "")
	if got := safeLoginShell(); !isExecutableFile(got) {
		t.Errorf("safeLoginShell() with an empty $SHELL = %q, want a usable fallback", got)
	}
}

// When nothing in the candidate list is usable, safeLoginShell still returns
// /bin/sh and lets the exec fail and be logged, rather than returning "" and
// exec'ing the empty string. Unreachable on a normal host, so the candidate list
// is overridden here.
func TestSafeLoginShellLastResort(t *testing.T) {
	old := fallbackShells
	fallbackShells = []string{"/nonexistent/bash", "/nonexistent/zsh"}
	t.Cleanup(func() { fallbackShells = old })

	t.Setenv("SHELL", "")
	if got := safeLoginShell(); got != "/bin/sh" {
		t.Errorf("safeLoginShell() with no usable candidate = %q, want /bin/sh", got)
	}
}
