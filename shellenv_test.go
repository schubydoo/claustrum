package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetLoginPATHForTest ends a read that runs, with no log line, and clears the state
// of the PATH read. The seams go back to production.
func resetLoginPATHForTest() {
	s := loginPATHs
	s.mu.Lock()
	r := s.run
	if r != nil {
		r.quiet = true
	}
	s.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.end
	}
	s.mu.Lock()
	s.armed, s.closed, s.good, s.path = false, false, false, ""
	s.failures, s.nextTry, s.run = 0, time.Time{}, nil
	s.clock, s.gate = time.Now, loginShellGate
	s.mu.Unlock()
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
	old := loginPATHExtractor
	loginPATHExtractor = func(ctx context.Context) (string, error) {
		f.calls <- struct{}{}
		select {
		case a := <-f.answer:
			return a.path, a.err
		case <-ctx.Done():
			return "", errors.New("shell PATH extraction timed out (/fake/sh)")
		}
	}
	loginPATHs.mu.Lock()
	loginPATHs.clock, loginPATHs.gate = f.clock, make(chan struct{}, 1)
	loginPATHs.mu.Unlock()
	t.Cleanup(func() {
		resetLoginPATHForTest()
		loginPATHExtractor = old
	})
	armLoginPATH()
	return f
}

// setSpawnWait sets the longest spawn wait for one test.
func setSpawnWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := loginPATHSpawnWait
	loginPATHSpawnWait = d
	t.Cleanup(func() { loginPATHSpawnWait = old })
}

// runningRead returns the read that runs, or nil.
func runningRead() *loginPATHRun {
	loginPATHs.mu.Lock()
	defer loginPATHs.mu.Unlock()
	return loginPATHs.run
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

	// The Windows shape this helper exists for: a stale "Path" that buildEnv could
	// not rewrite, followed by the "PATH" it appended. dedupEnv keeps the last.
	t.Run("the appended entry wins", func(t *testing.T) {
		if got, _ := envValue([]string{"Path=/stale", "PATH=/appended"}, "PATH"); got != "/appended" {
			t.Errorf("envValue = %q, want %q (dedupEnv keeps the last occurrence)", got, "/appended")
		}
	})
}

// TestLoginPATHReadStartsAtTheDaemonStart pins the start of the read on 52d82f0a
// (Linux and macOS). With no variable and with the value 0 a read starts with no
// spawn. The value 1 starts none. The variable leaves the daemon environment.
func TestLoginPATHReadStartsAtTheDaemonStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *string
		warm  bool
	}{
		{"no variable", nil, true},
		{"value 0", new("0"), true},
		{"value 1", new("1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := installPathReadFake(t)
			t.Setenv(noLoginPATHWarmEnv, "")
			if tc.value == nil {
				_ = os.Unsetenv(noLoginPATHWarmEnv)
			} else {
				t.Setenv(noLoginPATHWarmEnv, *tc.value)
			}
			warmLoginPATHAtStart()
			if _, ok := os.LookupEnv(noLoginPATHWarmEnv); ok {
				t.Errorf("%s is still in the daemon environment", noLoginPATHWarmEnv)
			}
			if !tc.warm {
				noReadStarted(t, f, "the start")
				return
			}
			r := startedRead(t, f)
			f.answer <- pathAnswer{path: "/login/bin"}
			awaitReadEnd(t, r)
			if got := currentLoginPATH(); got != "/login/bin" {
				t.Errorf("PATH after the read = %q, want /login/bin", got)
			}
		})
	}
}

