//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The reap of a git call whose stdout a child holds after git exited. 5fd08069
// keeps git a zombie until its stdout closes (cells A1 and A3), or until the 30 s
// bound of a config listing on standard input (cells B1 and B2). Rounds
// task101-03 (Linux VM) and task082-02 (macOS VM).

// procState is the state letter of the process pid, or "" when no such process
// exists. On Linux it reads /proc. Elsewhere it runs ps.
func procState(t *testing.T, pid int) string {
	t.Helper()
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return ""
		}
		rest := string(b[bytes.LastIndexByte(b, ')')+1:])
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0]
		}
		return ""
	}
	// BSD ps adds flags after the state letter, such as "Z+" or "Ss".
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if st := strings.TrimSpace(string(out)); st != "" {
		return st[:1]
	}
	return ""
}

// requireProcState skips the test when the state of a process cannot be read.
func requireProcState(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/self/stat"); err != nil {
			t.Skip("no /proc on this host: the state of a process cannot be read")
		}
		return
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps on this host: the state of a process cannot be read")
	}
}

// heldStdoutCall is a call of the helper "held-stdout" whose child holds the
// stdout for hold seconds. It returns the command and the file of the two pids.
// The child is ended at the end of the test.
func heldStdoutCall(t *testing.T, hold int) (*exec.Cmd, string) {
	t.Helper()
	exe, env := helperCommand(t, "held-stdout")
	pids := filepath.Join(t.TempDir(), "pids")
	cmd := exec.Command(exe, pids, strconv.Itoa(hold))
	cmd.Env = buildEnv(env)
	t.Cleanup(func() {
		b, err := os.ReadFile(pids)
		if err != nil {
			return
		}
		if f := strings.Fields(string(b)); len(f) == 2 {
			if pid, err := strconv.Atoi(f[1]); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	return cmd, pids
}

// gitPid reads the pid of the git call from the file of heldStdoutCall.
func gitPid(t *testing.T, pids string) int {
	t.Helper()
	var pid int
	if !pollUntil(5*time.Second, func() bool {
		b, err := os.ReadFile(pids)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.Fields(string(b))[0])
		return err == nil
	}) {
		t.Fatal("the git call wrote no pid")
	}
	return pid
}

// TestHeldStdoutReapsGitAtPipeEnd: a call under untilPipeEOF exits 0 while its
// child holds the stdout. git stays a zombie until the child ends, and it is
// gone when the call returns (cells A1 and A3). Mutation: in
// runToEOF, call cmd.Wait before the read of stdout.
func TestHeldStdoutReapsGitAtPipeEnd(t *testing.T) {
	requireProcState(t)
	const hold = 5
	cmd, pids := heldStdoutCall(t, hold)
	var out bytes.Buffer
	cmd.Stdout = &out
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- gitRunLimited(&gitStop{}, listingLimit, true, cmd) }()
	pid := gitPid(t, pids)
	if !pollUntil(hold*time.Second/2, func() bool { return procState(t, pid) == "Z" }) {
		t.Errorf("git (pid %d) is %q while its child holds the stdout, want a zombie (Z)", pid, procState(t, pid))
	}
	select {
	case err := <-done:
		t.Fatalf("the call returned %v while the child holds the stdout", err)
	default:
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the call did not return after the child ended")
	}
	if took := time.Since(start); took < hold*time.Second {
		t.Errorf("the call returned after %v, want it after the child ended (%d s)", took, hold)
	}
	if st := procState(t, pid); st != "" {
		t.Errorf("git (pid %d) is %q after the call returned, want it reaped", pid, st)
	}
	if err != nil || out.String() != "early\n" {
		t.Errorf("the call = %q, %v, want %q and no error", out.String(), err, "early\n")
	}
}

// TestHeldStdoutReapsGitAtStartBound: a config listing on standard input exits
// 0 while its child holds the stdout past the bound. git stays a zombie until
// the bound and is reaped then, and the call returns startBoundDrain later
// (cells B1 and B2: reaped at about 30 s, the reply at about 31 s).
// Mutation: in runStartBound, call cmd.Wait before the select, or wait for the
// end of stdout alone there.
func TestHeldStdoutReapsGitAtStartBound(t *testing.T) {
	requireProcState(t)
	const bound, hold = 3 * time.Second, 8
	cmd, pids := heldStdoutCall(t, hold)
	var out bytes.Buffer
	cmd.Stdout = &out
	start := time.Now()
	type result struct {
		ended bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		ended, err := runStartBound(cmd, []byte("[a]\n\tb = c\n"), bound)
		done <- result{ended, err}
	}()
	pid := gitPid(t, pids)
	if !pollUntil(bound/2, func() bool { return procState(t, pid) == "Z" }) {
		t.Errorf("git (pid %d) is %q before the bound, want a zombie (Z)", pid, procState(t, pid))
	}
	if !pollUntil(bound+3*time.Second, func() bool { return procState(t, pid) == "" }) {
		t.Fatalf("git (pid %d) is %q after the bound, want it reaped", pid, procState(t, pid))
	}
	if reaped := time.Since(start); reaped > bound+time.Second {
		t.Errorf("git was reaped %v after the start, want it at the bound (%v)", reaped, bound)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the call did not return")
	}
	took := time.Since(start)
	if !r.ended {
		t.Errorf("the call = %v, not ended, want the bound to end it", r.err)
	}
	if took < bound+startBoundDrain/2 || took > hold*time.Second-time.Second {
		t.Errorf("the call returned after %v, want it about %v after the start", took, bound+startBoundDrain)
	}
}

// TestHeldStdoutReapsGitWithNoLimits: a call under untilPipeEOF in a request
// with no time limits, such as a listing of git.worktree_remove, reaps git at
// the end of its stdout too (from the code, not measured). Mutation: in
// gitOutput, run such a call with cmd.Output.
func TestHeldStdoutReapsGitWithNoLimits(t *testing.T) {
	requireProcState(t)
	const hold = 5
	cmd, pids := heldStdoutCall(t, hold)
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := gitOutput(untilPipeEOF(context.Background()), cmd)
		done <- result{out, err}
	}()
	pid := gitPid(t, pids)
	if !pollUntil(hold*time.Second/2, func() bool { return procState(t, pid) == "Z" }) {
		t.Errorf("git (pid %d) is %q while its child holds the stdout, want a zombie (Z)", pid, procState(t, pid))
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the call did not return after the child ended")
	}
	if st := procState(t, pid); st != "" {
		t.Errorf("git (pid %d) is %q after the call returned, want it reaped", pid, st)
	}
	if r.err != nil || string(r.out) != "early\n" {
		t.Errorf("the call = %q, %v, want %q and no error", r.out, r.err, "early\n")
	}
}
