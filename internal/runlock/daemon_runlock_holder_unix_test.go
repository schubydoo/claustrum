//go:build unix

package runlock

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestClaimRunDirRefusesAHolderThatIsNotOurServeProcess pins the process check before
// the eviction SIGTERM (holderProcessRefusal). On macOS that check is D15. The holder
// runs on this node, so the record tests pass, and its command line is seamed to "not
// our serve process". The claim then sends no signal and serves without the lock.
//
// SAFETY: the hold is seamed and records each signal. No signal reaches a process,
// also when the check is gone. The holder is a child of this test.
func TestClaimRunDirRefusesAHolderThatIsNotOurServeProcess(t *testing.T) {
	if nodeID() == "" {
		t.Skip("needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, LockName)
	holder := spawnLockHolder(t, lockPath, "", "")

	var (
		mu      sync.Mutex
		sent    []syscall.Signal
		cmdline int
	)
	oc, ohold, ow := isServeCmdline, holdHolder, waitHolderLock
	t.Cleanup(func() { isServeCmdline, holdHolder, waitHolderLock = oc, ohold, ow })
	isServeCmdline = func(int, string) bool {
		mu.Lock()
		defer mu.Unlock()
		cmdline++
		return false
	}
	holdHolder = func(int) (func(syscall.Signal) error, func()) {
		return func(sig syscall.Signal) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, sig)
			return nil
		}, func() {}
	}
	waitHolderLock = func(int, time.Duration) bool { return false }

	buf := captureLogBuf(t)
	claim := LockRunFolder(sock, "serve")
	t.Cleanup(claim.Release)
	t.Cleanup(claim.StopLate)

	mu.Lock()
	defer mu.Unlock()
	if cmdline == 0 {
		t.Fatal("isServeCmdline never ran; the claim refused before the process check")
	}
	if len(sent) != 0 {
		t.Errorf("signals sent to the holder = %v, want none", sent)
	}
	if claim.Holder {
		t.Error("the claim took the lock from a holder that is not our serve process")
	}
	got := buf.String()
	for _, want := range []string{
		fmt.Sprintf("[daemon] serve: not signaling pid %d, the holder of %s (holder is not our serve process for this socket); proceeding without run-dir ownership", holder.Process.Pid, lockPath),
		"[daemon] serve: previous owner of " + dir + ": survivor",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("claim log missing %q\n--- got ---\n%s", want, got)
		}
	}
}
