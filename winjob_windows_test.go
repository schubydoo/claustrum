//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows jobs, as a Windows VM measured them against f6010b97 and 89cb6289. The rows are
// WJ01 to WJ07, WN02 to WN05 and K11, and WJ08 on 89cb6289. Every process that a test here ends is one that the
// test started. Each assertion names a job that the test made itself. The result therefore
// does not depend on a job that a CI runner or an SSH server put around the test.

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// inJob reports whether the process pid is in job.
func inJob(t *testing.T, pid int, job windows.Handle) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("OpenProcess(%d): %v", pid, err)
	}
	var res int32
	r, _, e := procIsProcessInJob.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&res)))
	_ = windows.CloseHandle(h)
	if r == 0 {
		t.Fatalf("IsProcessInJob(%d): %v", pid, e)
	}
	return res != 0
}

// newTestJob makes a job with the given limit flags. The caller closes it.
func newTestJob(t *testing.T, flags uint32) windows.Handle {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: flags},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		t.Fatalf("SetInformationJobObject: %v", err)
	}
	return job
}

// launchInJob runs the "detach-launch" helper inside job, the way the SSH server runs the
// -serve launcher inside the job of a session. It returns the pid of the detached process
// that the helper started, and the stderr of the launcher. The cleanup ends that process
// by its pid.
func launchInJob(t *testing.T, job windows.Handle) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	launcher := exec.Command(exe, pidFile)
	launcher.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "detach-launch"})
	var stderr strings.Builder
	launcher.Stderr = &stderr
	stdin, err := launcher.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := launcher.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(launcher.Process.Pid))
	if err != nil {
		_ = launcher.Process.Kill()
		t.Fatalf("OpenProcess(launcher): %v", err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		_ = launcher.Process.Kill()
		t.Fatalf("AssignProcessToJobObject(launcher): %v", err)
	}
	// Go-ahead: the launcher starts its child only now, from inside the job.
	if _, err := io.WriteString(stdin, "g"); err != nil {
		t.Fatalf("release launcher: %v", err)
	}
	if err := launcher.Wait(); err != nil {
		t.Fatalf("the launcher failed: %v\n%s", err, stderr.String())
	}
	pid := readPIDFile(t, pidFile)
	t.Cleanup(func() { killOwnPid(pid) })
	return pid, stderr.String()
}

// TestDaemonChildLeavesTheLauncherJob: the launcher is in a job that ends its processes on
// close and allows breakaway. The job of an SSH session is such a job (limit flags
// 0x2800). The process that the launcher starts is outside that job, and it lives on after
// the job closes (row WJ01).
func TestDaemonChildLeavesTheLauncherJob(t *testing.T) {
	job := newTestJob(t, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE|windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = windows.CloseHandle(job)
		}
	})
	pid, stderr := launchInJob(t, job)
	if strings.Contains(stderr, "detached spawn failed") {
		t.Errorf("launcher stderr = %q, want no fallback line in a job that allows breakaway", stderr)
	}

	if inJob(t, pid, job) {
		t.Errorf("the detached process %d is in the job of its launcher", pid)
	}
	// The session ends: the job closes and ends every process in it.
	_ = windows.CloseHandle(job)
	closed = true
	time.Sleep(time.Second)
	if !processAlive(t, pid) {
		t.Errorf("the detached process %d ended with the job of its launcher", pid)
	}
}

// TestDaemonChildStartsInAJobWithoutBreakaway: a job that does not allow breakaway refuses a
// start with CREATE_BREAKAWAY_FROM_JOB. The start then runs again without the flag, so the
// launcher still starts its child, inside the job, and prints one line on its stderr.
// Measured on a Windows VM against 89cb6289 (row JB, a job with the limit flags 0x2000):
// the serving process stays in the job, and the launcher prints that line.
func TestDaemonChildStartsInAJobWithoutBreakaway(t *testing.T) {
	job := newTestJob(t, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = windows.CloseHandle(job)
		}
	})
	pid, stderr := launchInJob(t, job)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := "[daemon] detached spawn failed (fork/exec " + exe + ": Access is denied.); retrying without breakaway\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("launcher stderr = %q, want it to end with %q", stderr, want)
	}

	if !processAlive(t, pid) {
		t.Fatalf("the detached process %d did not start", pid)
	}
	if !inJob(t, pid, job) {
		t.Errorf("the detached process %d left a job that does not allow breakaway", pid)
	}
	_ = windows.CloseHandle(job)
	closed = true
	if !waitProcessGone(pid, 5*time.Second) {
		t.Errorf("the detached process %d outlived a job that it could not leave", pid)
	}
}