// TestLoginPATHSpawnWaitIsBounded pins the spawn wait of 52d82f0a (Linux and macOS):
// at most 4 s, and never past 8 s after the start of the read. A spawn that comes
// before a good read gets the daemon's own PATH. The 8 s horizon is the rule under
// test here, so the spawn wait is long.
func TestLoginPATHSpawnWaitIsBounded(t *testing.T) {
	f := installPathReadFake(t)
	t.Setenv("PATH", "/daemon/bin")
	warmLoginPATHAtStart()
	r := startedRead(t, f)

	setSpawnWait(t, 200*time.Millisecond)
	start := time.Now()
	got, _ := envValue(buildEnv(nil), "PATH")
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Errorf("the spawn returned after %v, before its wait of 200ms", el)
	}
	if got != "/daemon/bin" {
		t.Errorf("PATH during the read = %q, want the daemon PATH", got)
	}

	setSpawnWait(t, 30*time.Second)
	f.advance(loginPATHSpawnHorizon - 100*time.Millisecond)
	start = time.Now()
	buildEnv(nil)
	if el := time.Since(start); el < 100*time.Millisecond || el > 10*time.Second {
		t.Errorf("the spawn at 100ms before the horizon took %v, want about 100ms", el)
	}
	f.advance(100 * time.Millisecond)
	start = time.Now()
	buildEnv(nil)
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("the spawn at the horizon took %v, want no wait", el)
	}

	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	if got, _ := envValue(buildEnv(nil), "PATH"); got != "/login/bin" {
		t.Errorf("PATH after the read = %q, want /login/bin", got)
	}
	noReadStarted(t, f, "a spawn after a good read")
}

// TestLoginPATHFailedReadIsTriedAgainLater pins the tries of 52d82f0a (Linux and
// macOS). A later spawn tries again 30 s after the first failure, then after 1m0s
// and 2m0s. A good read is kept, and no read runs after it. The log lines are the
// measured texts.
func TestLoginPATHFailedReadIsTriedAgainLater(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 10*time.Second)
	warmLoginPATHAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("boom 1")}
	awaitReadEnd(t, r)

	for i, pause := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute} {
		f.advance(pause - time.Second)
		buildEnv(nil)
		noReadStarted(t, f, "a spawn 1 s before the pause ends")
		f.advance(time.Second)
		done := make(chan struct{})
		go func() { buildEnv(nil); close(done) }()
		r := startedRead(t, f)
		if i < 2 {
			f.answer <- pathAnswer{err: errors.New("boom " + string(rune('2'+i)))}
		} else {
			f.answer <- pathAnswer{path: "/login/bin"}
		}
		awaitReadEnd(t, r)
		<-done
	}
	f.advance(time.Hour)
	buildEnv(nil)
	noReadStarted(t, f, "a spawn after a good read")

	want := "WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 1; next try in 30s at the earliest; until one works, programs get the daemon's own PATH): boom 1\n" +
		"WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 2; next try in 1m0s at the earliest; until one works, programs get the daemon's own PATH): boom 2\n" +
		"WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 3; next try in 2m0s at the earliest; until one works, programs get the daemon's own PATH): boom 3\n"
	// Windows reads no login shell, so it logs no success line.
	if loginPATHFromShell {
		want += "DEBUG [shellenv] Extracted shell PATH (10 chars) in 0s, after 3 failed read(s)\n"
	}
	if got := shellenvLines(buf); got != want {
		t.Errorf("log =\n%s\nwant\n%s", got, want)
	}
}

// TestLoginPATHStopsAfterSixteenFailures holds the cap of 16 failed reads
// (claustrum's own, not measured).
func TestLoginPATHStopsAfterSixteenFailures(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 10*time.Second)
	for i := range loginPATHMaxFailures {
		done := make(chan struct{})
		go func() { buildEnv(nil); close(done) }()
		r := startedRead(t, f)
		f.answer <- pathAnswer{err: errors.New("boom")}
		awaitReadEnd(t, r)
		<-done
		f.advance(loginPATHFirstPause << i)
	}
	buildEnv(nil)
	noReadStarted(t, f, "a spawn after 16 failures")
	if want := "(failure 16; no further try; programs get the daemon's own PATH): boom\n"; !strings.HasSuffix(shellenvLines(buf), want) {
		t.Errorf("last line of the log is not %q:\n%s", want, shellenvLines(buf))
	}
}

