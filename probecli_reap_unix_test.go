//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProbeCLIRunnableReapsDescendants pins claustrum's -probe-cli teardown: when the
// deadline kills a hung `<cli> --version`, the probe's descendants are reaped, not
// just the direct child. The fixture sh backgrounds a long sleeper (a grandchild of
// the probe), records its pid, then blocks so the shrunk deadline fires. With the
// process group + group-kill cancel, the sleeper is reaped; a plain CommandContext
// (the mutant) SIGKILLs only the direct sh, and the sleeper is reparented to PID 1
// and survives — the leak slowCLI's comment measured.
func TestProbeCLIRunnableReapsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// `sleep 10 &` is a grandchild of the probe (child of sh). 10s is a leak budget:
	// if the group teardown regresses, the sleeper self-exits in 10s instead of
	// lingering for the rest of the run.
	script := "#!/bin/sh\nsleep 10 &\necho $! > " + pidFile + "\nsleep 10\n"
	cli := filepath.Join(dir, "cli.sh")
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// 2s (not a tighter bound): the fixture sh must reach `echo $! > pidfile` before
	// the deadline fires, and macOS shell startup is ~10x slower than Linux — a
	// sub-second deadline flakes on a loaded macOS runner. The sleeper lives 10s, so
	// 2s still fires well inside it and the test finishes in ~2s.
	setProbeCLITimeout(t, 2*time.Second)

	if v := probeCLIRunnable(cli); v != probeCLIHung {
		t.Fatalf("verdict = %d, want probeCLIHung (%d)", v, probeCLIHung)
	}

	pid := readGrandchildPID(t, pidFile)

	// probeCLIRunnable's cancel SIGKILLed the group before it returned, so the
	// sleeper should already be gone (init reaps the orphan). Poll briefly for that;
	// still alive after the window means the group teardown regressed and the
	// descendant leaked.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return // reaped — the whole group died
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL) // do not leak the sleeper
			t.Fatalf("grandchild pid %d still alive: the deadline did not tear down the process group", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readGrandchildPID polls for the fixture's pid file (sh writes it right after
// backgrounding the sleeper) and parses the recorded pid.
func readGrandchildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && pid > 1 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid file %s never became readable", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
