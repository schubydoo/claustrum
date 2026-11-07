package shellenv

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	ResetForTest()
	f := &pathReadFake{now: time.Unix(1_000_000, 0), calls: make(chan struct{}, 64), answer: make(chan pathAnswer)}
	old := LoginPATHExtractor
	LoginPATHExtractor = func(ctx context.Context) (string, error) {
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
		ResetForTest()
		LoginPATHExtractor = old
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
			t.Setenv(NoLoginPATHWarmEnv, "")
			if tc.value == nil {
				_ = os.Unsetenv(NoLoginPATHWarmEnv)
			} else {
				t.Setenv(NoLoginPATHWarmEnv, *tc.value)
			}
			WarmAtStart()
			if _, ok := os.LookupEnv(NoLoginPATHWarmEnv); ok {
				t.Errorf("%s is still in the daemon environment", NoLoginPATHWarmEnv)
			}
			if !tc.warm {
				noReadStarted(t, f, "the start")
				return
			}
			r := startedRead(t, f)
			f.answer <- pathAnswer{path: "/login/bin"}
			awaitReadEnd(t, r)
			if got := CurrentLoginPATH(); got != "/login/bin" {
				t.Errorf("PATH after the read = %q, want /login/bin", got)
			}
		})
	}
}

// TestLoginPATHFailedReadIsTriedAgainLater pins the tries of 52d82f0a (Linux and
// macOS). A later spawn tries again 30 s after the first failure, then after 1m0s
// and 2m0s. A good read is kept, and no read runs after it. The log lines are the
// measured texts.
func TestLoginPATHFailedReadIsTriedAgainLater(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	setSpawnWait(t, 10*time.Second)
	WarmAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("boom 1")}
	awaitReadEnd(t, r)

	for i, pause := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute} {
		f.advance(pause - time.Second)
		AwaitLoginPATH(false)
		noReadStarted(t, f, "a spawn 1 s before the pause ends")
		f.advance(time.Second)
		done := make(chan struct{})
		go func() { AwaitLoginPATH(false); close(done) }()
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
	AwaitLoginPATH(false)
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
		go func() { AwaitLoginPATH(false); close(done) }()
		r := startedRead(t, f)
		f.answer <- pathAnswer{err: errors.New("boom")}
		awaitReadEnd(t, r)
		<-done
		f.advance(loginPATHFirstPause << i)
	}
	AwaitLoginPATH(false)
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
	go func() { AwaitLoginPATH(true); close(done) }()
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
	AwaitLoginPATH(true)
	noReadStarted(t, f, "a flagged spawn after a failure")
	done = make(chan struct{})
	go func() { AwaitLoginPATH(false); close(done) }()
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
	WarmAtStart()
	r := startedRead(t, f)
	Stop()
	awaitReadEnd(t, r)
	if got := shellenvLines(buf); got != "" {
		t.Errorf("the stopped read logged:\n%s", got)
	}
	AwaitLoginPATH(false)
	noReadStarted(t, f, "a spawn after the shutdown")
}

// TestLoginPATHReadNeedsTheGate holds the shared gate from the side of the PATH
// read: while another login shell runs, a spawn starts no read and counts no failure
// (claustrum's own, not measured).
func TestLoginPATHReadNeedsTheGate(t *testing.T) {
	f := installPathReadFake(t)
	loginPATHs.gate <- struct{}{}
	AwaitLoginPATH(false)
	noReadStarted(t, f, "a spawn while the gate is taken")
	if loginPATHs.failures != 0 {
		t.Errorf("failures = %d, want 0", loginPATHs.failures)
	}
	<-loginPATHs.gate
	done := make(chan struct{})
	go func() { AwaitLoginPATH(false); close(done) }()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	<-done
}

// TestAwaitLoginPATHReturnsWhenNeverArmed: newServerOnSocket of package main does not
// arm the read, so an unarmed read starts none at a spawn.
func TestAwaitLoginPATHReturnsWhenNeverArmed(t *testing.T) {
	f := installPathReadFake(t)
	ResetForTest()
	AwaitLoginPATH(false)
	noReadStarted(t, f, "a spawn with no armed read")
}

