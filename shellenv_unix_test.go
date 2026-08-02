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
	t.Setenv("SHELL", "/bin/sh")
	// extractLoginPATH records into loginPATH, NOT the process environment, so it
	// leaves package state behind for the next test. Reset both ways round.
	resetLoginPATHForTest()
	t.Cleanup(resetLoginPATHForTest)

	extractLoginPATH()

	// Assert the recorded value, not os.Getenv("PATH"). The old assertion could
	// no longer fail: extractLoginPATH deliberately stopped touching the process
	// environment, so PATH was only ever whatever t.Setenv had just put there.
	if currentLoginPATH() == "" {
		t.Error("extractLoginPATH recorded no login PATH")
	}
}

// With SHELL unset, extractLoginPATH falls back to /bin/sh rather than failing.
func TestExtractLoginPATHDefaultShell(t *testing.T) {
	t.Setenv("SHELL", "")
	resetLoginPATHForTest()
	t.Cleanup(resetLoginPATHForTest)

	extractLoginPATH() // must not panic; exercises the empty-SHELL default branch

	if currentLoginPATH() == "" {
		t.Error("extractLoginPATH (default shell) recorded no login PATH")
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

// The reference prefers zsh over bash. Measured 2026-08-02 against 5db5e4a two
// ways — stand-in shells bind-mounted over /usr/bin, and real bash+zsh in a
// clean Ubuntu VM with a marker in each shell's own login profile — both with
// single-shell controls that agreed. See the comment on fallbackShells.
//
// Pinning the shipped order directly is the point: this list was transcribed
// backwards from a record that had it right, so the defect is in the constant
// itself and no amount of behavioural testing around it would notice.
func TestFallbackShellsPrefersZshThenBash(t *testing.T) {
	want := []string{"/bin/zsh", "/bin/bash", "/bin/sh"}
	if len(fallbackShells) != len(want) {
		t.Fatalf("fallbackShells = %v, want %v", fallbackShells, want)
	}
	for i := range want {
		if fallbackShells[i] != want[i] {
			t.Errorf("fallbackShells[%d] = %q, want %q (the reference prefers zsh)",
				i, fallbackShells[i], want[i])
		}
	}
}

// safeLoginShell must walk fallbackShells in order and take the FIRST executable
// entry, so the order above is actually load-bearing rather than decorative.
func TestSafeLoginShellTakesTheFirstExecutableCandidate(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first, second := mk("first"), mk("second")

	old := fallbackShells
	t.Cleanup(func() { fallbackShells = old })
	t.Setenv("SHELL", "") // force the fallback list

	fallbackShells = []string{filepath.Join(dir, "absent"), first, second}
	if got := safeLoginShell(); got != first {
		t.Errorf("safeLoginShell() = %q, want the first executable candidate %q", got, first)
	}
	// Reverse the two: the answer must follow the LIST, not the filesystem.
	fallbackShells = []string{filepath.Join(dir, "absent"), second, first}
	if got := safeLoginShell(); got != second {
		t.Errorf("safeLoginShell() = %q, want %q after reordering the list", got, second)
	}
}

// A login shell that prints a GOOD sentinel and then hangs past the cap must
// leave loginPATH untouched: the reference discards the value on timeout.
//
// Measured 2026-08-02 against 5db5e4a — the reference's spawned child did NOT
// receive the extracted PATH, while claustrum's did. Wire-visible, because
// loginPATH becomes every spawned child's environment.
func TestExtractLoginPATHDiscardsTheValueOnTimeout(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	oldTimeout := loginPATHTimeout
	loginPATHTimeout = 300 * time.Millisecond
	t.Cleanup(func() { loginPATHTimeout = oldTimeout })

	// Sentinel first, THEN outlive the cap. The sentinel being valid is the whole
	// point: a stub that merely hangs cannot tell "discarded" from "never seen".
	shell := writeFakeShell(t, "printf '%s%s\\n' '"+pathSentinel+"' '/zzz/timed/out'\nsleep 5")

	// Keep the REAL PATH: the stub has to resolve `sleep` to hang at all. An
	// earlier version pinned PATH to a fake value, so the stub exited 127
	// immediately, no timeout fired, and the test passed against the unfixed code
	// for the wrong reason.
	t.Setenv("SHELL", shell)
	// Reset BEFORE the body, not only after it. The "" precondition below is a
	// claim about package state, and registering the reset purely as cleanup
	// borrows it from whichever test happened to run earlier — so this passed
	// under the full suite and failed under any -run filter or -shuffle.
	resetLoginPATHForTest()
	t.Cleanup(resetLoginPATHForTest)

	start := time.Now()
	extractLoginPATH()
	elapsed := time.Since(start)

	// The stub must actually have HUNG. If it dies early instead — an unresolved
	// `sleep`, say — extractLoginPATH returns at once, no deadline fires, and
	// every assertion below passes against unfixed code. That exact false pass
	// happened while writing this test.
	if elapsed < loginPATHTimeout {
		t.Fatalf("stub returned in %v, before the %v cap — it did not hang, so this "+
			"test proves nothing about the timeout path", elapsed, loginPATHTimeout)
	}
	if got := currentLoginPATH(); got != "" {
		t.Errorf("loginPATH = %q after a timeout, want empty — the reference discards it", got)
	}
	if !strings.Contains(buf.String(), "shell PATH extraction timed out") {
		t.Errorf("expected the one-line timeout log, got %q", buf.String())
	}
	// One line, not two: the reference says nothing about the exit status here.
	if strings.Contains(buf.String(), "Shell command exited with error") {
		t.Errorf("timeout must not also log the exit-status line, got %q", buf.String())
	}
}

// The echoed shell output is cut at 200 bytes plus "...". Measured 2026-08-02
// against 5db5e4a: a 500-byte no-sentinel shell produced a 203-character payload.
func TestTruncateShellOutput(t *testing.T) {
	if got := truncateShellOutput("short"); got != "short" {
		t.Errorf("short input = %q, want it unchanged", got)
	}
	exact := strings.Repeat("x", shellOutputLogLimit)
	if got := truncateShellOutput(exact); got != exact {
		t.Errorf("input at the limit was modified: len %d", len(got))
	}
	over := strings.Repeat("y", shellOutputLogLimit+1)
	got := truncateShellOutput(over)
	if len(got) != shellOutputLogLimit+3 {
		t.Errorf("len = %d, want %d (200 kept + \"...\")", len(got), shellOutputLogLimit+3)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated output %q must end in \"...\"", got)
	}
}

// And the truncation must actually reach the log line, not just exist.
func TestExtractLoginPATHTruncatesTheLoggedShellOutput(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	t.Setenv("PATH", os.Getenv("PATH"))
	// 500 bytes, no sentinel anywhere — the same shape as the measured fixture.
	t.Setenv("SHELL", writeFakeShell(t, "printf '%0500d' 0"))

	extractLoginPATH()

	const marker = "PATH sentinel not found in shell output: "
	i := strings.Index(buf.String(), marker)
	if i < 0 {
		t.Fatalf("expected the sentinel-not-found log, got %q", buf.String())
	}
	payload := strings.TrimRight(buf.String()[i+len(marker):], "\n")
	if len(payload) != shellOutputLogLimit+3 {
		t.Errorf("logged payload is %d chars, want %d — the cut is not reaching the log",
			len(payload), shellOutputLogLimit+3)
	}
}

// stubLoginPATHExtractor replaces the extraction seam with a no-op for the
// duration of a test.
//
// CORRECTION, 2026-08-02: two lifecycle tests used to do this by pointing $SHELL
// at "/claustrum-no-such-shell", commented "keep extractLoginPATH inert". That
// stopped being true when safeLoginShell gained a fallback list: an unusable
// $SHELL no longer fails the extraction, it falls through to /bin/zsh or
// /bin/bash and runs a REAL login shell. runServe starts that in a background
// goroutine which outlives the test and writes the runner's real login PATH into
// package state at an arbitrary later moment, bounded only by the 4s cap.
//
// Nothing failed because ~24 test files sit between those tests and this one, so
// the write always landed in a gap. That is wall-clock luck, not isolation —
// reproduced with `go test -count=15 -run 'TestRunServeChildFullLifecycle|
// TestExtractLoginPATHDiscardsTheValueOnTimeout'`, which fails with the leaked
// PATH. Stub the seam, which is what it exists for (see shellenv_test.go).
// The restore must AWAIT the extraction goroutine. startLoginPATH reads the seam
// inside the goroutine (shellenv.go:61), so restoring it from cleanup while that
// goroutine is live is a genuine data race — caught by -race the first time this
// helper existed, on exactly the tests it was written for. awaitLoginPATH returns
// immediately when nothing was started, so this is free for tests that never boot
// a server.
func stubLoginPATHExtractor(t *testing.T) {
	t.Helper()
	old := loginPATHExtractor
	loginPATHExtractor = func() {}
	t.Cleanup(func() {
		awaitLoginPATH()
		loginPATHExtractor = old
	})
}
