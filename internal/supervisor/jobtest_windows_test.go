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
	"unsafe"

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

// TestWindowsJobTestEndsTheTestJob: a test child that starts a grandchild and then
// sleeps is in a job with the flags 0x2800, as on 4c111e86 (measured on Windows). At the
// bound the end of the test job ends the child and the grandchild in it with exit code 1,
// as 4c111e86 ends the child and its conhost. A process outside the test job lives on.
// The close of the test job at the reap ends the grandchild with code 0 (measured on
// Windows with an end of the child alone), so the code 1 of the grandchild shows the end.
// The flags are read right after the start, and the bound is longer than the wait for the
// grandchild pid, so a slow start cannot reap the test job first.
func TestWindowsJobTestEndsTheTestJob(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	useJobTestHelper(t, "tree", pidFile)
	exe, env := helperCommand(t, "sleep")
	other := exec.Command(exe, "60")
	other.Env = BuildEnv(env)
	if err := other.Start(); err != nil {
		t.Fatalf("start of the process outside the job: %v", err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	buf := captureLogBuf(t)
	test, c := startJobTestForTest(t, 300*time.Millisecond, 8*time.Second)
	if flags := jobLimitFlags(t, c); flags != 0x2800 {
		t.Errorf("limit flags of the test job = %#x, want 0x2800", flags)
	}
	gpid := 0
	deadline := time.Now().Add(5 * time.Second)
	for gpid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil {
			gpid, _ = strconv.Atoi(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatal("the test child wrote no grandchild pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	gh, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(gpid))
	if err != nil {
		t.Fatalf("open of the grandchild %d: %v", gpid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(gh, 1)
		_ = windows.CloseHandle(gh)
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
	if !strings.Contains(buf.String(), ": the program had not run to its end after 8s\n") {
		t.Errorf("log = %q, want the bound line", buf.String())
	}
	if ev, _ := windows.WaitForSingleObject(gh, 10000); ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("the grandchild %d in the test job did not end", gpid)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(gh, &code); err != nil || code != 1 {
		t.Errorf("exit code of the grandchild = %d (%v), want 1", code, err)
	}
	if !winProcessAlive(other.Process.Pid) {
		t.Errorf("the process %d outside the test job ended", other.Process.Pid)
	}
}

// jobLimitFlags gives the limit flags of the job of the test child c.
func jobLimitFlags(t *testing.T, c *winJobTestChild) uint32 {
	t.Helper()
	c.group.mu.Lock()
	defer c.group.mu.Unlock()
	if c.group.job == 0 {
		t.Fatal("the test child has no job")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(c.group.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		t.Fatalf("query of the test job: %v", err)
	}
	return info.BasicLimitInformation.LimitFlags
}

// TestWindowsJobTestEndWithNoJob: End of a child with a kill handle and no job ends
// nothing and gives an error, so awaitJobTest fails the test. The child is the own child
// of the test.
func TestWindowsJobTestEndWithNoJob(t *testing.T) {
	exe, env := helperCommand(t, "sleep")
	cmd := exec.Command(exe, "60")
	cmd.Env = BuildEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	g, err := ConfineProcess(cmd.Process, JobsOff)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	c := &winJobTestChild{cmd: cmd, group: g}
	if err := c.End(); err == nil {
		t.Error("End with no job gave no error")
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if ev, _ := windows.WaitForSingleObject(h, 500); ev != uint32(windows.WAIT_TIMEOUT) {
		t.Error("End with no job ended the child")
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
