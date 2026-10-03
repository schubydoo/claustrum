package main

import (
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// resetLoginPATHForTest clears any extraction state left by an earlier test.
func resetLoginPATHForTest() {
	loginPATHMu.Lock()
	loginPATHOnce = nil
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
//
// The case folding is deliberately NOT unconditional. dedupEnv folds only on
// Windows, so on Unix "PATH" and "Path" are two independent variables and the
// child receives both. Matching case-insensitively everywhere would let a
// stray "Path" in the ambient environment shadow the real "PATH" and make this
// test assert a value the child never uses. Mirroring dedupEnvCase's own
// predicate keeps the reader honest on every OS.
func envValue(env []string, key string) (string, bool) {
	fold := runtime.GOOS == "windows"
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if !ok {
			continue
		}
		if k == key || (fold && strings.EqualFold(k, key)) {
			return v, true
		}
	}
	return "", false
}

// envValue encodes a per-OS rule and has been wrong in both directions: it read
// forwards (returning an entry dedupEnv discards), and folding case everywhere
// would let an unrelated Unix variable shadow the real one. These pin both ends
// so the next edit cannot quietly reintroduce either.
func TestEnvValueMatchesDedupEnvSemantics(t *testing.T) {
	// A real PATH followed by a later, differently-cased Path. On Unix these are
	// two independent variables and the child's $PATH is the "PATH" entry; on
	// Windows they are one variable and dedupEnv keeps the last.
	t.Run("case folding follows the OS", func(t *testing.T) {
		env := []string{"HOME=/h", "PATH=/real", "OTHER=x", "Path=/stray"}
		want := "/real"
		if runtime.GOOS == "windows" {
			want = "/stray"
		}
		got, ok := envValue(env, "PATH")
		if !ok {
			t.Fatal("envValue found no PATH")
		}
		if got != want {
			t.Errorf("envValue = %q, want %q on %s", got, want, runtime.GOOS)
		}
	})

	// The Windows shape this helper exists for: a stale "Path" that buildEnv could
	// not rewrite, followed by the "PATH" it appended. dedupEnv keeps the last.
	t.Run("the appended entry wins", func(t *testing.T) {
		if got, _ := envValue([]string{"Path=/stale", "PATH=/appended"}, "PATH"); got != "/appended" {
			t.Errorf("envValue = %q, want %q (dedupEnv keeps the last occurrence)", got, "/appended")
		}
	})
}

// TestLoginPATHIsReadAtTheFirstSpawnOnly pins when the login-shell PATH is read
// (89cb6289, Linux and macOS rows G1 to G3). Arming the read runs no shell. The first
// build of a child environment runs it and uses its result. A second build that
// comes while the read runs waits for it, so no child gets the PATH from before the
// read. No later build runs it again.
func TestLoginPATHIsReadAtTheFirstSpawnOnly(t *testing.T) {
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
	resetLoginPATHForTest()

	_ = os.Setenv("PATH", "/usr/bin:/bin")
	const want = "/home/test/.local/bin:/usr/bin:/bin"

	var runs atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	loginPATHExtractor = func() {
		if runs.Add(1) == 1 {
			close(started)
		}
		<-release
		// setLoginPATH, NOT os.Setenv: the extracted PATH is recorded for child
		// environments only and never installed into the daemon's own.
		setLoginPATH(want)
	}

	armLoginPATH()
	select {
	case <-started:
		t.Fatal("arming the read ran the login shell (row G1: no shell before the first spawn)")
	case <-time.After(50 * time.Millisecond):
	}

	// Two builds at once: the first runs the read, the second waits for it.
	paths := make(chan string, 2)
	for range 2 {
		go func() {
			got, _ := envValue(buildEnv(nil), "PATH")
			paths <- got
		}()
	}
	<-started
	select {
	case got := <-paths:
		t.Fatalf("a build returned PATH %q while the read still ran", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if got := <-paths; got != want {
			t.Errorf("PATH of a first build = %q, want %q", got, want)
		}
	}

	if got, _ := envValue(buildEnv(nil), "PATH"); got != want {
		t.Errorf("PATH of a later build = %q, want %q", got, want)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("the read ran %d times, want 1 (row G2: the second spawn runs no shell)", n)
	}
}

// TestLoginPATHFailureIsKept pins row G3: a read that gives no PATH is not tried
// again by a later spawn.
func TestLoginPATHFailureIsKept(t *testing.T) {
	origExtractor := loginPATHExtractor
	t.Cleanup(func() {
		loginPATHExtractor = origExtractor
		resetLoginPATHForTest()
	})
	resetLoginPATHForTest()
	var runs atomic.Int32
	loginPATHExtractor = func() { runs.Add(1) } // a failed read sets no PATH
	armLoginPATH()
	for range 3 {
		buildEnv(nil)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("a failed read ran %d times in three builds, want 1", n)
	}
}

// TestAwaitLoginPATHReturnsWhenNeverStarted guards the test servers:
// newServerOnSocket does not arm the read, so every test-booted server reaches
// buildEnv with no read to run, and no login shell starts.
func TestAwaitLoginPATHReturnsWhenNeverStarted(t *testing.T) {
	origExtractor := loginPATHExtractor
	t.Cleanup(func() { loginPATHExtractor = origExtractor })
	var runs atomic.Int32
	loginPATHExtractor = func() { runs.Add(1) }
	resetLoginPATHForTest()
	done := make(chan struct{})
	go func() {
		awaitLoginPATH()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitLoginPATH blocked with no read armed")
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("a read that nothing armed ran %d times", n)
	}
}
