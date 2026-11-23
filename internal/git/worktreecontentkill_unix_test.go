//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// TestKilledFileHashHoldsTheReplyForItsStdout: a child of the file hash holds its
// stdout. After the kill the reply waits for that stdout, at most gitDrainWait, and
// the call gets one send (87375cc7, measured on Linux and macOS). Mutation: no
// grace on Linux and macOS (the reply comes at once).
func TestKilledFileHashHoldsTheReplyForItsStdout(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
	dir := t.TempDir()
	self, holder := filepath.Join(dir, "self"), filepath.Join(dir, "holder")
	slowGit(t, "hash-object,--stdin-paths", "holdwait", 10*time.Second, "", "")
	contentTestCallOnly(t)
	t.Setenv("CLAUSTRUM_GITSTUB_WAITMS", "10000")
	t.Setenv("CLAUSTRUM_GITSTUB_HOLDSELF", self)
	t.Setenv("CLAUSTRUM_GITSTUB_HOLDPID", holder)
	t.Cleanup(func() {
		if b, err := os.ReadFile(holder); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	killed := recordHelperKills(t)
	var firedAt atomic.Int64
	firedDeadlineThen(t, func() bool { return exists(self) && exists(holder) }, func() { firedAt.Store(time.Now().UnixNano()) })
	raw, _ := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
	replied := time.Now()
	if !strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
	}
	took := replied.Sub(time.Unix(0, firedAt.Load()))
	if took < gitDrainWait-200*time.Millisecond || took > 8*time.Second {
		t.Errorf("the reply came %v after the kill, want about %v", took, gitDrainWait)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, got := range killed() {
		if got == pid {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d sends to the file hash, want 1", n)
	}
}

// TestFileHashIsReapedAtItsExit: a killed file hash does not wait for the end of
// the check, so its stdout wait can start. Mutation: no once test in runTracked.
func TestFileHashIsReapedAtItsExit(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	recordHelperKills(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitChild$")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_HELPER_EXIT_CHILD=1", "CLAUSTRUM_TEST_HELPER=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	k := newHelperKills()
	release := sync.OnceFunc(k.release)
	defer release()
	tracked := make(chan struct{})
	go func() {
		k.trackOnce(cmd.Process)
		close(tracked)
	}()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		k.mu.Lock()
		n := len(k.helpers)
		k.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(end) {
			_ = cmd.Process.Kill()
			t.Fatal("the helper was not tracked")
		}
	}
	k.fire()
	select {
	case <-tracked:
	case <-time.After(10 * time.Second):
		t.Error("the step of the file hash waited for the end of the check")
		release()
		<-tracked
		_ = cmd.Wait()
		t.FailNow()
	}
	_ = cmd.Wait()
}

// TestHelperKillsRepeatsOnceAfterTheWait: on Linux and macOS the kill repeats once
// after helperRepeatAfter, and the end of the check adds none (87375cc7, measured
// for the repeat). Mutations: no send in the timer, and no repeated test.
func TestHelperKillsRepeatsOnceAfterTheWait(t *testing.T) {
	setRepeatAfter(t, 5*time.Millisecond)
	calls := countHelperKills(t)
	k := newHelperKills()
	k.helpers = []*deadlineHelper{{p: &os.Process{}}}
	k.fire()
	for end := time.Now().Add(10 * time.Second); calls() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("no repeat of the kill")
		}
	}
	k.release()
	if n := calls(); n != 2 {
		t.Errorf("%d kills, want 2", n)
	}
}

// TestKilledHelperIsReapedAtTheEndOfTheCheck: on Linux and macOS a killed helper
// stays unreaped until the end of the check, and the repeat reaches it before its
// reap (87375cc7, measured). Mutations: no hold in track, and a gate that closes
// at the exit.
func TestKilledHelperIsReapedAtTheEndOfTheCheck(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	killed := recordHelperKills(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitChild$")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_HELPER_EXIT_CHILD=1", "CLAUSTRUM_TEST_HELPER=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	k := newHelperKills()
	tracked := make(chan struct{})
	go func() {
		k.track(cmd.Process, nil)
		close(tracked)
	}()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		k.mu.Lock()
		n := len(k.helpers)
		k.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(end) {
			_ = cmd.Process.Kill()
			t.Fatal("the helper was not tracked")
		}
	}
	k.fire()
	for end := time.Now().Add(10 * time.Second); sysproc.SurelyRuns(cmd.Process); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the helper still runs after the kill")
		}
	}
	select {
	case <-tracked:
		t.Error("the reap step did not wait for the end of the check")
	case <-time.After(200 * time.Millisecond):
	}
	k.release()
	if got := killed(); len(got) != 2 || got[0] != cmd.Process.Pid || got[1] != cmd.Process.Pid {
		t.Errorf("kills %v, want two to pid %d", got, cmd.Process.Pid)
	}
	select {
	case <-tracked:
	case <-time.After(10 * time.Second):
		t.Fatal("the reap step did not end after the end of the check")
	}
	_ = cmd.Wait()
}
