//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// markerOfPATHRead sets $m to the sentinel of the inner command in "$4". It uses
// only the shell, no other program.
const markerOfPATHRead = `m=${4#*'"___CLAUDE_SSH_PATH_'}; m=___CLAUDE_SSH_PATH_${m%%'___"'*}___; `

// helperInShell is a shell command that runs this test binary in a helper mode. The
// shell of a read gets eight variables only, so the line sets the mode itself.
func helperInShell(t *testing.T, mode string, args ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "CLAUSTRUM_TEST_HELPER=" + mode + " GORACE=atexit_sleep_ms=0 '" + exe + "' " + strings.Join(args, " ")
}

// TestExtractLoginPATH runs a read through a stub login shell that runs the inner
// command, so no login profile of the runner runs and no time limit is near.
func TestExtractLoginPATH(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/zzz/daemon")
	t.Setenv("SHELL", writeFakeShell(t, `exec /bin/sh -c "$4"`))
	got, err := extractLoginPATH(context.Background())
	if err != nil || got != "/usr/bin:/bin:/zzz/daemon" {
		t.Errorf("extractLoginPATH() = %q, %v; want the PATH that the shell printed", got, err)
	}
}

// With no usable $SHELL the read takes the first usable fallback shell.
func TestExtractLoginPATHDefaultShell(t *testing.T) {
	old := fallbackShells
	t.Cleanup(func() { fallbackShells = old })
	fallbackShells = []string{writeFakeShell(t, `exec /bin/sh -c "$4"`)}
	t.Setenv("SHELL", "")
	if got, err := extractLoginPATH(context.Background()); err != nil || got == "" {
		t.Errorf("extractLoginPATH() with the fallback shell = %q, %v", got, err)
	}
}

