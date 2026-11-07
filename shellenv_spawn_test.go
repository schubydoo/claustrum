package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/shellenv"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// The tests and helpers of the PATH read that need code outside internal/shellenv: supervisor.BuildEnv,
// a spawn or runServe. The other tests of the read are in internal/shellenv.

// resetLoginPATHForTest ends a read that runs, with no log line, and clears the state
// of the PATH read. The clock and the gate go back to production.
func resetLoginPATHForTest() { shellenv.ResetForTest() }

// armLoginPATH lets the reads of the login-shell PATH run.
func armLoginPATH() { shellenv.ArmForTest() }

// loginPATHRun is the read that runs: end is closed when the read has its result.
type loginPATHRun struct {
	end <-chan struct{}
}

// pathAnswer is the result of one fake read.
type pathAnswer struct {
	path string
	err  error
}

// pathReadFake is a scripted extractor with a manual clock and a gate of its own.
type pathReadFake struct {
	mu     sync.Mutex
	now    time.Time
	calls  chan struct{} // one value per started read
	answer chan pathAnswer
	paths  []string // the PATH that the shell of each read got from an adopt, or ""
}

func (f *pathReadFake) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *pathReadFake) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// installPathReadFake arms the read with the fake and resets it at the end. A read
// that a stop ends gives the timed-out error of the real read.
func installPathReadFake(t *testing.T) *pathReadFake {
	t.Helper()
	resetLoginPATHForTest()
	f := &pathReadFake{now: time.Unix(1_000_000, 0), calls: make(chan struct{}, 64), answer: make(chan pathAnswer)}
	old := shellenv.LoginPATHExtractor
	shellenv.LoginPATHExtractor = func(ctx context.Context) (string, error) {
		f.mu.Lock()
		f.paths = append(f.paths, shellenv.ShellPATHForTest(ctx))
		f.mu.Unlock()
		f.calls <- struct{}{}
		select {
		case a := <-f.answer:
			return a.path, a.err
		case <-ctx.Done():
			return "", errors.New("shell PATH extraction timed out (/fake/sh)")
		}
	}
	shellenv.UseClockForTest(f.clock)
	t.Cleanup(func() {
		resetLoginPATHForTest()
		shellenv.LoginPATHExtractor = old
	})
	armLoginPATH()
	return f
}

// lastShellPATH is the PATH that the shell of the last read got from an adopt.
func (f *pathReadFake) lastShellPATH() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) == 0 {
		return ""
	}
	return f.paths[len(f.paths)-1]
}

// setSpawnWait sets the longest spawn wait for one test.
func setSpawnWait(t *testing.T, d time.Duration) {
	t.Helper()
	t.Cleanup(shellenv.SetSpawnWaitForTest(d))
}

// runningRead returns the read that runs, or nil.
func runningRead() *loginPATHRun {
	end := shellenv.RunningReadForTest()
	if end == nil {
		return nil
	}
	return &loginPATHRun{end: end}
}

// awaitReadEnd waits for the read r to have its result.
func awaitReadEnd(t *testing.T, r *loginPATHRun) {
	t.Helper()
	select {
	case <-r.end:
	case <-time.After(10 * time.Second):
		t.Fatal("the read did not end")
	}
}

// startedRead waits for the fake to start a read and returns it.
func startedRead(t *testing.T, f *pathReadFake) *loginPATHRun {
	t.Helper()
	select {
	case <-f.calls:
	case <-time.After(10 * time.Second):
		t.Fatal("no read started")
	}
	r := runningRead()
	if r == nil {
		t.Fatal("a read started, and no read runs")
	}
	return r
}

// noReadStarted fails the test when the fake started a read. A start is
// synchronous with the call that starts it, so the test needs no wait.
func noReadStarted(t *testing.T, f *pathReadFake, when string) {
	t.Helper()
	if runningRead() != nil || len(f.calls) != 0 {
		t.Fatalf("%s: a read started", when)
	}
}

// shellenvLines returns the [shellenv] lines of a log capture, so a line of another
// test does not count.
func shellenvLines(buf *syncBuffer) string {
	var out strings.Builder
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if strings.Contains(line, "[shellenv]") {
			out.WriteString(line)
		}
	}
	return out.String()
}

