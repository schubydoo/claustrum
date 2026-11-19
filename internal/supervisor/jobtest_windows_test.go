//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The start test of the jobs with a real child on Windows. Each process that a test
// here ends is the test child that the test started, or the grandchild that that child
// started.

// useJobTestHelper makes the test child run this test binary in the helper mode.
func useJobTestHelper(t *testing.T, mode string, args ...string) {
	t.Helper()
	exe, env := helperCommand(t, mode)
	old := newJobTestCmd
	newJobTestCmd = func() (*exec.Cmd, error) {
		cmd := exec.Command(exe, args...)
		cmd.Env = BuildEnv(env)
		return cmd, nil
	}
	t.Cleanup(func() { newJobTestCmd = old })
}

// winProcessAlive reports that the process pid has not ended.
func winProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == uint32(windows.WAIT_TIMEOUT)
}

// startJobTestForTest starts the test child and runs the test with short bounds.
func startJobTestForTest(t *testing.T, pendingAfter, bound time.Duration) (*JobTest, *winJobTestChild) {
	t.Helper()
	c, err := startJobTestChild()
	if err != nil {
		t.Fatalf("start of the test child: %v", err)
	}
	return runJobTest(c, time.Now(), pendingAfter, bound), c.(*winJobTestChild)
}

// TestWindowsJobTestPassesWithAChildThatEndsClean: a test child that exits 0 in its job
// passes the test.
func TestWindowsJobTestPassesWithAChildThatEndsClean(t *testing.T) {
	useJobTestHelper(t, "echo", "hi")
	captureLogBuf(t)
	test, c := startJobTestForTest(t, 5*time.Second, time.Minute)
	if test.State() != JobTestPassed {
		t.Errorf("state = %d, want passed", test.State())
	}
	if c.jobErr != nil {
		t.Errorf("the test child joined no job: %v", c.jobErr)
	}
}

// TestWindowsJobTestFailsWithTheExitStatus: a test child that exits 2 fails the test
// with "exit status 2".
func TestWindowsJobTestFailsWithTheExitStatus(t *testing.T) {
	useJobTestHelper(t, "no-such-mode")
	buf := captureLogBuf(t)
	test, _ := startJobTestForTest(t, 5*time.Second, time.Minute)
	if test.State() != JobTestFailed {
		t.Errorf("state = %d, want failed", test.State())
	}
	if !strings.Contains(buf.String(), ": exit status 2\n") {
		t.Errorf("log = %q, want the failed line with exit status 2", buf.String())
	}
}

// TestWindowsJobTestEndsOnlyTheHungChild: a test child that starts a grandchild and
// then sleeps is ended at the bound with exit code 1, as on 4c111e86 (measured on
// Windows). The grandchild is in the job of the test child and lives on, so the end
// reaches neither the job nor another process.
func TestWindowsJobTestEndsOnlyTheHungChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	useJobTestHelper(t, "tree", pidFile)
	buf := captureLogBuf(t)
	test, c := startJobTestForTest(t, 300*time.Millisecond, 3*time.Second)
	gpid := 0
	deadline := time.Now().Add(10 * time.Second)
	for gpid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil {
			gpid, _ = strconv.Atoi(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatal("the test child wrote no grandchild pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(gpid)); err == nil {
			_ = windows.TerminateProcess(h, 1)
			_ = windows.CloseHandle(h)
		}
	})
	if test.State() != JobTestPending {
		t.Fatalf("state = %d, want pending", test.State())
	}
	deadline = time.Now().Add(20 * time.Second)
	for test.State() != JobTestFailed {
		if time.Now().After(deadline) {
			t.Fatalf("state = %d 20 s after the start, want failed", test.State())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code := c.cmd.ProcessState.ExitCode(); code != 1 {
		t.Errorf("exit code of the test child = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), ": the program had not run to its end after 3s\n") {
		t.Errorf("log = %q, want the bound line", buf.String())
	}
	if !winProcessAlive(gpid) {
		t.Errorf("the grandchild %d ended", gpid)
	}
}

// TestWindowsJobTestEndWithNoHandle: End of a child with no kill handle ends nothing and
// gives an error, so awaitJobTest fails the test.
func TestWindowsJobTestEndWithNoHandle(t *testing.T) {
	c := &winJobTestChild{group: &ProcGroup{}}
	if err := c.End(); err == nil {
		t.Error("End with no handle gave no error")
	}
}

// TestWindowsJobTestNoWaitHandle: a wait handle that does not open closes the gate at
// once. A hung test child then gets no end, and the test fails after the bound and the
// grace (claustrum's own). The test ends its own child at the cleanup.
func TestWindowsJobTestNoWaitHandle(t *testing.T) {
	useJobTestHelper(t, "sleep", "60")
	oldOpen, oldGrace := openJobTestWait, jobTestReapGrace
	openJobTestWait = func(int) (windows.Handle, error) { return 0, windows.ERROR_ACCESS_DENIED }
	jobTestReapGrace = 200 * time.Millisecond
	t.Cleanup(func() { openJobTestWait, jobTestReapGrace = oldOpen, oldGrace })
	captureLogBuf(t)
	test, c := startJobTestForTest(t, 100*time.Millisecond, time.Second)
	t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for test.State() != JobTestFailed {
		if time.Now().After(deadline) {
			t.Fatalf("state = %d 10 s after the start, want failed", test.State())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !winProcessAlive(c.cmd.Process.Pid) {
		t.Error("the test child ended, but the gate was closed")
	}
}
