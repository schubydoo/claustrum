//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// The fixtures of the host cleaner tests that tests of package main use too. The host
// cleaner tests moved into internal/hostsweep with their own copies.

// fakeProc points procinfo.Root at a temp tree and returns a helper to populate a pid's files.
// The tree has an empty locks file: a lock table that can be read and names no holder.
func fakeProc(t *testing.T) (root string, mk func(pid int, name, content string), link func(pid int, name, target string)) {
	t.Helper()
	root = t.TempDir()
	old := procinfo.Root()
	t.Cleanup(func() { procinfo.SetRootForTest(old) })
	procinfo.SetRootForTest(root)
	if err := os.WriteFile(filepath.Join(root, "locks"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mk = func(pid int, name, content string) {
		d := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link = func(pid int, name, target string) {
		p := filepath.Join(root, strconv.Itoa(pid), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	return root, mk, link
}

// hcStartSleeper starts a helper of this test binary that sleeps, and returns its command.
// The cleanup ends it by its own handle.
func hcStartSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	cmd := exec.Command(exe, "60")
	cmd.Env = buildEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

// hcWaitSignal waits for the helper and returns the signal that ended it.
func hcWaitSignal(t *testing.T, cmd *exec.Cmd) syscall.Signal {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper ended with %v, want a signal", err)
		}
		return ee.Sys().(syscall.WaitStatus).Signal()
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not end within 10 s of the signal")
		return 0
	}
}
