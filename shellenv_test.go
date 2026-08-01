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

// envValue reads key out of a buildEnv result. Windows spells the variable
// "Path", so the key comparison is case-insensitive.
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
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
