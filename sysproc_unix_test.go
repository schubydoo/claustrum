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
		// Every expectation is what the REFERENCE at 5db5e4a actually delivered,
		// observed by trapping the signal in the child and printing which one
		// arrived. The reply to process.kill is {"success":true} whatever is sent,
		// so it cannot distinguish signals — an earlier version of this audit
		// "refuted" the divergence by comparing those identical replies.
		{"KILL", syscall.SIGKILL},
		{"SIGKILL", syscall.SIGKILL}, // SIG prefix stripped, then matched
		{"INT", syscall.SIGINT},
		{"SIGINT", syscall.SIGINT},
		{"HUP", syscall.SIGHUP},
		{"SIGHUP", syscall.SIGHUP},
		{"TERM", syscall.SIGTERM},
		{"SIGTERM", syscall.SIGTERM},
		{"", syscall.SIGTERM},      // default
		{"bogus", syscall.SIGTERM}, // unknown → default

		// CASE-SENSITIVE. Lowercase and mixed case are not recognised and fall to
		// the default, on the reference and now here.
		{"kill", syscall.SIGTERM},
		{"int", syscall.SIGTERM},
		{"hup", syscall.SIGTERM},
		{"term", syscall.SIGTERM},
		{"Int", syscall.SIGTERM},
		{"sigkill", syscall.SIGTERM},
		{"sigint", syscall.SIGTERM},

		// There is NO QUIT mapping. claustrum used to send SIGQUIT here, which
		// dumps core by default, where the reference sends SIGTERM.
		{"QUIT", syscall.SIGTERM},
		{"quit", syscall.SIGTERM},
		{"SIGQUIT", syscall.SIGTERM},
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

// SIGKILL must reach the child's whole process group (negative pid), not just
// the leader. We start a shell as a group leader (newSysProcAttr → Setpgid) that
// backgrounds a grandchild sleep in the same group, then signal and assert the
// grandchild dies too. A mutant that drops the negation leaves the grandchild
// alive — which this test catches.
//
// This pins the KILL half of the split only. TestSignalTermSparesTheGroup pins
// the other half: every non-KILL signal must NOT reach the group.
func TestSignalProcessGroupKillsWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	cmd := treeFixture(t, pidFile)
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

// A non-KILL signal must reach the DIRECT CHILD only, leaving the rest of the
// process group running. Measured against the reference at 5db5e4a: spawning
// `sh -c "sleep 40 & …"` and sending process.kill TERM leaves the backgrounded
// grandchild alive there, while claustrum used to kill it along with everything
// else in the group.
//
// The leader must die and the grandchild must survive — asserting only the
// second half would pass for a signal that went nowhere at all.
func TestSignalTermSparesTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	cmd := treeFixture(t, pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	leader := cmd.Process.Pid
	gpid := readPIDFile(t, pidFile)
	// Reap in the background. A terminated-but-unreaped leader is a zombie, and
	// kill(pid, 0) succeeds on a zombie — so polling it the way waitProcessGone
	// does would report the leader as still alive no matter what. Waiting for
	// cmd.Wait to return is the only honest "it died" signal here.
	waited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(waited) }()
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	if err := syscall.Kill(gpid, 0); err != nil {
		t.Fatalf("grandchild %d not alive before signal: %v", gpid, err)
	}

	signalProcessGroup(cmd.Process, "TERM")

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Errorf("leader %d survived SIGTERM — the signal did not reach the direct child", leader)
	}
	// Give the grandchild a real chance to die before concluding it survived.
	// Checking immediately races the reap: once the leader is gone the grandchild
	// is reparented to init and disappears a moment later, so an instant
	// kill(gpid, 0) can still succeed even when the group signal did land.
	if waitProcessGone(gpid, 1500*time.Millisecond) {
		t.Errorf("grandchild %d died on SIGTERM — the signal reached the whole group, "+
			"but the reference spares it", gpid)
	}
}

// treeFixture builds a process-group leader that spawns one grandchild in the
// SAME group and then lingers, writing the grandchild's pid to pidFile. The
// fixture is this test binary in "tree" mode, not a /bin/sh script — AGENTS.md
// requires process fixtures to come from the test binary, and a shell script
// would make these tests depend on the platform shell's job-control semantics
// for whether the backgrounded child even lands in the leader's group.
//
// The "tree" mode waits for one byte on stdin before spawning, so stdin is
// pre-loaded here; the group is set at fork by newSysProcAttr, so there is
// nothing this test needs to sequence behind that gate.
func treeFixture(t *testing.T, pidFile string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "tree")
	cmd := exec.Command(exe, pidFile)
	cmd.Env = buildEnv(env)
	cmd.Stdin = strings.NewReader("g")
	cmd.SysProcAttr = newSysProcAttr()
	return cmd
}

// readPIDFile polls until the file holds a parseable PID (the fixture writes it
// asynchronously, after its grandchild starts).
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