// TestPathReadCommandHasAFreshSentinel pins the argv of 52d82f0a (Linux and macOS):
// -l, -i, -c and one command that prints the sentinel and the PATH on two lines. Two
// reads give two sentinels of 32 hex digits.
func TestPathReadCommandHasAFreshSentinel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", writeFakeShell(t, `printf '%s|' "$#" "$1" "$2" "$3" "$4" >> '`+filepath.Join(dir, "argv")+`'; echo >> '`+filepath.Join(dir, "argv")+`'`))
	for range 2 {
		_, _ = extractLoginPATH(context.Background())
	}
	b, err := os.ReadFile(filepath.Join(dir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^4\|-l\|-i\|-c\|/bin/sh -c 'printf "%s\\n%s\\n" "(___CLAUDE_SSH_PATH_[0-9a-f]{32}___)" "\$PATH"'\|$`)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("argv lines = %q, want two", lines)
	}
	var sentinels []string
	for _, l := range lines {
		m := re.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("argv %q does not match %s", l, re)
		}
		sentinels = append(sentinels, m[1])
	}
	if sentinels[0] == sentinels[1] {
		t.Errorf("two reads gave the same sentinel %s", sentinels[0])
	}
}

// TestExtractLoginPATHParsesTheShellOutput pins the parse of 52d82f0a (Linux and
// macOS). The line after the sentinel line is the PATH, on stdout or on stderr. The
// stream order is claustrum's own. It must end in a newline and hold an absolute directory. The exit status does not
// matter then. No failure text holds what the shell printed.
func TestExtractLoginPATHParsesTheShellOutput(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, wantErr string
	}{
		{"only on stderr", markerOfPATHRead + `printf '%s\n%s\n' "$m" /x:/y >&2`, "/x:/y", ""},
		{"no absolute directory", markerOfPATHRead + `printf '%s\nrelative:dirs\n' "$m"`, "",
			"the login shell's PATH line was cut short or had no absolute directory in it (13 bytes read)"},
		{"no final newline", markerOfPATHRead + `printf '%s\n/x:/y' "$m"`, "",
			"the login shell's PATH line was cut short or had no absolute directory in it (5 bytes read)"},
		{"a good line and exit 1", markerOfPATHRead + `printf '%s\n/x:/y\n' "$m"; exit 1`, "/x:/y", ""},
		{"exit 3 and no output", `exit 3`, "",
			"the login shell ended (exit status 3) without printing a usable PATH (0 bytes on stdout, 0 on stderr)"},
		{"output and exit 5", `echo SECRET123; echo SECRET123 >&2; exit 5`, "",
			"the login shell ended (exit status 5) without printing a usable PATH (10 bytes on stdout, 10 on stderr)"},
		{"clean exit and no output", `exit 0`, "",
			"the login shell ended (exit status 0) without printing a usable PATH (0 bytes on stdout, 0 on stderr)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", writeFakeShell(t, tc.body))
			got, err := extractLoginPATH(context.Background())
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if got != tc.want || msg != tc.wantErr {
				t.Errorf("got %q, %q; want %q, %q", got, msg, tc.want, tc.wantErr)
			}
		})
	}
}

// shrinkShellCaps sets small output caps for one test, so a stub shell prints past
// them with a shell builtin.
func shrinkShellCaps(t *testing.T) {
	t.Helper()
	o1, o2, o3 := shellStdoutCap, shellStderrKeep, shellStderrCap
	shellStdoutCap, shellStderrKeep, shellStderrCap = 128, 32, 128
	t.Cleanup(func() { shellStdoutCap, shellStderrKeep, shellStderrCap = o1, o2, o3 })
}

// TestExtractLoginPATHOutputCaps pins the caps of 52d82f0a (Linux and macOS) on small
// stand-in caps. Too much stdout fails the read with the cut-off text. A full stderr
// does not fail a read whose PATH line comes after it. A test of the suite can leave
// SIGPIPE ignored, and a child then prints a write error on stderr. So the inner
// command of the first case writes its stderr to /dev/null.
func TestExtractLoginPATHOutputCaps(t *testing.T) {
	shrinkShellCaps(t)
	t.Setenv("PATH", "/usr/bin:/bin:/zzz/caps")
	const cut = "the login shell ended (cut off after it had printed more than 1 MiB on stdout or 16 MiB on stderr) without printing a usable PATH "
	for _, tc := range []struct {
		name, body, want, wantErr string
	}{
		{"stdout over its cap", `printf '%0200d' 0; exec /bin/sh -c "$4" 2>/dev/null`, "", cut + "(128 bytes on stdout, 0 on stderr)"},
		{"stderr over its keep", `printf '%080d' 0 >&2; exec /bin/sh -c "$4"`, "/usr/bin:/bin:/zzz/caps", ""},
		{"stderr over its cap, then the PATH", `(printf '%0200d' 0 >&2); exec /bin/sh -c "$4"`, "/usr/bin:/bin:/zzz/caps", ""},
		{"stderr over its cap, no PATH", `printf '%0200d' 0 >&2`, "", cut + "(0 bytes on stdout, 32 on stderr)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", writeFakeShell(t, tc.body))
			got, err := extractLoginPATH(context.Background())
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if got != tc.want || msg != tc.wantErr {
				t.Errorf("got %q, %q; want %q, %q", got, msg, tc.want, tc.wantErr)
			}
		})
	}
}

// TestCappedOutputAtTheRealSizes holds the production caps in memory: 1 MiB of stdout,
// and 1 MiB kept of 16 MiB read on stderr. One byte more cuts the stream.
func TestCappedOutputAtTheRealSizes(t *testing.T) {
	chunk := make([]byte, 32<<10)
	fill := func(c *cappedOutput, n int) error {
		for n > 0 {
			k := min(n, len(chunk))
			if _, err := c.Write(chunk[:k]); err != nil {
				return err
			}
			n -= k
		}
		return nil
	}
	o := newShellOutput()
	if err := fill(&o.stdout, 1<<20); err != nil || o.stdout.cut {
		t.Fatalf("1 MiB of stdout: %v, cut %v", err, o.stdout.cut)
	}
	if err := fill(&o.stdout, 1); !errors.Is(err, errOutputCut) || o.stdout.kept.Len() != 1<<20 {
		t.Errorf("one byte more of stdout: %v, kept %d", err, o.stdout.kept.Len())
	}
	if err := fill(&o.stderr, 16<<20); err != nil || o.stderr.cut || o.stderr.kept.Len() != 1<<20 {
		t.Fatalf("16 MiB of stderr: %v, cut %v, kept %d", err, o.stderr.cut, o.stderr.kept.Len())
	}
	if err := fill(&o.stderr, 1); !errors.Is(err, errOutputCut) {
		t.Errorf("one byte more of stderr: %v, want the cut", err)
	}
	if got := o.counts(); got != "1048576 bytes on stdout, 1048576 on stderr" {
		t.Errorf("counts = %q", got)
	}
}

// TestExtractLoginPATHClosesAHeldPipe pins 52d82f0a (Linux and macOS): a shell that
// prints its PATH and leaves a process on its stdout gives its PATH about 500 ms
// after the shell ends. The holder is this test binary in sleep mode, and it ends by itself.
func TestExtractLoginPATHClosesAHeldPipe(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/zzz/held")
	t.Setenv("SHELL", writeFakeShell(t, `/bin/sh -c "$4"; `+helperInShell(t, "sleep", "5")+` &`))
	start := time.Now()
	got, err := extractLoginPATH(context.Background())
	el := time.Since(start)
	if err != nil || got != "/usr/bin:/bin:/zzz/held" {
		t.Errorf("got %q, %v; want the PATH", got, err)
	}
	if el > 3*time.Second {
		t.Errorf("the read took %v, want it to end after the wait delay of %v", el, loginPATHWaitDelay)
	}
}

// TestExtractLoginPATHDiscardsTheValueOnTimeout: a shell that prints a good line and
// then outlives the time limit gives no PATH and the timed-out text.
func TestExtractLoginPATHDiscardsTheValueOnTimeout(t *testing.T) {
	old := loginPATHTimeout
	loginPATHTimeout = 300 * time.Millisecond
	t.Cleanup(func() { loginPATHTimeout = old })
	shell := writeFakeShell(t, `/bin/sh -c "$4"; `+helperInShell(t, "sleep", "5"))
	t.Setenv("SHELL", shell)
	start := time.Now()
	got, err := extractLoginPATH(context.Background())
	el := time.Since(start)
	if el < loginPATHTimeout || el > 4*time.Second {
		t.Errorf("the read took %v, want the time limit of %v", el, loginPATHTimeout)
	}
	if want := "shell PATH extraction timed out (" + shell + ")"; got != "" || err == nil || err.Error() != want {
		t.Errorf("got %q, %v; want no PATH and %q", got, err, want)
	}
}

// TestLoginPATHTimeLimits holds the bounds of a read. The 30 s limit and the 31 s
// bound are claustrum's own (not measured). The 500 ms after the shell ends is measured.
func TestLoginPATHTimeLimits(t *testing.T) {
	if loginPATHTimeout != 30*time.Second || loginPATHBound != 31*time.Second || loginPATHWaitDelay != 500*time.Millisecond {
		t.Errorf("limits = %v, %v, %v; want 30s, 31s, 500ms", loginPATHTimeout, loginPATHBound, loginPATHWaitDelay)
	}
	if loginPATHSpawnWait != 4*time.Second || loginPATHSpawnHorizon != 8*time.Second || loginPATHFirstPause != 30*time.Second {
		t.Errorf("spawn bounds = %v, %v, %v; want 4s, 8s, 30s", loginPATHSpawnWait, loginPATHSpawnHorizon, loginPATHFirstPause)
	}
}

// groupGone polls until no process of the group pgid is left.
func groupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(-pgid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("a process of the group %d is still there", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startSleepingRead starts a real read whose shell writes its pid and then waits on
// a child in its own group. It returns that pid.
func startSleepingRead(t *testing.T, start func()) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("SHELL", writeFakeShell(t, `echo $$ > '`+pidFile+`'; `+helperInShell(t, "sleep", "30")))
	resetLoginPATHForTest()
	old := loginPATHExtractor
	loginPATHExtractor = extractLoginPATH
	t.Cleanup(func() {
		resetLoginPATHForTest()
		loginPATHExtractor = old
	})
	start()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(pidFile)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			var pid int
			for _, c := range strings.TrimSpace(string(b)) {
				pid = pid*10 + int(c-'0')
			}
			// The group holds only children of this test. A test that fails before the
			// stop leaves no sleeping helper.
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the stub shell wrote no pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestShutdownKillsTheShellGroupOfTheRead pins 52d82f0a: the shutdown ends the shell
// of a running read and every process of its group (Linux and macOS), and no failure
// line follows (Linux). The group holds only children of this test.
func TestShutdownKillsTheShellGroupOfTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	pid := startSleepingRead(t, warmLoginPATHAtStart)
	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("the shell %d leads no process group: %v", pid, err)
	}
	r := runningRead()
	stopLoginPATH()
	groupGone(t, pid)
	awaitReadEnd(t, r)
	if got := shellenvLines(buf); got != "" {
		t.Errorf("the stopped read logged:\n%s", got)
	}
}

// TestFlaggedSpawnKillsTheShellGroupOfTheRead pins 52d82f0a (Linux and macOS): a
// spawn with disableShellAgentSocket ends the shell group of a read that still runs
// after its wait, and the read fails with the timed-out text.
func TestFlaggedSpawnKillsTheShellGroupOfTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	setSpawnWait(t, 100*time.Millisecond)
	pid := startSleepingRead(t, warmLoginPATHAtStart)
	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("the shell %d leads no process group: %v", pid, err)
	}
	r := runningRead()
	buildSpawnEnv(nil, true)
	groupGone(t, pid)
	awaitReadEnd(t, r)
	if got := shellenvLines(buf); !strings.Contains(got, "Stopping the read of the login shell's PATH") ||
		!strings.Contains(got, "shell PATH extraction timed out (") {
		t.Errorf("log has no stop line and timed-out failure:\n%s", got)
	}
}

// TestLoginPATHShellGetsEightEntries pins 52d82f0a (Linux and macOS): the shell of
// the PATH read gets HOME, PATH, SHELL, TERM and USER of the daemon, with an empty
// TERM when the daemon has none, plus three fixed entries. No other variable of the
// daemon reaches it.
func TestLoginPATHShellGetsEightEntries(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "env")
	shell := writeFakeShell(t, "export -p > '"+dump+"'")
	t.Setenv("SHELL", shell)
	t.Setenv("HOME", "/home/row")
	t.Setenv("USER", "row")
	t.Setenv("EFX", "daemon-only")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv(daemonChildMarker, "1")
	t.Setenv("TERM", "x")
	os.Unsetenv("TERM") // t.Setenv puts the old state back

	want := []string{"HOME=/home/row", "PATH=" + os.Getenv("PATH"), "SHELL=" + shell, "TERM=", "USER=row",
		"DISABLE_AUTO_UPDATE=true", "ZSH_DISABLE_COMPFIX=true", "CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1"}
	if got := loginPATHShellEnv(); !slices.Equal(got, want) {
		t.Errorf("loginPATHShellEnv() = %q, want %q", got, want)
	}

	_, _ = extractLoginPATH(context.Background())
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the stub shell wrote no environment: %v", err)
	}
	for _, w := range want {
		name, _, _ := strings.Cut(w, "=")
		if !strings.Contains(string(b), name+"=") {
			t.Errorf("the shell environment has no %s:\n%s", name, b)
		}
	}
	for _, key := range []string{"EFX", "LANG", daemonChildMarker} {
		if strings.Contains(string(b), key+"=") {
			t.Errorf("the shell environment holds %s of the daemon:\n%s", key, b)
		}
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

// stubLoginPATHExtractor makes the read a stub that waits for its stop, for a test
// that boots runServe. The cleanup ends a read that the start left running.
func stubLoginPATHExtractor(t *testing.T) {
	t.Helper()
	old := loginPATHExtractor
	loginPATHExtractor = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() {
		resetLoginPATHForTest()
		loginPATHExtractor = old
	})
}
