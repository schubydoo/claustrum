//go:build linux || darwin

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// untilExit reads frames until the exit frame and returns the stdout bytes before it.
func untilExit(t *testing.T, frames <-chan streamFrame) string {
	t.Helper()
	var out []byte
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return string(out)
			}
			if f.Stream == "stdout" {
				b, err := base64.StdEncoding.DecodeString(f.Data)
				if err != nil {
					t.Fatalf("frame data is not base64: %v", err)
				}
				out = append(out, b...)
			}
		case <-deadline:
			t.Fatal("no exit frame")
		}
	}
}

// TestExitLineAfterClientKill pins the exited line of a child that a client request
// ended and that exited with code 0: ", signalled at client request" (89cb6289, Linux
// rows SP03a and SP03b). Before, the line ended at the code.
func TestExitLineAfterClientKill(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "term-exit0")
	p, err := m.spawn(c, "c1", exe, nil, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// "ready" comes after the helper installed its SIGTERM handler.
	select {
	case f := <-frames:
		if f.Stream != "stdout" {
			t.Fatalf("first frame is %q, want the ready line on stdout", f.Stream)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the helper never became ready")
	}
	p.signalIfLive("TERM", "client")
	untilExit(t, frames)
	if want := "[process.Manager] Process c1 exited with code 0, signalled at client request\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
}

// TestExitLineAfterOutsideSIGKILL pins the exited line of a child that a signal from
// outside the daemon ended: ", terminated by SIGKILL" (89cb6289, Linux row SP04). The
// test sends the signal to the one process it started itself.
func TestExitLineAfterOutsideSIGKILL(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	p, err := m.spawn(c, "c1", exe, []string{"60"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	untilExit(t, frames)
	if want := "[process.Manager] Process c1 exited with code -1, terminated by SIGKILL\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
}

// TestTrampolineStartFailureAnswersWithDirectStart pins a spawn whose trampoline does
// not start, with a cwd of mode 000: the daemon logs the trampoline line, starts the
// command directly, and answers with the error of that direct start, which names the
// command (89cb6289, Linux rows CW03 and CW03x2). Before, the frame named the
// trampoline. No record is written.
func TestTrampolineStartFailureAnswersWithDirectStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can enter a folder of mode 000")
	}
	logs := captureLogBuf(t)
	runDir := t.TempDir()
	d000 := filepath.Join(t.TempDir(), "d000")
	if err := os.Mkdir(d000, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d000, 0o700) })
	m := newTestProcManager(t)
	m.runDir = runDir // a run dir makes spawn go through the trampoline
	exe, env := helperCommand(t, "echo")

	_, err := m.spawn(nil, "p1", exe, []string{"hi"}, d000, env, false)
	if want := "fork/exec " + exe + ": permission denied"; err == nil || err.Error() != want {
		t.Fatalf("spawn error = %v, want %q", err, want)
	}
	want := []string{
		"[process.Manager] exec trampoline failed for p1 (fork/exec " + trampolineSelf() + ": permission denied); starting it directly — a successor daemon will not be able to reap it\n",
		"[process.Manager] Failed to start process p1: fork/exec " + exe + ": permission denied\n",
	}
	out := logs.String()
	first, second := strings.Index(out, want[0]), strings.Index(out, want[1])
	if first < 0 || second < first {
		t.Errorf("want the trampoline line and then the Failed to start line, got:\n%s", out)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "children")); !os.IsNotExist(err) {
		t.Errorf("a children folder exists after a spawn that did not start (lstat error: %v)", err)
	}
}

// TestTrampolineStartFailureDirectStartRuns covers the direct start that then works.
// No row measures it. claustrum's own choice: the child runs with the run dir entry
// and with no CLAUDE_SSH_CHILD entry, which only the trampoline can stamp.
func TestTrampolineStartFailureDirectStartRuns(t *testing.T) {
	// The test process can itself be a child of a daemon. An empty inherited entry
	// prints the same as an absent one.
	t.Setenv(envChildMarker, "")
	logs := captureLogBuf(t)
	old := trampolineSelf
	t.Cleanup(func() { trampolineSelf = old })
	noSelf := filepath.Join(t.TempDir(), "no-such-daemon")
	trampolineSelf = func() string { return noSelf }

	runDir := t.TempDir()
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	m.runDir = runDir
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "printenvs")
	// A Go runtime entry in the spawn env. The trampoline wrap moves it to a held name
	// in place. The direct start must get the env from before the wrap: the entry
	// under its own name, and no held twin.
	env["GOTRACEBACK"] = "all"
	held := heldEnvPrefix + "GOTRACEBACK"
	// The wrap can change the env in place only when its slice has spare room. The
	// strip of this daemon entry leaves that room.
	t.Setenv(managedSettingsDirEnv, t.TempDir())
	if _, err := m.spawn(c, "p1", exe, []string{envRunDir, envChildMarker, "GOTRACEBACK", held}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got, want := untilExit(t, frames), envRunDir+"="+runDir+"\n"+envChildMarker+"=\nGOTRACEBACK=all\n"+held+"=\n"; got != want {
		t.Errorf("child printed %q, want %q", got, want)
	}
	if want := "[process.Manager] exec trampoline failed for p1 (fork/exec " + noSelf + ": no such file or directory); starting it directly"; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
}
