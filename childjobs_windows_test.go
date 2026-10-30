//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	}{{jobsOff, false}, {jobsOn, true}} {
		attr := spawnSysProcAttr(tc.jobs)
		if got := attr.CreationFlags&windows.CREATE_SUSPENDED != 0; got != tc.want {
			t.Errorf("jobs %d: CREATE_SUSPENDED = %v, want %v", tc.jobs, got, tc.want)
		}
		if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
			t.Errorf("jobs %d: CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", tc.jobs, attr.CreationFlags)
		}
	}
}

// TestJobEndsTheTree: with jobs, process.kill, process.killAndWait and the kill of a clean
// stop end the child and the process that it started at once. That process holds the
// pipes of the child, so without the job the exit waits for the drain grace. Measured on a
// Windows VM against 52d82f0a: the kill reply comes in 11 ms with exitCode 1. killAndWait
// answers {"found":true,"died":true} in 21 ms. No process of the tree is left, and a
// process outside the daemon stays.
func TestJobEndsTheTree(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 30 * time.Second
	t.Cleanup(func() { exitDrainGrace = oldDrain })
	type result struct{ found, died, already, escalated bool }
	rows := []struct {
		id      string
		act     func(m *procManager, id string) result
		wantLog string
	}{
		{"jt-kill", func(m *procManager, id string) result { _ = m.kill(id, ""); return result{} },
			", asked to end by this daemon (client)"},
		{"jt-killAndWait", func(m *procManager, id string) result {
			f, d, a, e := m.killAndWait(id, "", 3*time.Second, true)
			return result{f, d, a, e}
		}, ", asked to end by this daemon (client)"},
		{"jt-clean-stop", func(m *procManager, _ string) result { m.killAll(); return result{} }, ""},
	}
	bystander := startBystander(t)
	for _, tc := range rows {
		t.Run(tc.id, func(t *testing.T) {
			m := newTestProcManager(t)
			m.jobs = jobsOn
			// The helper starts its grandchild at once, with no go-ahead.
			p, gpid := spawnTree(t, m, tc.id, "tree-stdio")
			if p.cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
				t.Errorf("CreationFlags = %#x, want the suspended start of a child with a job", p.cmd.SysProcAttr.CreationFlags)
			}
			if p.group == nil || p.group.job == 0 {
				t.Fatal("the child has no job")
			}
			if !inJob(t, gpid, p.group.job) {
				t.Errorf("grandchild %d is not in the job of the child", gpid)
			}
			buf := captureLogBuf(t)

			start := time.Now()
			got := tc.act(m, tc.id)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				t.Fatal("no exit of the managed process")
			}
			if took := time.Since(start); took >= exitDrainGrace {
				t.Errorf("the exit came after %s, want it before the drain grace", took)
			}
			if tc.id == "jt-killAndWait" && got != (result{found: true, died: true}) {
				t.Errorf("killAndWait = %+v, want found and died only", got)
			}
			if !waitProcessGone(gpid, 5*time.Second) {
				t.Errorf("grandchild %d outlived the kill of its job", gpid)
			}
			log := buf.String()
			if strings.Contains(log, "pipe drain grace expired") || strings.Contains(log, "group kill failed") {
				t.Errorf("log = %q, want no drain line and no group kill line", log)
			}
			if lines := exitLinesOf(log, tc.id); len(lines) != 1 {
				t.Errorf("log = %q, want exactly one exit line of %s with code 1, got %d", log, tc.id, len(lines))
			} else if w := exitLineOf(tc.id) + tc.wantLog; tc.wantLog != "" && lines[0] != w {
				t.Errorf("exit line = %q, want %q", lines[0], w)
			}
		})
	}
	if !processAlive(t, bystander.Process.Pid) {
		t.Errorf("the bystander %d ended", bystander.Process.Pid)
	}
}

// TestJobChildExitEndsTheRest: a child in a job exits at once and leaves a process that
// holds its pipes. The rest of the job ends, and the exit comes at once, with no drain
// grace. Measured on a Windows VM against 52d82f0a: `cmd /c "start /b ping …"` gives its
// exit frame at 123 ms. No ping is left. With CLAUDE_SSH_NO_JOBS=1 the
// frame comes after the 5 s drain grace, and the ping runs on.
func TestJobChildExitEndsTheRest(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 30 * time.Second
	t.Cleanup(func() { exitDrainGrace = oldDrain })
	const id = "jt-exit"
	m := newTestProcManager(t)
	m.jobs = jobsOn
	buf := captureLogBuf(t)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "exit-leave-kid")
	pidFile := filepath.Join(t.TempDir(), "gpid")
	start := time.Now()
	p, err := m.spawn(c, id, exe, []string{pidFile}, "", env, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	holdOwnPid(t, p.pid)
	gpid := readPIDFile(t, pidFile)
	holdOwnPid(t, gpid)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("no exit of the managed process")
	}
	if took := time.Since(start); took >= exitDrainGrace {
		t.Errorf("the exit came after %s, want it before the drain grace", took)
	}
	if !waitProcessGone(gpid, 5*time.Second) {
		t.Errorf("grandchild %d outlived its child in the job", gpid)
	}
	log := buf.String()
	if strings.Contains(log, "pipe drain grace expired") {
		t.Errorf("log = %q, want no drain line", log)
	}
	if !strings.Contains(log, "[process.Manager] Process "+id+" exited with code 0\n") {
		t.Errorf("log = %q, want the exit line of %s with code 0", log, id)
	}
}

