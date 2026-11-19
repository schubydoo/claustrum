package supervisor

import (
	"fmt"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The start test of the Windows jobs. Only Windows runs it (jobtest_windows.go). This
// file holds the steps that need no Windows call, so the tests of every system run them.
//
// Measured on Windows against 4c111e86. The daemon runs its own program with --version
// in a job. As admin the test passes in about 28 ms and logs nothing. As a user who is
// no admin the child exits 0xc0000142 and the daemon logs jobTestFailedLine. A child
// that has not ended after 5 s gives jobTestPendingLine, and the socket opens then.
// The daemon ends the test job at 1 minute. The child exits with code 1 then, and the
// daemon logs jobTestFailedLine with jobTestBoundReason.

// jobTestFailedLine is the line of a failed test: the time since the start of the test
// and the reason (measured on Windows against 4c111e86).
const jobTestFailedLine = "[process] job objects are not used on this host, so ending a process will not end what it started: the self-test failed after %s: %v"

// jobTestPendingLine is the line of a test whose child runs after jobTestPendingAfter.
// It names jobTestPendingAfter and jobTestBound (measured on Windows against 4c111e86).
const jobTestPendingLine = "[process] job objects are not used yet, so ending a process started now will not end what it starts: the self-test's program has not run to its end after %s; it may run for %s in all"

// jobTestBoundReason is the reason of a test that the bound ended. It names
// jobTestBound (measured on Windows against 4c111e86).
const jobTestBoundReason = "the program had not run to its end after %s"

const (
	// jobTestPendingAfter is the wait of the start for the test (measured on Windows).
	jobTestPendingAfter = 5 * time.Second
	// jobTestBound is the time after which the test child is ended (measured on Windows).
	jobTestBound = time.Minute
)

// jobTestReapGrace is the wait at the bound for the reap of a child that the end did not
// reach (claustrum's own, not measured). A test shortens it.
var jobTestReapGrace = 5 * time.Second

// jobTestChild is the child of the start test.
type jobTestChild interface {
	// Exited closes at the exit of the child, before its reap.
	Exited() <-chan struct{}
	// End ends the test job of the child, with exit code 1.
	End() error
	// Reap waits for the child. Its error is the reason of a failed test, and nil
	// passes the test.
	Reap() error
}

// runJobTest runs the test of the child c, which started at start. It waits up to
// pendingAfter for the result. A child that still runs then makes the test pending,
// and a goroutine waits on (awaitJobTest).
func runJobTest(c jobTestChild, start time.Time, pendingAfter, bound time.Duration) *JobTest {
	t := &JobTest{}
	// The gate closes at the exit of the child and before its reap, so the end at the
	// bound reaches the test job only while its child is not reaped.
	gate := &sysproc.LeaderGate{}
	reaped := make(chan error, 1)
	go func() {
		<-c.Exited()
		gate.Close()
		reaped <- c.Reap()
	}()
	wait := time.NewTimer(pendingAfter)
	defer wait.Stop()
	select {
	case err := <-reaped:
		t.finish(start, err)
		return t
	case <-wait.C:
	}
	logging.Infof(jobTestPendingLine, pendingAfter, bound)
	t.state.Store(uint32(JobTestPending))
	go t.awaitJobTest(c, gate, reaped, start, bound)
	return t
}

// awaitJobTest waits for the result of a pending test. At the bound it ends the test
// job once, only through the gate, and fails the test with the bound reason (measured on
// Windows against 4c111e86). A child that exited just before the bound is not ended,
// and its result counts.
//
// claustrum's own, not measured: a clean exit in the pending time puts the jobs in use
// for the later spawns. After an end that fails, or a gate that the exit closed, the
// result of a reap within jobTestReapGrace counts, and else the test fails with the
// bound reason. So the test never stays pending past the bound and the grace.
func (t *JobTest) awaitJobTest(c jobTestChild, gate *sysproc.LeaderGate, reaped <-chan error, start time.Time, bound time.Duration) {
	timer := time.NewTimer(bound - time.Since(start))
	defer timer.Stop()
	select {
	case err := <-reaped:
		t.finish(start, err)
		return
	case <-timer.C:
	}
	ended := false
	gate.Do(func() { ended = c.End() == nil })
	took := time.Since(start)
	boundErr := fmt.Errorf(jobTestBoundReason, bound)
	if ended {
		// A child that exited clean just before the end took hold keeps its pass.
		if err := <-reaped; err == nil {
			t.finish(start, nil)
			return
		}
		t.fail(took, boundErr)
		return
	}
	grace := time.NewTimer(jobTestReapGrace)
	defer grace.Stop()
	select {
	case err := <-reaped:
		t.finish(start, err)
	case <-grace.C:
		t.fail(took, boundErr)
	}
}

// finish takes the result err of a reaped child.
func (t *JobTest) finish(start time.Time, err error) {
	if err == nil {
		t.state.Store(uint32(JobTestPassed))
		return
	}
	t.fail(time.Since(start), err)
}

// fail logs the failed test and turns the jobs off. The time is in whole
// milliseconds, as measured. The rounding to the nearest one is claustrum's own.
func (t *JobTest) fail(took time.Duration, err error) {
	logging.Infof(jobTestFailedLine, took.Round(time.Millisecond), err)
	t.state.Store(uint32(JobTestFailed))
}