// TestLoginPATHFlaggedSpawnStopsTheRead pins 52d82f0a (Linux and macOS): a spawn with
// disableShellAgentSocket ends a read that still runs after its wait. The stop line
// comes first, then the timed-out failure. After that failure such a spawn starts no
// other read, and a plain spawn does.
func TestLoginPATHFlaggedSpawnStopsTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 100*time.Millisecond)
	done := make(chan struct{})
	go func() { buildSpawnEnv(nil, true); close(done) }()
	r := startedRead(t, f)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the flagged spawn did not return")
	}
	awaitReadEnd(t, r)
	want := "INFO  [shellenv] Stopping the read of the login shell's PATH at the desktop's word (disableShellAgentSocket); spawns that carry it start no other after a failure\n" +
		"WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 1; next try in 30s at the earliest; until one works, programs get the daemon's own PATH): shell PATH extraction timed out (/fake/sh)\n"
	if got := shellenvLines(buf); got != want {
		t.Errorf("log =\n%s\nwant\n%s", got, want)
	}

	f.advance(loginPATHFirstPause)
	buildSpawnEnv(nil, true)
	noReadStarted(t, f, "a flagged spawn after a failure")
	done = make(chan struct{})
	go func() { buildSpawnEnv(nil, false); close(done) }()
	r = startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	<-done
}

// TestLoginPATHShutdownStopsTheReadQuietly pins 52d82f0a: the shutdown ends a running
// read (Linux and macOS), and no failure line follows (Linux). No read starts after it.
func TestLoginPATHShutdownStopsTheReadQuietly(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	warmLoginPATHAtStart()
	r := startedRead(t, f)
	stopLoginPATH()
	awaitReadEnd(t, r)
	if got := shellenvLines(buf); got != "" {
		t.Errorf("the stopped read logged:\n%s", got)
	}
	buildEnv(nil)
	noReadStarted(t, f, "a spawn after the shutdown")
}

// TestLoginPATHReadNeedsTheGate holds the shared gate from the side of the PATH
// read: while another login shell runs, a spawn starts no read and counts no failure
// (claustrum's own, not measured).
func TestLoginPATHReadNeedsTheGate(t *testing.T) {
	f := installPathReadFake(t)
	loginPATHs.gate <- struct{}{}
	buildEnv(nil)
	noReadStarted(t, f, "a spawn while the gate is taken")
	if loginPATHs.failures != 0 {
		t.Errorf("failures = %d, want 0", loginPATHs.failures)
	}
	<-loginPATHs.gate
	done := make(chan struct{})
	go func() { buildEnv(nil); close(done) }()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	<-done
}

// TestAwaitLoginPATHReturnsWhenNeverArmed guards the test servers: newServerOnSocket
// does not arm the read, so a test spawn starts no login shell.
func TestAwaitLoginPATHReturnsWhenNeverArmed(t *testing.T) {
	f := installPathReadFake(t)
	resetLoginPATHForTest()
	buildEnv(nil)
	noReadStarted(t, f, "a spawn with no armed read")
}

// TestSpawnWithDisableShellAgentSocketStopsTheRead sends the flag through
// process.spawn, so a spawn path that drops it fails here (52d82f0a, Linux and macOS).
func TestSpawnWithDisableShellAgentSocketStopsTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 100*time.Millisecond)
	warmLoginPATHAtStart()
	r := startedRead(t, f)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "printenv")
	if _, err := m.spawn(c, "flagged", exe, []string{"PATH"}, "", env, true); err != nil {
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

// TestLoginPATHReadPanicFailsTheRead: a panic in the read fails it and does not end
// the daemon (claustrum's own).
func TestLoginPATHReadPanicFailsTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	installPathReadFake(t)
	loginPATHExtractor = func(context.Context) (string, error) { panic("boom") }
	warmLoginPATHAtStart()
	r := runningRead()
	if r == nil {
		t.Fatal("no read started")
	}
	awaitReadEnd(t, r)
	if !strings.Contains(shellenvLines(buf), "(failure 1; next try in 30s at the earliest; until one works, programs get the daemon's own PATH): recovered panic: boom") {
		t.Errorf("log has no failure line for the panic:\n%s", shellenvLines(buf))
	}
}