// envValue reads key out of a supervisor.BuildEnv result the way the CHILD will see it,
// which is not the same as the first matching entry.
//
// supervisor.BuildEnv appends rather than rewrites when the spellings differ: on Windows
// os.Environ() yields "Path=...", so sysproc.ReplaceOrAppendEnv's "PATH=" prefix test
// misses and a second, correct "PATH=..." lands at the end. That slice is not
// what reaches the process. exec.Cmd.environ() runs it through dedupEnv, which
// on Windows folds keys case-insensitively and keeps the LAST occurrence
// (os/exec: dedupEnvCase builds its output in reverse "to preserve the last
// occurrence of each key"). The appended entry therefore wins and the child gets
// the value supervisor.BuildEnv intended.
//
// So this must scan backwards. Reading forwards returns the stale pre-append
// entry — a value no child ever sees — and the test then fails on any Windows
// host whose environment block spells the key "Path". A runner can spell
// it "PATH", and then that read passes there while it fails on a stock Windows 11
// image.
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

	// The Windows shape this helper exists for: a stale "Path" that supervisor.BuildEnv could
	// not rewrite, followed by the "PATH" it appended. dedupEnv keeps the last.
	t.Run("the appended entry wins", func(t *testing.T) {
		if got, _ := envValue([]string{"Path=/stale", "PATH=/appended"}, "PATH"); got != "/appended" {
			t.Errorf("envValue = %q, want %q (dedupEnv keeps the last occurrence)", got, "/appended")
		}
	})
}

// TestLoginPATHSpawnWaitIsBounded pins the spawn wait of 52d82f0a (Linux and macOS):
// at most 4 s, and never past 8 s after the start of the read. A spawn that comes
// before a good read gets the daemon's own PATH. The 8 s horizon is the rule under
// test here, so the spawn wait is long.
func TestLoginPATHSpawnWaitIsBounded(t *testing.T) {
	f := installPathReadFake(t)
	t.Setenv("PATH", "/daemon/bin")
	shellenv.WarmAtStart()
	r := startedRead(t, f)

	setSpawnWait(t, 200*time.Millisecond)
	start := time.Now()
	got, _ := envValue(supervisor.BuildEnv(nil), "PATH")
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Errorf("the spawn returned after %v, before its wait of 200ms", el)
	}
	if got != "/daemon/bin" {
		t.Errorf("PATH during the read = %q, want the daemon PATH", got)
	}

	setSpawnWait(t, 30*time.Second)
	f.advance(shellenv.SpawnHorizonForTest() - 100*time.Millisecond)
	start = time.Now()
	supervisor.BuildEnv(nil)
	if el := time.Since(start); el < 100*time.Millisecond || el > 10*time.Second {
		t.Errorf("the spawn at 100ms before the horizon took %v, want about 100ms", el)
	}
	f.advance(100 * time.Millisecond)
	start = time.Now()
	supervisor.BuildEnv(nil)
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("the spawn at the horizon took %v, want no wait", el)
	}

	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	if got, _ := envValue(supervisor.BuildEnv(nil), "PATH"); got != "/login/bin" {
		t.Errorf("PATH after the read = %q, want /login/bin", got)
	}
	noReadStarted(t, f, "a spawn after a good read")
}

// TestSpawnWithDisableShellAgentSocketStopsTheRead sends the flag through
// process.spawn, so a spawn path that drops it fails here (52d82f0a, Linux and macOS).
func TestSpawnWithDisableShellAgentSocketStopsTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 100*time.Millisecond)
	shellenv.WarmAtStart()
	r := startedRead(t, f)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "flagged", exe, []string{"PATH"}, "", env, true); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitExitFrame(t, frames)
	select {
	case <-r.end:
	default:
		t.Fatal("the flagged spawn left the read running")
	}
	if !strings.Contains(shellenvLines(buf), "Stopping the read of the login shell's PATH") {
		t.Errorf("log has no stop line:\n%s", shellenvLines(buf))
	}
}