// spawnTree spawns the helper in the given tree mode through the production process
// manager and returns the managed child and the pid of its grandchild. mode "tree" gives
// the grandchild its own NUL stdio. mode "tree-stdio" lets it inherit the stdout and
// stderr of the child. The cleanups end the child and the grandchild by their recorded
// pids, also after a failed assertion: with no job, nothing else ends a grandchild.
func spawnTree(t *testing.T, m *procManager, id, mode string) (*managedProc, int) {
	t.Helper()
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, mode)
	pidFile := filepath.Join(t.TempDir(), "gpid")
	p, err := m.spawn(c, id, exe, []string{pidFile}, "", env, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { killOwnPid(p.pid) })
	if mode == "tree" {
		if !m.writeStdin(id, []byte("g")) { // go-ahead: the helper starts its grandchild now
			t.Fatal("stdin go-ahead refused")
		}
	}
	gpid := readPIDFile(t, pidFile)
	t.Cleanup(func() { killOwnPid(gpid) })
	if !processAlive(t, gpid) {
		t.Fatalf("grandchild %d not alive before the action", gpid)
	}
	return p, gpid
}

// TestKillEndsOnlyTheDirectChild: process.kill, process.killAndWait and the kill of a
// clean stop end the direct child, with exit code 1 and the "signalled at client request"
// exit line. The grandchild is alive 1 s later. Measured on a Windows VM against
// 89cb6289: only the direct child ends, and the grandchild is alive at 1 s, 5 s and 15 s.
// The rows are seven signal forms of process.kill, three forms of process.killAndWait,
// -stop and server.shutdown. claustrum gave the same states in those rows. A Job Object
// around the child ends the grandchild here.
func TestKillEndsOnlyTheDirectChild(t *testing.T) {
	type result struct{ found, died, already, escalated bool }
	actions := []struct {
		name    string
		act     func(m *procManager) result
		wantLog string
	}{
		{"kill default", func(m *procManager) result { m.kill("c1", ""); return result{} },
			"[process.Manager] Process c1 exited with code 1, signalled at client request\n"},
		{"kill TERM", func(m *procManager) result { m.kill("c1", "TERM"); return result{} },
			"[process.Manager] Process c1 exited with code 1, signalled at client request\n"},
		{"kill KILL", func(m *procManager) result { m.kill("c1", "KILL"); return result{} },
			"[process.Manager] Process c1 exited with code 1, signalled at client request\n"},
		{"killAndWait default", func(m *procManager) result {
			f, d, a, e := m.killAndWait("c1", "", 3*time.Second, true)
			return result{f, d, a, e}
		}, "[process.Manager] Process c1 exited with code 1, signalled at client request\n"},
		{"killAndWait no escalate", func(m *procManager) result {
			f, d, a, e := m.killAndWait("c1", "", 3*time.Second, false)
			return result{f, d, a, e}
		}, "[process.Manager] Process c1 exited with code 1, signalled at client request\n"},
		// The exit line of a clean stop is not measured on Windows. Only the code is.
		{"clean stop", func(m *procManager) result { m.killAll(); return result{} }, ""},
	}
	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestProcManager(t)
			p, gpid := spawnTree(t, m, "c1", "tree")
			buf := captureLogBuf(t)

			got := tc.act(m)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				t.Fatal("the direct child did not end")
			}
			if strings.HasPrefix(tc.name, "killAndWait") && got != (result{found: true, died: true}) {
				t.Errorf("killAndWait = %+v, want found and died only", got)
			}
			if log := buf.String(); !strings.HasSuffix(log, tc.wantLog) || !strings.Contains(log, "Process c1 exited with code 1") {
				t.Errorf("log = %q, want exit code 1 and the end %q", log, tc.wantLog)
			}
			if strings.Contains(buf.String(), "group kill failed") {
				t.Errorf("log = %q, want no group kill line", buf.String())
			}
			time.Sleep(time.Second)
			if !processAlive(t, gpid) {
				t.Errorf("grandchild %d ended with the direct child", gpid)
			}
		})
	}
}

