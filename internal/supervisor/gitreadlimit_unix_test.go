//go:build unix

package supervisor

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestGitReadLimitKillsAGitThatRunsOn: at the limit the pane's git gets SIGTERM,
// and a git that still runs after the wait gets the group SIGKILL. The exit frame
// and the exited line carry the killedBy "limit" (c9125eb8, measured on Linux and
// macOS). The limit and the wait are shortened. The helper prints "ready" once it
// ignores SIGTERM, so its exit frame is seq 2.
func TestGitReadLimitKillsAGitThatRunsOn(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setGitReadLimitForTest(t, 3*time.Second, 300*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p := spawnHelperGitRead(t, m, c, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"gr","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"limit","end":{"at":<n>,"len":109}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals to the git = %q, want TERM then KILL", got)
	}
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] the pane's git has run for 3s; ending it",
		"[process.Manager] Process gr exited with code -1, terminated by SIGKILL, asked to end by this daemon (limit)")
}

// TestGitReadLimitSparesTheGroupOfAGitThatEnds: a git that exits on the SIGTERM of
// the limit gets no group SIGKILL, and a process in its group lives on (c9125eb8,
// measured on Linux and macOS). The window after the exit runs past the wait.
func TestGitReadLimitSparesTheGroupOfAGitThatEnds(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	const grace = 300 * time.Millisecond
	setGitReadLimitForTest(t, 3*time.Second, grace)
	sent := recordSignalsByProc(t)
	pidFile := filepath.Join(t.TempDir(), "kid")
	c, lines := rawLineConn(t)
	p := spawnHelperGitRead(t, m, c, "term-exit0-kid", pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	kid := waitPIDFile(t, pidFile)
	// The test ends its own grandchild only while it can still be alive.
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"gr","stream":"exit","seq":2,"exitCode":0,"killedBy":"limit","end":{"at":<n>,"len":89}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	for end := time.Now().Add(grace + 700*time.Millisecond); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
			t.Fatalf("signals to the git = %q, want TERM alone", got)
		}
		if err := syscall.Kill(kid, 0); err != nil {
			gone = true
			t.Fatalf("the process %d in the group of the git ended: %v", kid, err)
		}
	}
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] the pane's git has run for 3s; ending it",
		"[process.Manager] Process gr exited with code 0, asked to end by this daemon (limit)")
}

// TestGitReadLimitLogsNothingForAReapedGit: a git that exited before the limit,
// while a grandchild still holds its stdout and its exit drain is open, gets no
// limit line, no signal and no killedBy (claustrum's own, not measured). The limit
// is shortened to fall inside the drain.
func TestGitReadLimitLogsNothingForAReapedGit(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setGitReadLimitForTest(t, time.Second, 100*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p := spawnHelperGitRead(t, m, c, "orphan-stdout", "3")
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 20*time.Second))
	if strings.Contains(exit, "killedBy") {
		t.Errorf("exit frame = %s, want no killedBy", exit)
	}
	if got := sent(p.cmd.Process); len(got) != 0 {
		t.Errorf("signals to the reaped git = %q, want none", got)
	}
	if strings.Contains(logs.String(), "the pane's git has run for") {
		t.Errorf("log = %q, want no limit line for a reaped git", logs.String())
	}
}

// TestGitReadLimitGoesOnAfterARefusedTerm: a SIGTERM that the system refuses still
// leads to the group SIGKILL after the wait, and the KILL sets the killedBy "limit"
// (claustrum's own, not measured). The seam refuses the TERM and sends nothing.
func TestGitReadLimitGoesOnAfterARefusedTerm(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setGitReadLimitForTest(t, time.Second, 300*time.Millisecond)
	realSignal := signalGroup
	var mu sync.Mutex
	var names []string
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		names = append(names, signame)
		mu.Unlock()
		if signame == "TERM" {
			return &SignalFailed{Name: "terminated", Err: syscall.EPERM}
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	c, lines := rawLineConn(t)
	spawnHelperGitRead(t, m, c, "sleep", "60")
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 20*time.Second))
	const want = `{"type":"stream","processId":"gr","stream":"exit","seq":1,"exitCode":-1,"signal":"SIGKILL","killedBy":"limit","end":{"at":<n>,"len":109}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(names, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want the refused TERM, then KILL", names)
	}
}
