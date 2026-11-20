package supervisor

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJobTestChild is a test child of the start test of the jobs. It is no process, so
// nothing here signals a process. End counts its calls and lets the child exit.
type fakeJobTestChild struct {
	exited  chan struct{}
	release chan error // the result that Reap gives
	ends    atomic.Int32
	// endExits makes End close exited and give the result of an ended child.
	endExits bool
	// endErr is the error of End. An End with an error lets the child run.
	endErr error
	// endClean makes End close exited and give a nil result, as for a child that
	// exited clean just before the end.
	endClean bool
}

func newFakeJobTestChild(endExits bool) *fakeJobTestChild {
	return &fakeJobTestChild{exited: make(chan struct{}), release: make(chan error, 1), endExits: endExits}
}

func (c *fakeJobTestChild) Exited() <-chan struct{} { return c.exited }

func (c *fakeJobTestChild) End() error {
	if c.endErr != nil {
		c.ends.Add(1)
		return c.endErr
	}
	if c.ends.Add(1) == 1 && c.endExits {
		close(c.exited)
		if c.endClean {
			c.release <- nil
		} else {
			c.release <- errors.New("exit status 1")
		}
	}
	return nil
}

func (c *fakeJobTestChild) Reap() error { return <-c.release }

// endBySelf lets the child end by itself with the result err.
func (c *fakeJobTestChild) endBySelf(err error) {
	close(c.exited)
	c.release <- err
}

// waitJobTestState polls the state of t until it is want, within 5 s.
func waitJobTestState(t *testing.T, test *JobTest, want JobTestState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for test.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state = %d, want %d", test.State(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestJobTestLineTexts: the three texts of the start test with the measured bounds, as
// 4c111e86 logs them (measured on Windows).
func TestJobTestLineTexts(t *testing.T) {
	if got, want := fmt.Sprintf(jobTestPendingLine, jobTestPendingAfter, jobTestBound),
		"[process] job objects are not used yet, so ending a process started now will not end what it starts: the self-test's program has not run to its end after 5s; it may run for 1m0s in all"; got != want {
		t.Errorf("pending line = %q, want %q", got, want)
	}
	if got, want := fmt.Sprintf(jobTestFailedLine, (60001*time.Millisecond+400*time.Microsecond).Round(time.Millisecond), fmt.Errorf(jobTestBoundReason, jobTestBound)),
		"[process] job objects are not used on this host, so ending a process will not end what it started: the self-test failed after 1m0.001s: the program had not run to its end after 1m0s"; got != want {
		t.Errorf("bound line = %q, want %q", got, want)
	}
	if got, want := fmt.Sprintf(jobTestFailedLine, (27*time.Millisecond+300*time.Microsecond).Round(time.Millisecond), errors.New("exit status 0xc0000142")),
		"[process] job objects are not used on this host, so ending a process will not end what it started: the self-test failed after 27ms: exit status 0xc0000142"; got != want {
		t.Errorf("failed line = %q, want %q", got, want)
	}
}

// TestJobTestPassesQuietly: a child that ends clean before the pending time passes the
// test, logs nothing and is not ended.
func TestJobTestPassesQuietly(t *testing.T) {
	buf := captureLogBuf(t)
	c := newFakeJobTestChild(false)
	c.endBySelf(nil)
	test := runJobTest(c, time.Now(), 5*time.Second, time.Minute)
	if test.State() != JobTestPassed || test.SpawnsOutside() {
		t.Errorf("state = %d, outside %v, want passed", test.State(), test.SpawnsOutside())
	}
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times, want 0", n)
	}
	if strings.Contains(buf.String(), "self-test") {
		t.Errorf("log = %q, want no line of the test", buf.String())
	}
}

