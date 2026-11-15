//go:build unix

package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countRunGroupKills replaces reapRunGroup with a fake that counts its calls
// and sends nothing. The kill through the handle of the leader still ends it.
func countRunGroupKills(t *testing.T) *atomic.Int32 {
	t.Helper()
	var kills atomic.Int32
	prev := reapRunGroup
	t.Cleanup(func() { reapRunGroup = prev })
	reapRunGroup = func(*os.Process) { kills.Add(1) }
	return &kills
}

// boundedRuns are the three runs whose cmd.Cancel sends a group kill. Each run
// starts script under bound and reports that the bound stopped it.
var boundedRuns = []struct {
	name string
	run  func(t *testing.T, script string, bound time.Duration) bool
}{
	{"probe-cli", func(t *testing.T, script string, bound time.Duration) bool {
		setProbeCLITimeout(t, bound)
		return probeCLIRunnable(script) == probeCLIHung
	}},
	{"ldd", func(t *testing.T, script string, bound time.Duration) bool {
		old := lddPath
		t.Cleanup(func() { lddPath = old })
		lddPath = script
		ctx, cancel := context.WithTimeout(context.Background(), bound)
		defer cancel()
		_, err := runLddVersion(ctx)
		return errors.Is(err, errLddBound)
	}},
	{"launcher", func(t *testing.T, script string, bound time.Duration) bool {
		return runViaManagedLauncher([]string{script}, "cli", bound).hung
	}},
}

// writeRunScript writes body as an executable sh script and returns its path.
func writeRunScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBoundedRunLateStopSendsNoGroupKill: Go runs cmd.Cancel from its watch of
// the context, which can come after the wait reaped the run. That call sends no
// group kill (claustrum's own, from the code). The test makes the late call
// itself, right after the wait.
func TestBoundedRunLateStopSendsNoGroupKill(t *testing.T) {
	for _, r := range boundedRuns {
		t.Run(r.name, func(t *testing.T) {
			kills := countRunGroupKills(t)
			var stops atomic.Int32
			prev := runWaited
			t.Cleanup(func() { runWaited = prev })
			runWaited = func(c *exec.Cmd) {
				if c.Cancel != nil {
					stops.Add(1)
					_ = c.Cancel()
				}
			}
			r.run(t, writeRunScript(t, "exit 0"), 20*time.Second)
			if stops.Load() == 0 {
				t.Fatal("no run with a cmd.Cancel ran")
			}
			if n := kills.Load(); n != 0 {
				t.Errorf("group kills after the reap = %d, want 0", n)
			}
		})
	}
}

// TestBoundedRunDeadlineKillsGroup: a bound that fires while the run still runs
// sends one group kill, before the reap. With failWait the wait for the exit
// fails, as a kqueue that cannot be made does on macOS. The gate then stays
// open while the run runs, so the bound still sends its group kill (claustrum's
// own, not measured). The bound of 1 s leaves time for the start on a loaded
// host.
func TestBoundedRunDeadlineKillsGroup(t *testing.T) {
	for _, failWait := range []bool{false, true} {
		for _, r := range boundedRuns {
			t.Run(fmt.Sprintf("%s/failWait=%v", r.name, failWait), func(t *testing.T) {
				kills := countRunGroupKills(t)
				if failWait {
					prev := leaderExitWait
					t.Cleanup(func() { leaderExitWait = prev })
					leaderExitWait = func(int) bool { return false }
				}
				if !r.run(t, writeRunScript(t, "exec sleep 10"), time.Second) {
					t.Fatal("the bound did not stop the run")
				}
				if n := kills.Load(); n != 1 {
					t.Errorf("group kills (failed exit wait %v) = %d, want 1", failWait, n)
				}
			})
		}
	}
}

// TestRunCLIVersionStopStepAfterTheReapSendsNoGroupKill: the stop step of
// runCLIVersion tests its flag after the reap of the CLI. The seam holds the
// stop step until the CLI exited by itself and its wait returned, and then runs
// it before the flag is set. That stop step sends no group kill (claustrum's
// own, from the code).
func TestRunCLIVersionStopStepAfterTheReapSendsNoGroupKill(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	stubDelay(t, time.Second)
	kills := countRunGroupKills(t)
	entered, release, ran := make(chan struct{}), make(chan struct{}), make(chan struct{})
	oldAfter := afterCLIBound
	afterCLIBound = func(d time.Duration, stop func()) *time.Timer {
		return oldAfter(d, func() {
			close(entered)
			<-release
			stop()
			close(ran)
		})
	}
	t.Cleanup(func() { afterCLIBound = oldAfter })
	prev := runWaited
	t.Cleanup(func() { runWaited = prev })
	runWaited = func(*exec.Cmd) {
		select {
		case <-entered:
		case <-time.After(20 * time.Second):
			t.Error("the stop step did not start")
			return
		}
		close(release)
		<-ran
	}
	runCLIVersion(f.cli, 200*time.Millisecond)
	select {
	case <-ran:
	default:
		t.Fatal("the held stop step did not run")
	}
	if n := kills.Load(); n != 0 {
		t.Errorf("group kills after the reap = %d, want 0", n)
	}
}

// TestRunCLIVersionFailedExitWaitStillKills: a wait for the exit that fails
// keeps the gate of runCLIVersion open while the CLI runs, so the bound still
// sends one group kill (claustrum's own, not measured).
func TestRunCLIVersionFailedExitWaitStillKills(t *testing.T) {
	kills := countRunGroupKills(t)
	prev := leaderExitWait
	t.Cleanup(func() { leaderExitWait = prev })
	leaderExitWait = func(int) bool { return false }
	if got := runCLIVersion(writeRunScript(t, "exec sleep 10"), time.Second); got != cliRunStopped {
		t.Fatalf("result %d, want cliRunStopped", got)
	}
	if n := kills.Load(); n != 1 {
		t.Errorf("group kills = %d, want 1", n)
	}
}

// TestRunLddVersionKeepsBothStreamsInOrder: the ldd run gives stderr and
// stdout back in one buffer, in the order that ldd wrote them, as
// CombinedOutput did before the gate.
func TestRunLddVersionKeepsBothStreamsInOrder(t *testing.T) {
	useLddStub(t, t.TempDir(), "#!/bin/sh\nprintf 'musl libc (x86_64)\\n' >&2\nprintf 'out\\n'\n")
	out, err := runLddVersion(context.Background())
	if err != nil {
		t.Fatalf("runLddVersion = %v", err)
	}
	if want := "musl libc (x86_64)\nout\n"; string(out) != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}
