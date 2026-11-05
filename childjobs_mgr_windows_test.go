//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/supervisor"
	"golang.org/x/sys/windows"
)

// TestJobEndsTheTree: with jobs, process.kill, process.killAndWait and the kill of a clean
// stop end the child and the process that it started at once. That process holds the
// pipes of the child, so without the job the exit waits for the drain grace. Measured on
// Windows against 52d82f0a: the kill reply comes in 11 ms with exitCode 1. killAndWait
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
			m.jobs = supervisor.JobsOn
			// The helper starts its grandchild at once, with no go-ahead.
			p, gpid := spawnTree(t, m, tc.id, "tree-stdio")
			if p.cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
				t.Errorf("CreationFlags = %#x, want the suspended start of a child with a job", p.cmd.SysProcAttr.CreationFlags)
			}
			if p.group == nil || p.group.JobForTest() == 0 {
				t.Fatal("the child has no job")
			}
			if !inJob(t, gpid, p.group.JobForTest()) {
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
// grace. Measured on Windows against 52d82f0a: `cmd /c "start /b ping …"` gives its exit
// frame at 123 ms. No ping is left. With CLAUDE_SSH_NO_JOBS=1 the frame comes after the
// 5 s drain grace, and the ping runs on.
func TestJobChildExitEndsTheRest(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 30 * time.Second
	t.Cleanup(func() { exitDrainGrace = oldDrain })
	const id = "jt-exit"
	m := newTestProcManager(t)
	m.jobs = supervisor.JobsOn
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

// TestChildReadsJobFlags: a spawned child reads the limit flags of its own job. With
// supervisor.JobsOn they are 0x2800 (kill on job close and breakaway allowed), as on 52d82f0a
// (measured on Windows). With supervisor.JobsKeep they are 0x800 (claustrum's own).
func TestChildReadsJobFlags(t *testing.T) {
	for _, tc := range []struct {
		jobs supervisor.ChildJobs
		want string
	}{{supervisor.JobsOn, "0x2800"}, {supervisor.JobsKeep, "0x800"}} {
		t.Run(tc.want, func(t *testing.T) {
			m := newTestProcManager(t)
			m.jobs = tc.jobs
			t.Cleanup(m.killAll)
			captureLogBuf(t)
			c, frames := pipeConn(t)
			exe, env := helperCommand(t, "job-flags")
			if _, err := m.spawn(c, "jt-flags", exe, nil, "", env, false); err != nil {
				t.Fatalf("spawn: %v", err)
			}
			// The first stdout frame of a new helper can take more than the 3 s of
			// firstStdout on a slow Windows runner, so this test waits up to 10 s.
			got := ""
			deadline := time.After(10 * time.Second)
			for got == "" {
				select {
				case f := <-frames:
					if f.Stream == "stdout" && f.Data != "" {
						b, err := base64.StdEncoding.DecodeString(f.Data)
						if err != nil {
							t.Fatalf("bad base64 in stdout frame: %v", err)
						}
						got = strings.TrimSpace(string(b))
					}
				case <-deadline:
					t.Fatal("no stdout frame within 10 s")
				}
			}
			if got != tc.want {
				t.Errorf("job flags = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJobConfineFailureStillResumes: with jobs on and a failed confinement, the child
// that started suspended still resumes and runs to its end (claustrum's own).
func TestJobConfineFailureStillResumes(t *testing.T) {
	old := confineProc
	t.Cleanup(func() { confineProc = old })
	confineProc = func(*os.Process, supervisor.ChildJobs) (*supervisor.ProcGroup, error) {
		return nil, errors.New("open process: access is denied")
	}
	m := newTestProcManager(t)
	m.jobs = supervisor.JobsOn
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
// process, as on 52d82f0a (measured on Windows).
func TestJobAllowsBreakaway(t *testing.T) {
	const id = "jt-breakaway"
	m := newTestProcManager(t)
	m.jobs = supervisor.JobsOn
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
	if p.group == nil || p.group.JobForTest() == 0 {
		t.Fatal("the child has no job")
	}
	if inJob(t, pid, p.group.JobForTest()) {
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
