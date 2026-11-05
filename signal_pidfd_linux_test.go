//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/supervisor"
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
	fd, err := procinfo.PidfdOpenForTest()(os.Getpid())
	if err == syscall.ENOSYS || err == syscall.EPERM {
		t.Skipf("pidfd_open is not usable here: %v", err)
	}
	if err == nil {
		_ = syscall.Close(fd)
	}
}

// TestChildHandleHoldsAPidfd pins what the SIGTERM of process.kill rests on: the
// process handle of a started child holds a pid file descriptor, so the standard
// library sends pidfd_send_signal on it (89cb6289, measured on Linux). A toolchain
// that stops doing so moves that call back to kill(pid, sig). The helper is a child
// of this test.
func TestChildHandleHoldsAPidfd(t *testing.T) {
	skipWithoutPidfd(t)
	cmd := hcStartSleeper(t)
	if fd := pidfdOf(t, cmd.Process.Pid); fd < 0 {
		t.Fatalf("no pid file descriptor for the child %d in this process", cmd.Process.Pid)
	}
	_ = supervisor.SignalProcessGroup(cmd.Process, "TERM")
	if sig := hcWaitSignal(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("the helper ended with signal %v, want SIGTERM", sig)
	}
}