// TestKillWithKidsHoldingThePipes: a grandchild holds the stdout and stderr of the child.
// A kill ends the child at once, and the exit of the managed process waits for the drain
// grace. Measured on a Windows VM against 89cb6289: process.kill answers at once and its
// exit frame comes 5.002 s later. process.killAndWait answers after 5.005 s with
// found, died and escalated, and the log holds the lines below. The graces are shrunk
// here: the kill grace ends first, then the drain grace.
func TestKillWithKidsHoldingThePipes(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 1500 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = oldDrain })

	t.Run("killAndWait", func(t *testing.T) {
		m := newTestProcManager(t)
		_, gpid := spawnTree(t, m, "c1", "tree-stdio")
		buf := captureLogBuf(t)

		start := time.Now()
		found, died, already, escalated := m.killAndWait("c1", "", 300*time.Millisecond, true)
		took := time.Since(start)

		if !found || !died || already || !escalated {
			t.Errorf("killAndWait = found %v died %v alreadyExited %v escalated %v, want true true false true", found, died, already, escalated)
		}
		if took < exitDrainGrace {
			t.Errorf("killAndWait answered after %s, want it to wait for the drain grace of %s", took, exitDrainGrace)
		}
		log := buf.String()
		wantLinesInOrder(t, log,
			"[process.Manager] KillAndWait c1: group kill failed: TerminateProcess: Access is denied.",
			"[process.Manager] Process c1: pipe drain grace expired (grandchild holding stdio?); force-closing")
		for _, w := range []string{
			"[process.Manager] stdout read error for process c1: read |0: file already closed\n",
			"[process.Manager] stderr read error for process c1: read |0: file already closed\n",
		} {
			if !strings.Contains(log, w) {
				t.Errorf("log lacks %q\nlog:\n%s", w, log)
			}
		}
		if w := "[process.Manager] Process c1 exited with code 1, signalled at client request\n"; !strings.HasSuffix(log, w) {
			t.Errorf("log does not end with %q\nlog:\n%s", w, log)
		}
		if !processAlive(t, gpid) {
			t.Errorf("grandchild %d ended", gpid)
		}
	})

	t.Run("kill", func(t *testing.T) {
		m := newTestProcManager(t)
		p, gpid := spawnTree(t, m, "c1", "tree-stdio")
		buf := captureLogBuf(t)

		start := time.Now()
		m.kill("c1", "")
		if replied := time.Since(start); replied > 500*time.Millisecond {
			t.Errorf("kill returned after %s, want at once", replied)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatal("no exit of the managed process")
		}
		if took := time.Since(start); took < exitDrainGrace {
			t.Errorf("the exit came after %s, want it after the drain grace of %s", took, exitDrainGrace)
		}
		log := buf.String()
		if strings.Contains(log, "group kill failed") {
			t.Errorf("log = %q, want no group kill line for process.kill", log)
		}
		wantLinesInOrder(t, log,
			"[process.Manager] Process c1: pipe drain grace expired (grandchild holding stdio?); force-closing")
		if w := "[process.Manager] Process c1 exited with code 1, signalled at client request\n"; !strings.HasSuffix(log, w) {
			t.Errorf("log does not end with %q\nlog:\n%s", w, log)
		}
		if !processAlive(t, gpid) {
			t.Errorf("grandchild %d ended", gpid)
		}
	})
}

// TestSecondDaemonLogSharesTheHeldFile: a live daemon holds remote-server.log open, so the
// launcher of a second daemon cannot rotate it. The launcher then opens that same file for
// append. Both daemons write to the end of it, and no line of either one is lost. Without
// that file the second daemon keeps the stdout and stderr of its launcher. An ssh command
// that started it then does not return (rows WN04 and WJ04 on a Windows VM).
func TestSecondDaemonLogSharesTheHeldFile(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	first := openDaemonLog(sock)
	if first == nil {
		t.Fatal("no log for the first daemon")
	}
	defer first.Close()
	if _, err := io.WriteString(first, "A1\n"); err != nil {
		t.Fatal(err)
	}
	second := openDaemonLog(sock)
	if second == nil {
		t.Fatal("no log file for the second daemon while the first holds the log open")
	}
	defer second.Close()
	for _, w := range []struct {
		f    *os.File
		line string
	}{{second, "B1\n"}, {first, "A2\n"}, {second, "B2\n"}} {
		if _, err := io.WriteString(w.f, w.line); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, daemonLogName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "A1\nB1\nA2\nB2\n" {
		t.Errorf("log = %q, want every line of both daemons in write order", got)
	}
}

// TestChildSurvivesAKilledDaemon: a process that owns one spawned child ends hard, as a
// daemon under taskkill /F does. The child is alive 1 s later (rows WJ02, WJ06). The test
// then ends the child by its pid.
func TestChildSurvivesAKilledDaemon(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	daemon := exec.Command(exe, pidFile)
	daemon.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "spawn-hold"})
	if err := daemon.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })
	pid := readPIDFile(t, pidFile)
	t.Cleanup(func() { killOwnPid(pid) })
	if !processAlive(t, pid) {
		t.Fatalf("child %d not alive before the kill", pid)
	}

	if err := daemon.Process.Kill(); err != nil {
		t.Fatalf("kill the owner: %v", err)
	}
	_ = daemon.Wait()
	time.Sleep(time.Second)
	if !processAlive(t, pid) {
		t.Errorf("child %d ended with its killed owner", pid)
	}
}

// TestFirstLogLineOnWindows: the first log line of a daemon start is the "not the run dir's
// lock holder" line (row K11 and every other daemon row).
func TestFirstLogLineOnWindows(t *testing.T) {
	buf := captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	// Another test can leave a child whose exit line lands in this buffer. Take the first
	// line that is not a [process.Manager] line.
	first := ""
	for _, ln := range strings.Split(buf.String(), "\n") {
		if ln != "" && !strings.Contains(ln, "[process.Manager]") {
			first = ln
			break
		}
	}
	if want := "[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none"; !strings.HasSuffix(first, want) {
		t.Errorf("first log line = %q, want %q", first, want)
	}
}