// TestJobTestFailsWithTheExit: a child that fails before the pending time fails the
// test at once with its exit, and is not ended.
func TestJobTestFailsWithTheExit(t *testing.T) {
	buf := captureLogBuf(t)
	c := newFakeJobTestChild(false)
	c.endBySelf(errors.New("exit status 0xc0000142"))
	test := runJobTest(c, time.Now(), 5*time.Second, time.Minute)
	if test.State() != JobTestFailed || !test.SpawnsOutside() {
		t.Errorf("state = %d, outside %v, want failed", test.State(), test.SpawnsOutside())
	}
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times, want 0", n)
	}
	re := regexp.MustCompile(`\[process\] job objects are not used on this host, so ending a process will not end what it started: the self-test failed after (\d+ms|0s): exit status 0xc0000142\n`)
	if !re.MatchString(buf.String()) {
		t.Errorf("log = %q, want the failed line", buf.String())
	}
}

// TestJobTestEndsAHungChildOnceAtTheBound: a child that runs past the pending time makes
// the test pending with its line. At the bound the test ends that child once and
// fails with the bound reason.
func TestJobTestEndsAHungChildOnceAtTheBound(t *testing.T) {
	buf := captureLogBuf(t)
	c := newFakeJobTestChild(true)
	start := time.Now()
	test := runJobTest(c, start, 200*time.Millisecond, 2*time.Second)
	if test.State() != JobTestPending || !test.SpawnsOutside() {
		t.Fatalf("state = %d, outside %v, want pending", test.State(), test.SpawnsOutside())
	}
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times before the bound, want 0", n)
	}
	if !strings.Contains(buf.String(), "[process] job objects are not used yet, so ending a process started now will not end what it starts: the self-test's program has not run to its end after 200ms; it may run for 2s in all\n") {
		t.Errorf("log = %q, want the pending line", buf.String())
	}
	waitJobTestState(t, test, JobTestFailed)
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("the test failed after %v, before the bound", took)
	}
	if n := c.ends.Load(); n != 1 {
		t.Errorf("End ran %d times, want 1", n)
	}
	re := regexp.MustCompile(`the self-test failed after \d+(\.\d+)?s: the program had not run to its end after 2s\n`)
	if !re.MatchString(buf.String()) {
		t.Errorf("log = %q, want the bound line", buf.String())
	}
}

// TestJobTestKeepsACleanExitAtTheEnd: an end that took hold at the bound, of a child
// that exited clean just before it, keeps the pass (claustrum's own, not measured).
func TestJobTestKeepsACleanExitAtTheEnd(t *testing.T) {
	captureLogBuf(t)
	c := newFakeJobTestChild(true)
	c.endClean = true
	test := runJobTest(c, time.Now(), 20*time.Millisecond, 200*time.Millisecond)
	waitJobTestState(t, test, JobTestPassed)
	if n := c.ends.Load(); n != 1 {
		t.Errorf("End ran %d times, want 1", n)
	}
}

// TestJobTestFailsWhenTheEndFails: after an end that fails at the bound and no reap in
// the grace, the test fails with the bound reason, so it does not stay pending
// (claustrum's own).
func TestJobTestFailsWhenTheEndFails(t *testing.T) {
	old := jobTestReapGrace
	jobTestReapGrace = 100 * time.Millisecond
	t.Cleanup(func() { jobTestReapGrace = old })
	buf := captureLogBuf(t)
	c := newFakeJobTestChild(false)
	c.endErr = errors.New("no handle of the self-test child")
	test := runJobTest(c, time.Now(), 20*time.Millisecond, 200*time.Millisecond)
	waitJobTestState(t, test, JobTestFailed)
	if n := c.ends.Load(); n != 1 {
		t.Errorf("End ran %d times, want 1", n)
	}
	if !strings.Contains(buf.String(), ": the program had not run to its end after 200ms\n") {
		t.Errorf("log = %q, want the bound line", buf.String())
	}
}

// TestJobTestFailsWhenTheReapNeverComes: a child whose exit closed the gate and whose
// reap does not come fails the test jobTestReapGrace after the bound, with no end, as
// after a wait handle that did not open (claustrum's own).
func TestJobTestFailsWhenTheReapNeverComes(t *testing.T) {
	old := jobTestReapGrace
	jobTestReapGrace = 100 * time.Millisecond
	t.Cleanup(func() { jobTestReapGrace = old })
	captureLogBuf(t)
	c := newFakeJobTestChild(false)
	close(c.exited)
	test := runJobTest(c, time.Now(), 20*time.Millisecond, 200*time.Millisecond)
	waitJobTestState(t, test, JobTestFailed)
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times, want 0", n)
	}
	c.release <- nil
}

