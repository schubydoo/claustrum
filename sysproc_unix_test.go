//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseSignal(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
	}{
		{"KILL", syscall.SIGKILL},
		{"kill", syscall.SIGKILL},    // ToUpper handles case for the bare name
		{"SIGKILL", syscall.SIGKILL}, // SIG prefix stripped (case-sensitive), then matched
		{"sigkill", syscall.SIGTERM}, // quirk: TrimPrefix is case-sensitive, so lowercase "sig" isn't stripped → default
		{"INT", syscall.SIGINT},
		{"HUP", syscall.SIGHUP},
		{"QUIT", syscall.SIGQUIT},
		{"TERM", syscall.SIGTERM},
		{"", syscall.SIGTERM},      // default
		{"bogus", syscall.SIGTERM}, // unknown → default
	}
	for _, tc := range cases {
		if got := parseSignal(tc.in); got != tc.want {
			t.Errorf("parseSignal(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDetachSysProcAttr(t *testing.T) {
	if detachSysProcAttr() == nil {
		t.Error("detachSysProcAttr should return a non-nil SysProcAttr")
	}
}

// signalProcessGroup must signal the child's whole process group (negative pid),
// not just the leader. We start a shell as a group leader (newSysProcAttr →
// Setpgid) that backgrounds a grandchild sleep in the same group, then signal the
// group and assert the grandchild dies too. The reference behavior is group-wide;
// a mutant that drops the negation (signalling only +pid, the leader) leaves the
// grandchild alive — which this test catches.
func TestSignalProcessGroupKillsWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	// Non-interactive sh has no job control, so the backgrounded sleep stays in
	// the shell's process group. Record its PID, then wait so the leader stays
	// alive until we signal it.
	script := "sleep 60 & echo $! > " + pidFile + "; wait"
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = newSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { signalProcessGroup(cmd.Process, "KILL"); _ = cmd.Wait() })

	gpid := readPIDFile(t, pidFile)
	if err := syscall.Kill(gpid, 0); err != nil {
		t.Fatalf("grandchild %d not alive before signal: %v", gpid, err)
	}

	signalProcessGroup(cmd.Process, "KILL")

	if !waitProcessGone(gpid, 3*time.Second) {
		t.Errorf("grandchild %d survived the group kill — signalProcessGroup hit the leader, not the group", gpid)
	}
}

// readPIDFile polls until the file holds a parseable PID (the backgrounding shell
// writes it asynchronously).
func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				if pid, err := strconv.Atoi(s); err == nil {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild pid file never written: %s", path)
	return 0
}

// waitProcessGone polls signal-0 until the pid no longer exists (ESRCH).
func waitProcessGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true // ESRCH: the process is gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
