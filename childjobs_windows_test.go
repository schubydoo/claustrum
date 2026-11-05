//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/runlock"
	"golang.org/x/sys/windows"
)

// The Windows jobs of the children. Every process that a test here ends is one that the
// test started, or one that such a process started. Each job is one that the test or the
// manager of the test made.

// startBystander starts a sleeper outside every manager, as a process beside the daemon.
// The cleanup ends it.
func startBystander(t *testing.T) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "60")
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// TestSpawnStartsSuspendedOnlyWithJobs: with jobs a child starts suspended, so it joins
// its job before it runs. Without jobs it starts as before.
func TestSpawnStartsSuspendedOnlyWithJobs(t *testing.T) {
	for _, tc := range []struct {
		jobs childJobs
		want bool
	}{{jobsOff, false}, {jobsOn, true}, {jobsKeep, true}} {
		attr := spawnSysProcAttr(tc.jobs)
		if got := attr.CreationFlags&windows.CREATE_SUSPENDED != 0; got != tc.want {
			t.Errorf("jobs %d: CREATE_SUSPENDED = %v, want %v", tc.jobs, got, tc.want)
		}
		if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
			t.Errorf("jobs %d: CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", tc.jobs, attr.CreationFlags)
		}
	}
}

// TestJobCloseByMode: the close of the group handles ends a child in a job of jobsOn and
// the process that it started, as a daemon that dies closes them. A job of jobsKeep
// (-keep-children) ends nothing at the close (claustrum's own).
func TestJobCloseByMode(t *testing.T) {
	for _, tc := range []struct {
		jobs childJobs
		ends bool
	}{{jobsOn, true}, {jobsKeep, false}} {
		t.Run(fmt.Sprintf("jobs %d", tc.jobs), func(t *testing.T) {
			cmd, gpid, group := startConfinedTreeJobs(t, tc.jobs)
			if group.job == 0 || !inJob(t, gpid, group.job) {
				t.Fatalf("grandchild %d is not in the job of the child", gpid)
			}
			group.close()
			for _, pid := range []int{cmd.Process.Pid, gpid} {
				if gone := waitProcessGone(pid, 2*time.Second); gone != tc.ends {
					t.Errorf("process %d gone after the close = %v, want %v", pid, gone, tc.ends)
				}
			}
		})
	}
}

// TestKilledDaemonByMode: a process that owns one spawned child in a job ends hard, as a
// daemon under taskkill /F does. With jobsOn the child ends with it. Measured on Windows
// against 52d82f0a: after a taskkill /F of the daemon the tree is gone at 0, 2 and 5 s,
// and a process outside the daemon stays. With jobsKeep (-keep-children) the child lives
// on (claustrum's own). The owner writes the job state of its child first, so the test
// knows that the child was in a job. TestChildSurvivesAKilledDaemon has the child with no
// job.
func TestKilledDaemonByMode(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bystander := startBystander(t)
	for _, tc := range []struct {
		mode string
		ends bool
	}{{"job", true}, {"keep", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			daemon := exec.Command(exe, pidFile, tc.mode)
			daemon.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "spawn-hold"})
			if err := daemon.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })
			pid := readPIDFile(t, pidFile)
			holdOwnPid(t, pid)
			if !processAlive(t, pid) {
				t.Fatalf("child %d not alive before the kill", pid)
			}
			if state, err := os.ReadFile(pidFile + ".job"); err != nil || string(state) != "in-job" {
				t.Fatalf("job state of the child = %q, %v, want in-job", state, err)
			}
			if err := daemon.Process.Kill(); err != nil {
				t.Fatalf("kill the owner: %v", err)
			}
			_ = daemon.Wait()
			if gone := waitProcessGone(pid, 5*time.Second); gone != tc.ends {
				t.Errorf("child %d gone after the kill of its owner = %v, want %v", pid, gone, tc.ends)
			}
		})
	}
	if !processAlive(t, bystander.Process.Pid) {
		t.Errorf("the bystander %d ended", bystander.Process.Pid)
	}
}

// TestJobLineAtStart: the start logs one job line (`52d82f0a`, measured on Windows). It
// ends "true (off switch: false)" with no switch, and "false (off switch: true)" with
// CLAUDE_SSH_NO_JOBS=1. Only the text "1" is the switch (claustrum's own for "0").
// -keep-children gives jobsKeep, and the line stays the same (claustrum's own).
func TestJobLineAtStart(t *testing.T) {
	for _, tc := range []struct {
		value    string
		set      bool
		keep     bool
		want     childJobs
		wantLine string
	}{
		{"", false, false, jobsOn, "true (off switch: false)"},
		{"1", true, false, jobsOff, "false (off switch: true)"},
		{"0", true, false, jobsOn, "true (off switch: false)"},
		{"", false, true, jobsKeep, "true (off switch: false)"},
		{"1", true, true, jobsOff, "false (off switch: true)"},
	} {
		t.Run(fmt.Sprintf("%q set %v keep %v", tc.value, tc.set, tc.keep), func(t *testing.T) {
			t.Setenv(childJobsOffEnv, tc.value)
			if !tc.set {
				_ = os.Unsetenv(childJobsOffEnv)
			}
			buf := captureLogBuf(t)
			if got := startChildJobs(tc.keep); got != tc.want {
				t.Errorf("jobs = %d, want %d", got, tc.want)
			}
			// Another test can leave a child whose exit line lands in this buffer, so the
			// check counts the job lines only.
			want := "[daemon] job objects for child processes as the socket opens: " + tc.wantLine + "\n"
			if log := buf.String(); !strings.Contains(log, want) || strings.Count(log, "job objects") != 1 {
				t.Errorf("log = %q, want one job line %q", log, want)
			}
		})
	}
}

// TestJobLineFollowsTheLockLine: in a start the job line comes after the "not the run
// dir's lock holder" line, as on 52d82f0a (measured on Windows).
func TestJobLineFollowsTheLockLine(t *testing.T) {
	t.Setenv(childJobsOffEnv, "")
	_ = os.Unsetenv(childJobsOffEnv)
	buf := captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	if s.procs.jobs != jobsOn {
		t.Errorf("jobs = %d, want jobsOn", s.procs.jobs)
	}
	wantLinesInOrder(t, buf.String(),
		runlock.NotHolderLine,
		"[daemon] job objects for child processes as the socket opens: true (off switch: false)")
}

// TestKeepChildrenServerKeepsJobs: a daemon started with -keep-children gives its
// children the jobs of jobsKeep, so a kept child outlives the daemon (claustrum's own).
func TestKeepChildrenServerKeepsJobs(t *testing.T) {
	t.Setenv(childJobsOffEnv, "")
	_ = os.Unsetenv(childJobsOffEnv)
	captureLogBuf(t)
	s, sock := bootLineServer(t, true)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	if s.procs.jobs != jobsKeep {
		t.Errorf("jobs = %d, want jobsKeep", s.procs.jobs)
	}
}