// TestJobTestSendsNoEndAfterTheExit: a child that has exited but is not reaped at the
// bound gets no end, and its own result counts.
func TestJobTestSendsNoEndAfterTheExit(t *testing.T) {
	captureLogBuf(t)
	bound := make(chan struct{}, 1)
	hook := func() {
		select {
		case bound <- struct{}{}:
		default:
		}
	}
	afterJobTestBound.Store(&hook)
	t.Cleanup(func() { afterJobTestBound.Store(nil) })
	c := newFakeJobTestChild(false)
	test := runJobTest(c, time.Now(), 20*time.Millisecond, 200*time.Millisecond)
	if test.State() != JobTestPending {
		t.Fatalf("state = %d, want pending", test.State())
	}
	// The child exits, and its reap waits until the step at the bound has passed the gate.
	close(c.exited)
	select {
	case <-bound:
	case <-time.After(5 * time.Second):
		t.Fatal("the step at the bound did not run within 5 s")
	}
	c.release <- nil
	waitJobTestState(t, test, JobTestPassed)
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times after the exit, want 0", n)
	}
}

// TestJobTestLatePass: a child that ends clean in the pending time puts the jobs in use
// (claustrum's own, not measured), with no end.
func TestJobTestLatePass(t *testing.T) {
	captureLogBuf(t)
	c := newFakeJobTestChild(false)
	test := runJobTest(c, time.Now(), 20*time.Millisecond, time.Minute)
	if test.State() != JobTestPending {
		t.Fatalf("state = %d, want pending", test.State())
	}
	c.endBySelf(nil)
	waitJobTestState(t, test, JobTestPassed)
	if n := c.ends.Load(); n != 0 {
		t.Errorf("End ran %d times, want 0", n)
	}
}

// TestSpawnOutsideJobByTestState: a spawn puts its child in no job and marks it outside
// while the start test is pending or failed, and only with the jobs on.
func TestSpawnOutsideJobByTestState(t *testing.T) {
	for _, tc := range []struct {
		jobs  ChildJobs
		state JobTestState
		test  bool
		want  bool
	}{
		{JobsOn, JobTestFailed, true, true},
		{JobsOn, JobTestPending, true, true},
		{JobsKeep, JobTestFailed, true, true},
		{JobsOn, JobTestPassed, true, false},
		{JobsOn, JobTestPassed, false, false},
		{JobsOff, JobTestFailed, true, false},
	} {
		t.Run(fmt.Sprintf("jobs %d state %d test %v", tc.jobs, tc.state, tc.test), func(t *testing.T) {
			m := newTestProcManager(t)
			t.Cleanup(m.KillAll)
			m.Jobs = tc.jobs
			if tc.test {
				m.JobTest = NewJobTestForTest(tc.state)
			}
			var asked []ChildJobs
			old := confineProc
			confineProc = func(p *os.Process, jobs ChildJobs) (*ProcGroup, error) {
				asked = append(asked, jobs)
				return old(p, jobs)
			}
			t.Cleanup(func() { confineProc = old })
			c, _ := pipeConn(t)
			exe, env := helperCommand(t, "echo")
			p, err := m.Spawn(c, "j1", exe, []string{"hi"}, "", env, true)
			if err != nil {
				t.Fatalf("spawn: %v", err)
			}
			if p.SpawnedOutside() != tc.want {
				t.Errorf("OutsideJob = %v, want %v", p.SpawnedOutside(), tc.want)
			}
			wantJobs := tc.jobs
			if tc.want {
				wantJobs = JobsOff
			}
			if len(asked) != 1 || asked[0] != wantJobs {
				t.Errorf("confine asked for %v, want [%d]", asked, wantJobs)
			}
		})
	}
}