// TestJobCloseEndsNothing: the close of the group handles leaves a child in its job alive.
// No job ends its processes at the close of its last handle. So a daemon under taskkill
// /F leaves the tree, and so does a child that -keep-children kept (claustrum's own).
func TestJobCloseEndsNothing(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "60")
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
	cmd.SysProcAttr = spawnSysProcAttr(jobsOn)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	group, err := confineProcess(cmd.Process, jobsOn)
	if err != nil {
		group.close()
		t.Fatalf("confineProcess: %v", err)
	}
	if err := resumeChild(cmd.Process, cmd.SysProcAttr); err != nil {
		group.close()
		t.Fatalf("resumeChild: %v", err)
	}
	if !inJob(t, cmd.Process.Pid, group.job) {
		group.close()
		t.Fatalf("child %d is not in its job", cmd.Process.Pid)
	}
	group.close()
	if waitProcessGone(cmd.Process.Pid, 2*time.Second) {
		t.Errorf("child %d ended at the close of its job", cmd.Process.Pid)
	}
}

// TestKilledDaemonLeavesAJobChild: a process that owns one spawned child in a job ends
// hard, as a daemon under taskkill /F does. The child lives on, as the children of
// 89cb6289 do (measured with no job). 52d82f0a is not measured there. The owner writes
// the job state of its child first, so the test knows that the child was in a job.
// TestChildSurvivesAKilledDaemon has the child with no job.
func TestKilledDaemonLeavesAJobChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	daemon := exec.Command(exe, pidFile, "job")
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
	if waitProcessGone(pid, 2*time.Second) {
		t.Errorf("child %d ended with its killed owner", pid)
	}
}

// TestJobConfineFailureStillResumes: with jobs on and a failed confinement, the child
// that started suspended still resumes and runs to its end (claustrum's own).
func TestJobConfineFailureStillResumes(t *testing.T) {
	old := confineProc
	t.Cleanup(func() { confineProc = old })
	confineProc = func(*os.Process, childJobs) (*procGroup, error) {
		return nil, errors.New("open process: access is denied")
	}
	m := newTestProcManager(t)
	m.jobs = jobsOn
	t.Cleanup(m.killAll)
	captureLogBuf(t)
	c, _ := pipeConn(t)
	echo, env := helperCommand(t, "echo")
	p, err := m.spawn(c, "jt-confine", echo, []string{"hi"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn = %v, want success: a confinement failure is not fatal", err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the child never ran to its end: it was not resumed")
	}
}

// TestJobAllowsBreakaway: a process in the job of a child that starts a process with
// CREATE_BREAKAWAY_FROM_JOB gets it, outside the job. The kill of the job leaves that
// process. claustrum's own, not measured.
func TestJobAllowsBreakaway(t *testing.T) {
	const id = "jt-breakaway"
	m := newTestProcManager(t)
	m.jobs = jobsOn
	captureLogBuf(t)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "breakaway-start")
	pidFile := filepath.Join(t.TempDir(), "pid")
	p, err := m.spawn(c, id, exe, []string{pidFile}, "", env, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	holdOwnPid(t, p.pid)
	pid := readPIDFile(t, pidFile)
	holdOwnPid(t, pid)
	if p.group == nil || p.group.job == 0 {
		t.Fatal("the child has no job")
	}
	if inJob(t, pid, p.group.job) {
		t.Errorf("the breakaway process %d is in the job of the child", pid)
	}
	_ = m.kill(id, "")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("no exit of the managed process")
	}
	if waitProcessGone(pid, time.Second) {
		t.Errorf("the breakaway process %d ended with the job", pid)
	}
}

// TestJobLineAtStart: the start logs one job line (52d82f0a, Windows VM). It ends "true
// (off switch: false)" with no switch, and "false (off switch: true)" with
// CLAUDE_SSH_NO_JOBS=1. Only the text "1" is the switch (claustrum's own for "0").
func TestJobLineAtStart(t *testing.T) {
	for _, tc := range []struct {
		value    string
		set      bool
		want     childJobs
		wantLine string
	}{
		{"", false, jobsOn, "true (off switch: false)"},
		{"1", true, jobsOff, "false (off switch: true)"},
		{"0", true, jobsOn, "true (off switch: false)"},
	} {
		t.Run(fmt.Sprintf("%q set %v", tc.value, tc.set), func(t *testing.T) {
			t.Setenv(childJobsOffEnv, tc.value)
			if !tc.set {
				_ = os.Unsetenv(childJobsOffEnv)
			}
			buf := captureLogBuf(t)
			if got := startChildJobs(); got != tc.want {
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
// dir's lock holder" line, as on 52d82f0a (Windows VM).
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
		runDirNotHolderLine,
		"[daemon] job objects for child processes as the socket opens: true (off switch: false)")
}
