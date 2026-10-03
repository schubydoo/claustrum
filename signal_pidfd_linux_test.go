//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidfdOf returns the descriptor of this process that is a pid file descriptor of
// pid, or -1 when there is none. The fdinfo of such a descriptor holds a "Pid:" line.
func pidfdOf(t *testing.T, pid int) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fdinfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile("/proc/self/fdinfo/" + e.Name())
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "Pid:" && f[1] == strconv.Itoa(pid) {
				fd, _ := strconv.Atoi(e.Name())
				return fd
			}
		}
	}
	return -1
}

// skipWithoutPidfd skips a test on a kernel that has no pidfd_open call, and in a
// sandbox that refuses the call with EPERM.
func skipWithoutPidfd(t *testing.T) {
	t.Helper()
	fd, err := hcPidfdOpen(os.Getpid())
	if err == syscall.ENOSYS || err == syscall.EPERM {
		t.Skipf("pidfd_open is not usable here: %v", err)
	}
	if err == nil {
		_ = syscall.Close(fd)
	}
}

// TestChildHandleHoldsAPidfd pins what the SIGTERM of process.kill rests on: the
// process handle of a started child holds a pid file descriptor, so the standard
// library sends pidfd_send_signal on it (89cb6289, Linux rows E, A2 and B1). A
// toolchain that stops doing so moves that call back to kill(pid, sig). The helper is
// a child of this test.
func TestChildHandleHoldsAPidfd(t *testing.T) {
	skipWithoutPidfd(t)
	cmd := hcStartSleeper(t)
	if fd := pidfdOf(t, cmd.Process.Pid); fd < 0 {
		t.Fatalf("no pid file descriptor for the child %d in this process", cmd.Process.Pid)
	}
	signalProcessGroup(cmd.Process, "TERM")
	if sig := hcWaitSignal(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("the helper ended with signal %v, want SIGTERM", sig)
	}
}

// TestEvictionSignalGoesThroughAPidfd pins the hold of the lock holder (89cb6289, Linux
// row E3, strace): pidfd_open and then pidfd_send_signal, and no kill. -stop sends
// through realSignalHolder. The serve eviction opens the same hold earlier (see the
// next test). The target is a helper that this test started.
func TestEvictionSignalGoesThroughAPidfd(t *testing.T) {
	skipWithoutPidfd(t)
	cmd := hcStartSleeper(t)
	pid := cmd.Process.Pid

	oldOpen, oldSend, oldKill := hcPidfdOpen, hcPidfdSend, hcKillPid
	t.Cleanup(func() { hcPidfdOpen, hcPidfdSend, hcKillPid = oldOpen, oldSend, oldKill })
	var opened []int
	var sent []syscall.Signal
	kills := 0
	hcPidfdOpen = func(p int) (int, error) { opened = append(opened, p); return oldOpen(p) }
	hcPidfdSend = func(fd int, sig syscall.Signal) error { sent = append(sent, sig); return oldSend(fd, sig) }
	hcKillPid = func(int, syscall.Signal) error { kills++; return nil }

	if !realSignalHolder(pid, syscall.SIGTERM) {
		t.Fatal("realSignalHolder reported no delivery to a live helper")
	}
	if len(opened) != 1 || opened[0] != pid {
		t.Errorf("pidfd_open calls = %v, want one for the helper %d", opened, pid)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGTERM {
		t.Errorf("pidfd_send_signal calls = %v, want one SIGTERM", sent)
	}
	if kills != 0 {
		t.Errorf("kill was called %d times, want 0", kills)
	}
	if sig := hcWaitSignal(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("the helper ended with signal %v, want SIGTERM", sig)
	}
}

// TestEvictionOpensThePidfdBeforeItReadsTheHolder pins row P6 (89cb6289, Linux, strace):
// the eviction opens the pid file descriptor of the holder first, then reads its pid
// namespace and its command line, then sends SIGTERM through the descriptor, with no
// kill. The holder is a helper that this test started.
func TestEvictionOpensThePidfdBeforeItReadsTheHolder(t *testing.T) {
	skipWithoutPidfd(t)
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	cmd := hcStartSleeper(t)
	pid := cmd.Process.Pid

	oldOpen, oldSend, oldKill := hcPidfdOpen, hcPidfdSend, hcKillPid
	oldLink, oldCmdline, oldWait := osReadlink, isServeCmdline, waitHolderLock
	t.Cleanup(func() {
		hcPidfdOpen, hcPidfdSend, hcKillPid = oldOpen, oldSend, oldKill
		osReadlink, isServeCmdline, waitHolderLock = oldLink, oldCmdline, oldWait
	})
	var calls []string
	hcPidfdOpen = func(p int) (int, error) {
		calls = append(calls, "pidfd_open "+strconv.Itoa(p))
		return oldOpen(p)
	}
	osReadlink = func(name string) (string, error) {
		if name == "/proc/"+strconv.Itoa(pid)+"/ns/pid" {
			calls = append(calls, "read ns/pid")
		}
		return oldLink(name)
	}
	// The helper is the test binary, so the read of its command line is seamed.
	isServeCmdline = func(int, string) bool { calls = append(calls, "read cmdline"); return true }
	hcPidfdSend = func(fd int, sig syscall.Signal) error {
		calls = append(calls, "pidfd_send_signal "+sig.String())
		return oldSend(fd, sig)
	}
	hcKillPid = func(int, syscall.Signal) error { calls = append(calls, "kill"); return nil }
	// The helper holds no lock, so the wait on the lock is seamed. Its end is read below.
	waitHolderLock = func(int, time.Duration) bool { return true }

	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, runDirLockName)
	writeRecordFile(t, lockPath, ownerRecord{Pid: pid, Role: "serve", Node: nodeID()})
	if !evictGone(lockPath, filepath.Join(dir, "s.sock")) {
		t.Error("evictRunDirHolder = false, want true")
	}
	want := []string{"pidfd_open " + strconv.Itoa(pid), "read ns/pid", "read cmdline", "pidfd_send_signal terminated"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	if sig := hcWaitSignal(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("the helper ended with signal %v, want SIGTERM", sig)
	}
}
