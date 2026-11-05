//go:build linux

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The fixtures of the host cleaner tests that tests of package main use too. The host
// cleaner tests moved into internal/hostsweep with their own copies.

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
