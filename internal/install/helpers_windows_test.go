//go:build windows

package install

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The helpers of package main that the Windows tests of this package use,
// under the same names.

// ignoreSigterm is a no-op on Windows, which has no POSIX signals.
func ignoreSigterm() {}

// stubProcIDs is empty on Windows, which has no process group id or session id
// of the POSIX kind.
func stubProcIDs() string { return "" }

// stubNewSession starts a child of the "cli-stub" mode in a new process group,
// the closest Windows analogue of a new session.
func stubNewSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// holdOpen opens path with no delete sharing, as the holder does, and returns
// the release. A folder is opened with backup semantics.
func holdOpen(t *testing.T, path string, dir bool) (release func()) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("the hold of %s: %v", path, err)
	}
	var flags uint32
	if dir {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		t.Fatalf("the hold of %s: %v", path, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = windows.CloseHandle(h) }) }
	t.Cleanup(release)
	return release
}

// readPIDFile polls until the file holds a parseable PID (the helper writes it
// asynchronously).
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
