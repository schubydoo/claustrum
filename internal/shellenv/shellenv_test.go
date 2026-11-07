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
	ResetForTest()
	f := &pathReadFake{now: time.Unix(1_000_000, 0), calls: make(chan struct{}, 64), answer: make(chan pathAnswer)}
	old := LoginPATHExtractor
	LoginPATHExtractor = func(ctx context.Context) (string, error) {
		p, _ := ctx.Value(shellPATHKey{}).(string)
		f.mu.Lock()
		f.paths = append(f.paths, p)
		f.mu.Unlock()
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

// againAfterAdopt notes a good adopt of path, moves the clock by d and runs a spawn.
func againAfterAdopt(f *pathReadFake, path string, d time.Duration) {
	NoteAdoptedPATH(path)
	f.advance(d)
	RefreshLoginPATH()
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

// TestLoginPATHReadAgainAfterAnAdopt holds the read again (RefreshLoginPATH). On
// Linux and macOS against c9125eb8, after a good adopt a spawn 31 s after the last
// read starts none and one 61 s after it starts one. That spawn does not wait and
// keeps the old PATH. On Linux the shell gets the PATH of the adopt. The read logs
// the good line, and later spawns get its PATH. That a spawn with no new adopt
// starts no other read, and the exact 60 s, are claustrum's own (not measured).
// Windows reads no login shell, so it starts none.
func TestLoginPATHReadAgainAfterAnAdopt(t *testing.T) {
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/1")
	if !loginPATHFromShell {
		againAfterAdopt(f, "/p2", time.Hour)
		noReadStarted(t, f, "a read again where no login shell runs")
		return
	}
	againAfterAdopt(f, "/p2", loginPATHRefreshAfter-time.Second)
	noReadStarted(t, f, "a spawn 1 s before the floor ends")

	f.advance(time.Second)
	RefreshLoginPATH() // returns with the read still running: it does not wait
	r := startedRead(t, f)
	if got := f.lastShellPATH(); got != "/p2" {
		t.Errorf("the shell of the read again got PATH %q, want /p2", got)
	}
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
	f.advance(time.Hour)
	RefreshLoginPATH()
	noReadStarted(t, f, "a spawn with no new adopt")

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
	againAfterAdopt(f, "/p2", time.Hour)
	noReadStarted(t, f, "a read again after a failed read only")
}

// TestLoginPATHFailedReadAgain: on Linux against c9125eb8, a failed read again logs
// the failure line with the note of a failure after a good read, and the spawns keep
// the old PATH. After a later adopt the next good read logs "after 1 failed read(s)".
// That the good read clears the count is claustrum's own (not measured).
func TestLoginPATHFailedReadAgain(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/0")
	againAfterAdopt(f, "/p2", 71*time.Second)
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("the login shell ended (exit status 1) without printing a usable PATH (105 bytes on stdout, 112 on stderr)")}
	awaitReadEnd(t, r)
	if got := CurrentLoginPATH(); got != "/login/0" {
		t.Errorf("PATH after a failed read again = %q, want /login/0", got)
	}
	f.advance(3 * time.Second)
	RefreshLoginPATH()
	noReadStarted(t, f, "a spawn 3 s after the failed read again")

	againAfterAdopt(f, "/p5", 70*time.Second)
	r = startedRead(t, f)
	if got := f.lastShellPATH(); got != "/p5" {
		t.Errorf("the shell of the next read got PATH %q, want /p5", got)
	}
	f.answer <- pathAnswer{path: "/login/3"}
	awaitReadEnd(t, r)
	want := "WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 1; next try in 30s at the earliest; " +
		"until one works, programs keep the last PATH that was read, or sshd's if none was): " +
		"the login shell ended (exit status 1) without printing a usable PATH (105 bytes on stdout, 112 on stderr)\n" +
		"DEBUG [shellenv] Extracted shell PATH (8 chars) in 0s, after 1 failed read(s)\n"
	if got := shellenvLines(buf); !strings.HasSuffix(got, want) {
		t.Errorf("log =\n%s\nwant the last lines\n%s", got, want)
	}
	if got := CurrentLoginPATH(); got != "/login/3" {
		t.Errorf("PATH after the good read = %q, want /login/3", got)
	}
	if loginPATHs.failures != 0 {
		t.Errorf("failures after the good read = %d, want 0", loginPATHs.failures)
	}
}

// TestLoginPATHFailedReadAgainTriesAgain: after a failed read again, a spawn with no
// new adopt tries again once the pause and the floor end (claustrum's own, not
// measured).
func TestLoginPATHFailedReadAgainTriesAgain(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/0")
	againAfterAdopt(f, "/p2", 61*time.Second)
	r := startedRead(t, f)
	f.answer <- pathAnswer{err: errors.New("boom")}
	awaitReadEnd(t, r)
	f.advance(61 * time.Second)
	RefreshLoginPATH()
	r = startedRead(t, f)
	if got := f.lastShellPATH(); got != "/p2" {
		t.Errorf("the shell of the try again got PATH %q, want /p2", got)
	}
	f.answer <- pathAnswer{path: "/login/2"}
	awaitReadEnd(t, r)
}

// TestLoginPATHReadAgainCap: on Linux against c9125eb8, 24 reads again, each after
// an adopt 61 s after the last read, then the line of the cap after the good line of
// the 24th. That no 25th read follows, the window of 24 h from the start of the first
// read, and a new window with its own cap from the first read after it, are
// claustrum's own (not measured).
func TestLoginPATHReadAgainCap(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	start := f.clock()
	goodFirstRead(t, f, "/login/0")
	for range loginPATHAgainCap {
		againAfterAdopt(f, "/g", 61*time.Second)
		r := startedRead(t, f)
		f.answer <- pathAnswer{path: "/login/n"}
		awaitReadEnd(t, r)
	}
	againAfterAdopt(f, "/g", 61*time.Second)
	noReadStarted(t, f, "the 25th read again")

	capLine := "INFO  [shellenv] Since 1970-01-12T13:46:40Z the login shell's PATH has been read again 24 times, " +
		"each time because a new connection sent its environment. It is not read again before " +
		"1970-01-13T13:46:40Z; until then programs keep the PATH just read\n"
	want := "DEBUG [shellenv] Extracted shell PATH (8 chars) in 0s, after 0 failed read(s)\n" + capLine
	got := shellenvLines(buf)
	if !strings.HasSuffix(got, want) || strings.Count(got, "[shellenv] Since ") != 1 {
		t.Errorf("log =\n%s\nwant one cap line at the end:\n%s", got, want)
	}

	// The read at the end of the window opens a new window, which has a cap of its own.
	f.advance(start.Add(loginPATHAgainWindow).Sub(f.clock()))
	RefreshLoginPATH()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/m"}
	awaitReadEnd(t, r)
	for range loginPATHAgainCap - 1 {
		againAfterAdopt(f, "/g", 61*time.Second)
		r := startedRead(t, f)
		f.answer <- pathAnswer{path: "/login/n"}
		awaitReadEnd(t, r)
	}
	againAfterAdopt(f, "/g", 61*time.Second)
	noReadStarted(t, f, "the 25th read again of the second window")
	secondCap := "INFO  [shellenv] Since 1970-01-13T13:46:40Z the login shell's PATH has been read again 24 times, " +
		"each time because a new connection sent its environment. It is not read again before " +
		"1970-01-14T13:46:40Z; until then programs keep the PATH just read\n"
	if got := shellenvLines(buf); !strings.HasSuffix(got, secondCap) || strings.Count(got, "[shellenv] Since ") != 2 {
		t.Errorf("log =\n%s\nwant a second cap line at the end:\n%s", got, secondCap)
	}
}

// TestLoginPATHFailedReadsAgainStop: after a failed read again the pause doubles,
// so a spawn 61 s after the third failure starts no read. After 16 failures no read
// starts again, and the note says so. All of it is claustrum's own (not measured).
func TestLoginPATHFailedReadsAgainStop(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	goodFirstRead(t, f, "/login/0")
	NoteAdoptedPATH("/p")
	wait := loginPATHRefreshAfter
	for n := 1; n <= loginPATHMaxFailures; n++ {
		f.advance(wait)
		RefreshLoginPATH()
		r := startedRead(t, f)
		f.answer <- pathAnswer{err: errors.New("boom")}
		awaitReadEnd(t, r)
		wait = max(loginPATHRefreshAfter, loginPATHFirstPause<<(n-1))
		if n == 3 {
			f.advance(61 * time.Second)
			RefreshLoginPATH()
			noReadStarted(t, f, "a spawn 61 s after the third failure")
			wait -= 61 * time.Second
		}
	}
	f.advance(1000 * time.Hour)
	RefreshLoginPATH()
	noReadStarted(t, f, "a spawn after 16 failed reads again")
	want := "WARN  [shellenv] Could not read PATH from the login shell after 0s (failure 16; no further try; " +
		"programs keep the last PATH that was read): boom\n"
	if got := shellenvLines(buf); !strings.HasSuffix(got, want) {
		t.Errorf("log =\n%s\nwant a last line\n%s", got, want)
	}
}

// TestLoginPATHReadAgainWindowKeepsARunningRead: a read of the old window that still
// runs at its end counts in the old window, and a spawn meanwhile opens no new
// window. That read is the 24th, and it ends after the window, so it logs no cap
// line (claustrum's own, not measured).
func TestLoginPATHReadAgainWindowKeepsARunningRead(t *testing.T) {
	if !loginPATHFromShell {
		t.Skip("no login shell runs here, so no read again starts")
	}
	buf := captureLogBuf(t)
	f := installPathReadFake(t)
	start := f.clock()
	goodFirstRead(t, f, "/login/0")
	for range loginPATHAgainCap - 1 {
		againAfterAdopt(f, "/g", 61*time.Second)
		r := startedRead(t, f)
		f.answer <- pathAnswer{path: "/login/n"}
		awaitReadEnd(t, r)
	}
	againAfterAdopt(f, "/g", start.Add(loginPATHAgainWindow-time.Second).Sub(f.clock()))
	r := startedRead(t, f)
	againAfterAdopt(f, "/g", 61*time.Second) // past the end of the window, while the read runs
	f.answer <- pathAnswer{path: "/login/n"}
	awaitReadEnd(t, r)
	if loginPATHs.againCount != loginPATHAgainCap || !loginPATHs.againSince.Equal(start) {
		t.Errorf("window = %d reads since %v, want %d since %v", loginPATHs.againCount, loginPATHs.againSince, loginPATHAgainCap, start)
	}
	if got := shellenvLines(buf); strings.Contains(got, "[shellenv] Since ") {
		t.Errorf("log =\n%s\nwant no cap line for a read that ends after its window", got)
	}
}
