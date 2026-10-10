//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// readWithLimit runs one files.read and fails the test if no answer comes in
// 10 s. A read of a pipe or a device that is not refused can wait without end.
func readWithLimit(t *testing.T, s *server, path string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path})) }()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatalf("files.read of %s did not answer in 10 s", path)
		return ""
	}
}

// TestFilesReadRefusesNulOnWindows pins the NUL device in each spelling that
// 5fd08069 refuses (Windows VM, rows 2, 3, 5a, 5c, 5d and 5e). The last row is
// NUL inside a folder. Windows 11 IoT LTSC 26100 maps that name to the device:
// the VM row is C:\o\w\f\NUL. No other Windows version is measured.
func TestFilesReadRefusesNulOnWindows(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{
		"NUL", "nul", "NUL:", `\\.\NUL`, `\\?\NUL`, filepath.Join(t.TempDir(), "NUL"),
	} {
		got := readWithLimit(t, s, path)
		if !strings.Contains(got, `"code":-32602,"message":"files.read: not a regular file"`) {
			t.Errorf("files.read(%s) = %s, want the refusal", path, got)
		}
	}
}

// TestFilesReadRefusesNamedPipeOnWindows pins a named pipe that the test makes
// itself. 5fd08069 refuses a pipe with and without data (Windows VM, rows 7a to
// 7d). The test makes one listening instance in its own thread and keeps the
// server handle open during the read. No goroutine accepts: the open of the
// daemon connects to the listening instance.
func TestFilesReadRefusesNamedPipeOnWindows(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\claustrum-files-read-%d`, os.Getpid())
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(name16, windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatalf("create the pipe %s: %v", name, err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })

	got := readWithLimit(t, newTestServer(t), name)
	if !strings.Contains(got, `"code":-32602,"message":"files.read: not a regular file"`) {
		t.Errorf("files.read(%s) = %s, want the refusal", name, got)
	}
}
