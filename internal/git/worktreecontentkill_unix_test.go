//go:build unix

package git

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

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
