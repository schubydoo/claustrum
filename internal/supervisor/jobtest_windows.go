//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
)

// newJobTestCmd gives the command of the start test: the program of the daemon with
// --version (measured on Windows against 4c111e86). A test replaces it.
var newJobTestCmd = func() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "--version"), nil
}

// startJobTest starts the test child and runs the test (runJobTest). A failed start
// fails the test with its error (claustrum's own, not measured).
func startJobTest() *JobTest {
	start := time.Now()
	c, err := startJobTestChild()
	if err != nil {
		t := &JobTest{}
		t.fail(time.Since(start), err)
		return t
	}
	return runJobTest(c, start, jobTestPendingAfter, jobTestBound)
}

// winJobTestChild is the test child on Windows.
type winJobTestChild struct {
	cmd    *exec.Cmd
	group  *ProcGroup
	jobErr error
	exited chan struct{}
}

// startJobTestChild starts the test child suspended, puts it in a job and resumes it.
// The job has the flags of JobsKeep, so the close of its handle ends nothing (claustrum's
// own, not measured). The only end of the child is End at the bound.
//
// A failed join does not stop the test: the child runs to its end, and Reap gives the
// join error (claustrum's own, not measured). A failed resume leaves the child asleep
// until End at the bound. If the wait handle does not open, the gate closes at once and
// End never runs. The test then fails jobTestReapGrace after the bound (awaitJobTest),
// and the child is left to end by itself (claustrum's own, not measured).
func startJobTestChild() (jobTestChild, error) {
	cmd, err := newJobTestCmd()
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = SpawnSysProcAttr(JobsKeep)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &winJobTestChild{cmd: cmd, exited: make(chan struct{})}
	c.group, c.jobErr = ConfineProcess(cmd.Process, JobsKeep)
	h, hErr := openJobTestWait(cmd.Process.Pid)
	_ = ResumeChild(cmd.Process, cmd.SysProcAttr)
	go func() {
		if hErr == nil {
			_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
			_ = windows.CloseHandle(h)
		}
		close(c.exited)
	}()
	return c, nil
}

// openJobTestWait opens the wait handle of the test child. A test replaces it.
var openJobTestWait = func(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
}

func (c *winJobTestChild) Exited() <-chan struct{} { return c.exited }

// End ends the test child alone, by its own handle, with exit code 1. 4c111e86 gives
// the child exit code 1 at the bound (measured on Windows). It ends no other process
// of the job (claustrum's own). With no handle it ends nothing and gives an error, and
// awaitJobTest then fails the test.
func (c *winJobTestChild) End() error {
	g := c.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.h == 0 {
		return errors.New("no handle of the self-test child")
	}
	return os.NewSyscallError("TerminateProcess", windows.TerminateProcess(g.h, 1))
}

// Reap waits for the child and closes its handles. A child that is in no job fails
// the test. The text of that reason is claustrum's own: 4c111e86 logs it with "a thread
// had been suspended 2 times, so it still sleeps" (measured), which claustrum does not
// test.
func (c *winJobTestChild) Reap() error {
	err := c.cmd.Wait()
	c.group.Close()
	if c.jobErr != nil {
		return fmt.Errorf("starting it inside a Windows job object failed: %v", c.jobErr)
	}
	return err
}
