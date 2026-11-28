//go:build linux

package leftsweep

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// helperEnv names the fixture mode of the test binary.
const helperEnv = "CLAUSTRUM_LEFTSWEEP_HELPER"

// TestMain runs the test binary as a sleeping fixture when helperEnv is set.
func TestMain(m *testing.M) {
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}
	if os.Getenv(helperEnv) == "sleep" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startSleeper starts a sleeping copy of the test binary with env added to its
// environment. An inherited CLAUDE_SSH_ entry is left out, so only env sets the
// marks. The cleanup kills it by its own handle and reaps it.
func startSleeper(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_SSH_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(append(cmd.Env, helperEnv+"=sleep"), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// Two real children of the test on the real /proc: the one with both marks ends
// by SIGKILL, the one with one mark stays. The hold seam passes a real pid file
// descriptor only for these two children. Any other pid gets a send that fails
// the test, so a broken test can signal no other process.
func TestRunEndsARealChildWithBothMarks(t *testing.T) {
	runDir := t.TempDir()
	if err := os.Mkdir(runDir+"/children", 0o755); err != nil {
		t.Fatal(err)
	}
	both := startSleeper(t, runDirMark+runDir, childMark)
	one := startSleeper(t, runDirMark+runDir)
	ours := map[int]bool{both.Process.Pid: true, one.Process.Pid: true}

	// Wait until the environments of both children show the run folder mark.
	deadline := time.Now().Add(10 * time.Second)
	for !hasMark(both.Process.Pid, runDir) || !hasMark(one.Process.Pid, runDir) {
		if time.Now().After(deadline) {
			t.Fatal("the children did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	old, oldWait := holdPID, waitPass
	t.Cleanup(func() { holdPID, waitPass = old, oldWait })
	// The pause before the next pass waits until the killed child shows no
	// environment, so a slow runner cannot see its marks again.
	waitPass = func(time.Duration) {
		end := time.Now().Add(10 * time.Second)
		for time.Now().Before(end) {
			b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", both.Process.Pid))
			if err != nil || len(b) == 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("the killed child still shows its environment")
	}
	holdPID = func(pid int) (func(syscall.Signal) error, func(), error) {
		if ours[pid] {
			return procinfo.HoldPIDByFD(pid)
		}
		guard := func(sig syscall.Signal) error {
			t.Errorf("signal %v to pid %d, which is no child of this test", sig, pid)
			return syscall.EPERM
		}
		return guard, func() {}, nil
	}

	n, err := Run(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Run = %d, want 1", n)
	}
	_ = both.Wait()
	if ws, ok := both.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the child with both marks ended with %v, want SIGKILL", both.ProcessState)
	}
	if err := one.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the child with one mark is gone: %v", err)
	}
}

// hasMark reports whether the environment of pid holds the run folder mark.
func hasMark(pid int, runDir string) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return false
	}
	for _, kv := range splitNUL(b) {
		if kv == runDirMark+runDir {
			return true
		}
	}
	return false
}

func splitNUL(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return out
}
