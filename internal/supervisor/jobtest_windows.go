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

// startJobTest starts the test child and begins the test (beginJobTest). A failed start
// fails the test with its error (claustrum's own, not measured).
func startJobTest() *JobTest {
	start := time.Now()
	c, err := startJobTestChild()
	if err != nil {
		t := &JobTest{}
		t.fail(time.Since(start), err)
		return t
	}
	return beginJobTest(c, start, c.suspended, jobTestPendingAfter, jobTestBound)
}

// winJobTestChild is the test child on Windows.
type winJobTestChild struct {
	cmd    *exec.Cmd
	group  *ProcGroup
	jobErr error
	exited chan struct{}
	// suspended is the highest suspend count of a thread before the resume.
	suspended uint32
}

// startJobTestChild starts the test child suspended, puts it in a job and resumes it.
// The job has the flags 0x2800 of JobsOn (measured on Windows against 4c111e86), also
// with -keep-children. So the close of its handle at the reap, or at the exit of the
// daemon, ends what is left in it. At the reap that end has code 0 (measured on Windows,
// claustrum's own). At the daemon exit, 4c111e86 ends the test child with code 0
// (measured on Windows).
//
// A failed join does not stop the test: the child runs to its end, and Reap gives the
// join error (claustrum's own, not measured). A resume that leaves a thread asleep fails
// the test and ends the test job at once (beginJobTest). Other errors of the resume do not stop the test
// (claustrum's own, not measured). End at the bound reaches the child only with a job
// and a wait handle. With no job End fails. If the wait handle does not open, the gate
// closes at once and End never runs. In both cases the test fails jobTestReapGrace after the bound unless
// the reap comes first (awaitJobTest), and the daemon leaves the child as it is
// (claustrum's own, not measured). With no wait handle, the end at an asleep failure
// runs only if it comes before the close of the gate (claustrum's own, not measured).
func startJobTestChild() (*winJobTestChild, error) {
	cmd, err := newJobTestCmd()
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = SpawnSysProcAttr(JobsOn)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &winJobTestChild{cmd: cmd, exited: make(chan struct{})}
	c.group, c.jobErr = ConfineProcess(cmd.Process, JobsOn)
	h, hErr := openJobTestWait(cmd.Process.Pid)
	c.suspended, _ = resumeJobTestChild(cmd.Process, cmd.SysProcAttr)
	go func() {
		if hErr == nil {
			_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
			_ = windows.CloseHandle(h)
		}
		close(c.exited)
	}()
	return c, nil
}

// resumeJobTestChild resumes the test child and gives the suspend count of
// resumeChildCount. A test replaces it.
var resumeJobTestChild = resumeChildCount

// openJobTestWait opens the wait handle of the test child. A test replaces it.
var openJobTestWait = func(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
}

func (c *winJobTestChild) Exited() <-chan struct{} { return c.exited }

// End ends the test job, with exit code 1. Measured on Windows against 4c111e86: at the
// bound the test child and its conhost, both in the test job, exit 0x1 within 2 ms. A
// child that still sleeps after its resume exits 0x1 after about 8 ms. End
// ends no other job and no process outside the test job. With no job it ends nothing and
// gives an error, and awaitJobTest then fails the test (claustrum's own).
func (c *winJobTestChild) End() error {
	g := c.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return errors.New("no job of the self-test child")
	}
	return os.NewSyscallError("TerminateJobObject", windows.TerminateJobObject(g.job, 1))
}

// Reap waits for the child and closes its handles. A child that is in no job fails
// the test with the join error, a text of claustrum's own.
func (c *winJobTestChild) Reap() error {
	err := c.cmd.Wait()
	c.group.Close()
	if c.jobErr != nil {
		return fmt.Errorf("starting it inside a Windows job object failed: %v", c.jobErr)
	}
	return err
}
