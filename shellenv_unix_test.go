//go:build unix

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	t.Setenv("SHELL", "/bin/true") // ignores args → emits nothing

	extractLoginPATH()

	if !strings.Contains(buf.String(), "[shellenv] PATH sentinel not found in shell output") {
		t.Errorf("expected sentinel-not-found log, got %q", buf.String())
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
	// Sentinel printed at the very start of the line → strings.Index == 0.
	shell := writeFakeShell(t, "printf '%s%s\\n' '"+pathSentinel+"' '"+want+"'")

	t.Setenv("PATH", "/original/path/should/be/replaced")
	t.Setenv("SHELL", shell)

	extractLoginPATH()

	if got := os.Getenv("PATH"); got != want {
		t.Errorf("PATH = %q, want %q (column-0 sentinel must install the extracted value)", got, want)
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
