package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// TestRepoConfigOpenTakesNoTerminal pins that the open of the config read gives
// its process no controlling terminal. With a config that is a link to a pty
// slave, 89cb6289 and 5fd08069 stay up when the pty master closes (cell V17,
// Linux VM, 6 of 6 runs). A build without the flag took the pty as its
// controlling terminal there, and it ended at that close.
//
// The open runs in a child of the test binary that leads a new session, as the
// daemon does. Such a process has no controlling terminal, and it gets one from
// an open of a terminal without the flag. The test process is no session
// leader, so an open in it shows nothing. A host with no pty skips the test.
func TestRepoConfigOpenTakesNoTerminal(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("this host gives no pty: %v", err)
	}
	defer func() { _ = master.Close() }()
	var number uint32
	var unlock int32
	for request, arg := range map[uintptr]unsafe.Pointer{
		syscall.TIOCGPTN:   unsafe.Pointer(&number),
		syscall.TIOCSPTLCK: unsafe.Pointer(&unlock),
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), request, uintptr(arg)); errno != 0 {
			t.Skipf("this host gives no pty slave: %v", errno)
		}
	}
	config := filepath.Join(t.TempDir(), "config")
	mustSymlink(t, "/dev/pts/"+strconv.FormatUint(uint64(number), 10), config)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, config)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "open-repo-file"})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child that opens the config: %v, output %q", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "no terminal" {
		t.Errorf("after the open of a pty slave the child has %q, want no terminal", got)
	}
}
