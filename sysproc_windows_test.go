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
// the kill handle in sysproc_windows.go
// with a real two-level process tree. CI therefore pins the behavior of the file and not
// only its compilation (IMPROVEMENTS item 20). A kill ends the direct child only,
// so every test ends the grandchild that it started by its recorded pid.

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
	if attr.CreationFlags&createBreakawayFromJob == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_BREAKAWAY_FROM_JOB set", attr.CreationFlags)
	}
}

// startConfinedTree starts this test binary in "tree" helper mode
// (helperproc_test.go), takes its kill handle, and only then releases
// the helper to spawn its grandchild sleeper, mirroring the production order
// (process.spawn takes the handle right after start). Returns the parent cmd, the
// grandchild PID, and the group. The cleanup ends the parent and the grandchild, each by
// its recorded pid, also after a failed assertion.
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
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	group, err := confineProcess(cmd.Process)
	if err != nil {
		t.Fatalf("confineProcess: %v", err)
	}
	t.Cleanup(group.close)
	// Go-ahead: the helper spawns its grandchild only now.
	if _, err := io.WriteString(stdin, "g"); err != nil {
		t.Fatalf("release helper: %v", err)
	}
	gpid := readPIDFile(t, pidFile)
	t.Cleanup(func() { killOwnPid(gpid) })
	return cmd, gpid, group
}

// killOwnPid ends a process that this test started, by its pid. It is a no-op for a pid
// that is gone.
func killOwnPid(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	_ = windows.TerminateProcess(h, 1)
	_ = windows.CloseHandle(h)
}

// signal ends the direct child only. Its grandchild lives on. Measured on a Windows VM
// against 89cb6289: every kill form ends only the direct child, and the grandchild is
// alive 1 s, 5 s and 15 s later. A Job Object around the child ends the grandchild here.
func TestSignalEndsOnlyTheDirectChild(t *testing.T) {
	cmd, gpid, group := startConfinedTree(t)

	if !processAlive(t, gpid) {
		t.Fatalf("grandchild %d not alive before signal", gpid)
	}
	if err := group.signal(cmd.Process, "KILL"); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
		t.Errorf("parent %d survived the kill", cmd.Process.Pid)
	}
	time.Sleep(time.Second)
	if !processAlive(t, gpid) {
		t.Errorf("grandchild %d ended with its parent: the kill reached more than the direct child", gpid)
	}
}

// A kill of a child that has ended, while its handle is still open, answers the text that
// 89cb6289 logged on a Windows VM: "TerminateProcess: Access is denied.".
func TestSignalOnAnEndedChildIsAccessDenied(t *testing.T) {
	cmd, _, group := startConfinedTree(t)

	if err := group.signal(cmd.Process, "KILL"); err != nil {
		t.Fatalf("first signal: %v", err)
	}
	_ = cmd.Wait()
	err := group.signal(cmd.Process, "KILL")
	if err == nil || err.Error() != "TerminateProcess: Access is denied." {
		t.Errorf("signal on an ended child = %v, want %q", err, "TerminateProcess: Access is denied.")
	}
}

// close drops the kill handle and ends nothing. The daemon's death closes the handle the
// same way. The children of the reference survive a killed daemon (rows WN03, WJ02, WJ05,
// WJ06 on a Windows VM).
func TestCloseEndsNothing(t *testing.T) {
	cmd, gpid, group := startConfinedTree(t)

	group.close()
	time.Sleep(time.Second)
	if !processAlive(t, gpid) {
		t.Errorf("grandchild %d ended at close", gpid)
	}
	if !processAlive(t, cmd.Process.Pid) {
		t.Errorf("parent %d ended at close", cmd.Process.Pid)
	}
}

// With no handle (zero handle or nil receiver, the failure paths of confineProcess),
// signal must still fall back to killing the parent process.
func TestSignalFallsBackWithoutHandle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *procGroup
	}{
		{"zero handle", &procGroup{}},
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

			if err := tc.group.signal(cmd.Process, "KILL"); err != nil {
				t.Errorf("signal = %v, want nil from the fallback", err)
			}
			if !waitProcessGone(cmd.Process.Pid, 5*time.Second) {
				t.Errorf("parent %d survived: signal did not fall back to proc.Kill", cmd.Process.Pid)
			}
		})
	}
}

// close is idempotent (a second close must not panic or release twice). A closed group
// falls back to proc.Kill, so a signal after close still ends the parent.
func TestCloseIdempotentAndSignalAfterClose(t *testing.T) {
	cmd, _, group := startConfinedTree(t)

	group.close()
	group.close() // second close must be a no-op
	if err := group.signal(cmd.Process, "KILL"); err != nil {
		t.Errorf("signal after close = %v, want nil from the fallback", err)
	}
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
	defer func() { _ = windows.CloseHandle(h) }()
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
		_ = windows.CloseHandle(h)
		if gone {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