// TestLoginPATHReadPanicFailsTheRead: a panic in the read fails it and does not end
// the daemon (claustrum's own).
func TestLoginPATHReadPanicFailsTheRead(t *testing.T) {
	buf := captureLogBuf(t)
	installPathReadFake(t)
	LoginPATHExtractor = func(context.Context) (string, error) { panic("boom") }
	WarmAtStart()
	r := runningRead()
	if r == nil {
		t.Fatal("no read started")
	}
	awaitReadEnd(t, r)
	if !strings.Contains(shellenvLines(buf), "(failure 1; next try in 30s at the earliest; until one works, programs get the daemon's own PATH): recovered panic: boom") {
		t.Errorf("log has no failure line for the panic:\n%s", shellenvLines(buf))
	}
}

// goodFirstRead makes the read at the start give path, and waits for its end.
func goodFirstRead(t *testing.T, f *pathReadFake, path string) {
	t.Helper()
	WarmAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: path}
	awaitReadEnd(t, r)
}

// TestLoginPATHReadAgainAfterThePause holds the read again (RefreshLoginPATH). On
// Linux against c9125eb8, no read again starts within seconds of a good read, and one
// starts after 10 min 35 s. Its spawn does not wait and keeps the old PATH. The read
// logs the good line again, and later spawns get its PATH. The pause of 10m0s is
// claustrum's own (not measured). Windows reads no login shell, so it starts none.
func TestLoginPATHReadAgainAfterThePause(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/1")
	if !loginPATHFromShell {
		f.advance(time.Hour)
		RefreshLoginPATH()
		noReadStarted(t, f, "a read again where no login shell runs")
		return
	}
	f.advance(loginPATHRefreshAfter - time.Second)
	RefreshLoginPATH()
	noReadStarted(t, f, "a read again 1 s before the pause ends")

	f.advance(time.Second)
	RefreshLoginPATH() // returns with the read still running: it does not wait
	r := startedRead(t, f)
	if got := CurrentLoginPATH(); got != "/login/1" {
		t.Errorf("PATH while the read again runs = %q, want /login/1", got)
	}
	RefreshLoginPATH()
	if len(f.calls) != 0 {
		t.Error("a second read again started while one runs")
	}
	f.answer <- pathAnswer{path: "/login/two"}
	awaitReadEnd(t, r)
	if got := CurrentLoginPATH(); got != "/login/two" {
		t.Errorf("PATH after the read again = %q, want /login/two", got)
	}
	RefreshLoginPATH()
	noReadStarted(t, f, "a read again right after a read again")

	want := "DEBUG [shellenv] Extracted shell PATH (8 chars) in 0s, after 0 failed read(s)\n" +
		"DEBUG [shellenv] Extracted shell PATH (10 chars) in 0s, after 0 failed read(s)\n"
	if got := shellenvLines(buf); got != want {
		t.Errorf("log =\n%s\nwant\n%s", got, want)
	}
}

// TestLoginPATHReadAgainNeedsAGoodRead: before a good read the read again starts
// nothing, so the tries after a failure keep their pauses (claustrum's own, not
// measured).
func TestLoginPATHReadAgainNeedsAGoodRead(t *testing.T) {
	f := installPathReadFake(t)
	WarmAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("boom")}
	awaitReadEnd(t, r)
	f.advance(loginPATHRefreshAfter)
	RefreshLoginPATH()
	noReadStarted(t, f, "a read again after a failed read only")
}

// TestLoginPATHFailedReadAgainKeepsThePATH: a failed read again keeps the PATH of the
// last good read and counts no failure (claustrum's own, not measured).
func TestLoginPATHFailedReadAgainKeepsThePATH(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/1")
	f.advance(loginPATHRefreshAfter)
	RefreshLoginPATH()
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("boom")}
	awaitReadEnd(t, r)
	if got := CurrentLoginPATH(); got != "/login/1" {
		t.Errorf("PATH after a failed read again = %q, want /login/1", got)
	}
	if loginPATHs.failures != 0 {
		t.Errorf("failures = %d, want 0", loginPATHs.failures)
	}
	want := "WARN  [shellenv] Could not read PATH from the login shell again after 0s: boom; programs keep the PATH of the last good read\n"
	if got := shellenvLines(buf); !strings.HasSuffix(got, want) {
		t.Errorf("log =\n%s\nwant a last line\n%s", got, want)
	}
}
