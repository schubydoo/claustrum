//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// These tests are the Windows analogue of sysproc_unix_test.go: they exercise
// the Job Object confinement/teardown in sysproc_windows.go (IMPROVEMENTS #14)
// with a real two-level process tree, so the file's behavior — not just its
// compilation — is pinned in CI (IMPROVEMENTS #20).

func TestNewSysProcAttrNewProcessGroup(t *testing.T) {
	attr := newSysProcAttr()
	if attr == nil {
		t.Fatal("newSysProcAttr returned nil")
	}
	if attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", attr.CreationFlags)
	}
}

func TestDetachSysProcAttrFlags(t *testing.T) {
	attr := detachSysProcAttr()
	if attr == nil {
		t.Fatal("detachSysProcAttr returned nil")
	}
	if attr.CreationFlags&detachedProcess == 0 {
		t.Errorf("CreationFlags = %#x, want DETACHED_PROCESS set", attr.CreationFlags)
	}
	if attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", attr.CreationFlags)
	}
}

// startConfinedTree starts this test binary in "tree" helper mode
// (helperproc_test.go), confines it to a Job Object, and only then releases
// the helper to spawn its grandchild sleeper — mirroring the production order
// (process.spawn confines right after start) and guaranteeing the grandchild
// is born inside the job. Returns the parent cmd, the grandchild PID, and the
// job group.
func startConfinedTree(t *testing.T) (*exec.Cmd, int, *procGroup) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "gpid")
	cmd := exec.Command(exe, pidFile)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "tree"})
	cmd.SysProcAttr = newSysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	group, err := confineProcess(cmd.Process)
	if err != nil {
		t.Fatalf("confineProcess: %v", err)
	}
	// Go-ahead: the helper spawns its grandchild only now, inside the job.
	if _, err := io.WriteString(stdin, "g"); err != nil {
		t.Fatalf("release helper: %v", err)
	}
	gpid := readPIDFile(t, pidFile)
	t.Cleanup(func() {
		group.signal(cmd.Process, "KILL")
		group.close()
		_ = cmd.Wait()
	})
	return cmd, gpid, group
}

// signal must terminate the entire job — the child and its descendants. A
// mutant that breaks TerminateJobObject (or the job==0 guard) leaves the
// grandchild alive, which this catches; the Unix twin is
// TestSignalProcessGroupKillsWholeGroup.
func TestSignalKillsWholeJobTree(t *testing.T) {
	cmd, gpid, group := startConfinedTree(t)

	if !processAlive(t, gpid) {
		t.Fatalf("grandchild %d not alive before signal", gpid)
	}
	group.signal(cmd.Process, "KILL")
	if !waitProcessGone(gpid, 5*time.Second) {
		t.Errorf("grandchild %d survived the job kill — signal hit only the parent, not the job", gpid)
	}
	if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
		t.Errorf("parent %d survived the job kill", cmd.Process.Pid)
	}
}

// close drops the last job handle; with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE set
// at creation, that alone must reap every process still in the job. A mutant
// that drops the LimitFlags leaves the tree running after close.
func TestCloseReapsJobTree(t *testing.T) {
	cmd, gpid, group := startConfinedTree(t)

	if !processAlive(t, gpid) {
		t.Fatalf("grandchild %d not alive before close", gpid)
	}
	group.close()
	if !waitProcessGone(gpid, 5*time.Second) {
		t.Errorf("grandchild %d survived close — KILL_ON_JOB_CLOSE not in effect", gpid)
	}
	if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
		t.Errorf("parent %d survived close", cmd.Process.Pid)
	}
}

// With no job established (zero handle or nil receiver — the confinement
// failure paths), signal must still fall back to killing the parent process.
func TestSignalFallsBackWithoutJob(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *procGroup
	}{
		{"zero job handle", &procGroup{}},
		{"nil receiver", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatalf("os.Executable: %v", err)
			}
			cmd := exec.Command(exe, "60")
			cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
			cmd.SysProcAttr = newSysProcAttr()
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

			tc.group.signal(cmd.Process, "KILL")
			if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
				t.Errorf("parent %d survived — signal did not fall back to proc.Kill", cmd.Process.Pid)
			}
		})
	}
}

// close is idempotent (double-close must not panic or double-release), and a
// closed group reports terminate()==false so signal falls back to the parent
// kill rather than terminating a stale handle.
func TestCloseIdempotentAndTerminateAfterClose(t *testing.T) {
	cmd, _, group := startConfinedTree(t)

	group.close()
	group.close() // second close must be a no-op
	if group.terminate() {
		t.Error("terminate() after close returned true; want false (zero handle)")
	}
	// The fallback path must still tear the parent down (the job close already
	// killed it; signal on the closed group must not panic and must not hang).
	group.signal(cmd.Process, "KILL")
	if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
		t.Errorf("parent %d still alive after close + fallback signal", cmd.Process.Pid)
	}
}

// readPIDFile polls until the file holds a parseable PID (the helper writes it
// asynchronously). Same contract as the Unix twin in sysproc_unix_test.go.
func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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

// stillActive is GetExitCodeProcess's exit code for a live process
// (STILL_ACTIVE = STATUS_PENDING = 259; x/sys/windows exports no constant).
const stillActive = 259

// processAlive reports whether pid is a live process: its exit code (via a
// query-only handle) is still STILL_ACTIVE.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// waitProcessGone polls until the pid no longer names a live process.
func waitProcessGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return true // no such process
		}
		var code uint32
		gone := windows.GetExitCodeProcess(h, &code) != nil || code != stillActive
		windows.CloseHandle(h)
		if gone {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
