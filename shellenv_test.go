package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// resetLoginPATHForTest clears any extraction state left by an earlier test.
func resetLoginPATHForTest() {
	loginPATHMu.Lock()
	loginPATHWait = nil
	loginPATHMu.Unlock()
	setLoginPATH("")
}

// envValue reads key out of a buildEnv result the way the CHILD will see it,
// which is not the same as the first matching entry.
//
// buildEnv appends rather than rewrites when the spellings differ: on Windows
// os.Environ() yields "Path=...", so replaceOrAppendEnv's "PATH=" prefix test
// misses and a second, correct "PATH=..." lands at the end. That slice is not
// what reaches the process. exec.Cmd.environ() runs it through dedupEnv, which
// on Windows folds keys case-insensitively and keeps the LAST occurrence
// (os/exec: dedupEnvCase builds its output in reverse "to preserve the last
// occurrence of each key"). The appended entry therefore wins and the child gets
// the value buildEnv intended.
//
// So this must scan backwards. Reading forwards returns the stale pre-append
// entry — a value no child ever sees — and the test then fails on any Windows
// host whose environment block spells the key "Path". CI's windows-latest
// happens to spell it "PATH", which is why that read passed there while failing
// on a stock Windows 11 image.
func envValue(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// TestBuildEnvWaitsForLoginPATH is the W10 regression test. Before the fix,
// extraction ran fire-and-forget and the first spawn built its child env from
// the pre-extraction PATH — so the first spawn in a session (the agent CLI) could
// fail to resolve a binary that every later spawn resolved fine.
func TestBuildEnvWaitsForLoginPATH(t *testing.T) {
	origPATH, hadPATH := os.LookupEnv("PATH")
	origExtractor := loginPATHExtractor
	t.Cleanup(func() {
		loginPATHExtractor = origExtractor
		if hadPATH {
			_ = os.Setenv("PATH", origPATH)
		} else {
			_ = os.Unsetenv("PATH")
		}
		resetLoginPATHForTest()
	})

	_ = os.Setenv("PATH", "/usr/bin:/bin")
	const want = "/home/test/.local/bin:/usr/bin:/bin"

	started := make(chan struct{})
	loginPATHExtractor = func() {
		close(started)
		// Long enough that a non-waiting buildEnv reliably wins the race.
		time.Sleep(100 * time.Millisecond)
		// setLoginPATH, NOT os.Setenv: the extracted PATH is recorded for child
		// environments only and never installed into the daemon's own.
		setLoginPATH(want)
	}

	startLoginPATH()
	<-started // extraction is in flight and has NOT yet set PATH

	got, ok := envValue(buildEnv(nil), "PATH")
	if !ok {
		t.Fatal("buildEnv produced no PATH entry")
	}
	if got != want {
		t.Fatalf("first-spawn PATH = %q, want %q: buildEnv did not wait for login-shell extraction", got, want)
	}
}

// TestAwaitLoginPATHReturnsWhenNeverStarted guards the deadlock this design
// could introduce: newServerOnSocket deliberately does not fork a login shell,
// so every test-booted server reaches buildEnv with no extraction in flight.
func TestAwaitLoginPATHReturnsWhenNeverStarted(t *testing.T) {
	resetLoginPATHForTest()
	done := make(chan struct{})
	go func() {
		awaitLoginPATH()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitLoginPATH blocked with no extraction in flight")
	}
}
