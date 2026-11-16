//go:build linux

package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
	"golang.org/x/sys/unix"
)

// leaderReaped reports that the process pid is gone from the children of
// the test: the wait of exec.Cmd reaped it. The probe itself reaps nothing.
func leaderReaped(pid int) bool {
	var info unix.Siginfo
	return unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil) == unix.ECHILD
}

// TestLFSRoomNoKillAfterReap: the poll of the watch reads a low free space
// only after git exited. The reap of git waits for the end of the watch, so
// the poll never sees a reaped leader, and the group kill never comes after
// the reap. A git that exited by itself gets no kill (claustrum's own, from
// the code). The git call ends when the fake free space closes its stdin.
func TestLFSRoomNoKillAfterReap(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "lfs")
	if err := os.MkdirAll(filepath.Join(store, "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = feed.Close() }()
	cmd := exec.Command("git", "hash-object", "--stdin")
	cmd.Dir, cmd.Stdin = dir, in
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	cmd.WaitDelay = time.Second

	var mu sync.Mutex
	reads, lateKills, kills := 0, 0, 0
	reapedInPoll := false
	prevFree, prevReap, prevPoll := storeFreeBytes, reapCheckoutGroup, lfsRoomPoll
	t.Cleanup(func() { storeFreeBytes, reapCheckoutGroup, lfsRoomPoll = prevFree, prevReap, prevPoll })
	lfsRoomPoll = 10 * time.Millisecond
	storeFreeBytes = func(string) (uint64, bool) {
		mu.Lock()
		reads++
		n := reads
		mu.Unlock()
		if n == 1 {
			return 1000, true
		}
		if n == 2 {
			// git exits. The poll then waits up to 500 ms for a reap.
			_ = feed.Close()
			for range 100 {
				if leaderReaped(cmd.Process.Pid) {
					mu.Lock()
					reapedInPoll = true
					mu.Unlock()
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		return 1, true
	}
	reapCheckoutGroup = func(p *os.Process) {
		mu.Lock()
		defer mu.Unlock()
		kills++
		if leaderReaped(p.Pid) {
			lateKills++
		}
	}
	w := newLFSRoomWatch(dir, store)
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, func(error) bool { return false }, w, nil, nil); err != nil {
		t.Fatalf("git hash-object = %v, %s", err, errBuf.String())
	}
	select {
	case <-w.ended:
	default:
		t.Error("the goroutine of the watch runs after the wait, want it ended")
	}
	mu.Lock()
	defer mu.Unlock()
	// A git that exited by itself gets no kill at all (gitStillRuns).
	if reapedInPoll || lateKills != 0 || kills != 0 {
		t.Errorf("the poll saw a reaped leader = %v, kills after the reap = %d of %d, want no kill", reapedInPoll, lateKills, kills)
	}
	if reads < 2 {
		t.Errorf("reads of the free space = %d, want the poll to run", reads)
	}
}
