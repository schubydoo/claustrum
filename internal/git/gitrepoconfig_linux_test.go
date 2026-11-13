package git

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
// slave, 89cb6289 and 5fd08069 stay up when the pty master closes (measured on
// Linux, 6 of 6 runs). A build without the flag took the pty as its controlling
// terminal there, and it ended at that close.
//
// The open runs in a child of the test binary that leads a new session, as the
// daemon does. Such a process has no controlling terminal, and it gets one from
// an open of a terminal without the flag. The test process is no session
// leader, so an open in it shows nothing. A host with no pty skips the test.
func TestRepoConfigOpenTakesNoTerminal(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	defer linkPtySlave(t, config)()
	if got := childTerminal(t, "open-repo-file", config); got != "no terminal" {
		t.Errorf("after the open of a pty slave the child has %q, want no terminal", got)
	}
}

// linkPtySlave makes link a symbolic link to the slave of a new pty. It returns the
// close of the master. A host with no pty skips the test.
func linkPtySlave(t *testing.T, link string) func() {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("this host gives no pty: %v", err)
	}
	var number uint32
	var unlock int32
	for request, arg := range map[uintptr]unsafe.Pointer{
		syscall.TIOCGPTN:   unsafe.Pointer(&number),
		syscall.TIOCSPTLCK: unsafe.Pointer(&unlock),
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), request, uintptr(arg)); errno != 0 {
			_ = master.Close()
			t.Skipf("this host gives no pty slave: %v", errno)
		}
	}
	mustSymlink(t, "/dev/pts/"+strconv.FormatUint(uint64(number), 10), link)
	return func() { _ = master.Close() }
}

// childTerminal runs the helper mode of the test binary on arg in a child that leads
// a new session, as the daemon does. It returns the line of the child: "terminal" or
// "no terminal".
func childTerminal(t *testing.T, mode, arg string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, arg)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": mode})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child of %s: %v, output %q", mode, err, out)
	}
	return strings.TrimSpace(string(out))
}
