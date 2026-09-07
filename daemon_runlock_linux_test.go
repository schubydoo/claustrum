//go:build linux

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNodeIDShapeLinux(t *testing.T) {
	// When the host exposes boot_id, nodeID MUST be non-empty: an empty node makes
	// every signal guard refuse, silently disabling eviction across the whole suite,
	// so this must FAIL (not skip) rather than mistake a broken nodeID for a
	// no-/proc host.
	bootIDPresent := false
	if _, err := os.Stat("/proc/sys/kernel/random/boot_id"); err == nil {
		bootIDPresent = true
	}
	got := nodeID()
	if bootIDPresent {
		if got == "" {
			t.Fatal("nodeID is empty on a host that exposes /proc/sys/kernel/random/boot_id")
		}
		if !strings.Contains(got, "/") {
			t.Errorf("nodeID %q lacks the boot_id/ns-pid separator", got)
		}
	} else if got != "" {
		t.Errorf("nodeID = %q without a boot_id source, want empty", got)
	}
}

// TestPidNamespaceRefusalLinux covers the two reachable branches: our own pid is in our
// namespace (no refusal), and an absent pid reads as ENOENT = holder gone (no refusal).
// The cross-namespace refusal direction needs a second pid namespace, which is not
// portably stageable in a unit test; in production the isServeCmdline guard backs it up
// (a pid in another namespace maps to a different or absent /proc entry here, so its
// cmdline is not our -serve).
func TestPidNamespaceRefusalLinux(t *testing.T) {
	if r := pidNamespaceRefusal(os.Getpid()); r != "" {
		t.Errorf("pidNamespaceRefusal(self) = %q, want no refusal", r)
	}
	if r := pidNamespaceRefusal(1 << 30); r != "" {
		t.Errorf("pidNamespaceRefusal(absent pid) = %q, want no refusal (holder gone)", r)
	}
}

// skipUnlessPidUninspectable skips unless /proc/<pid>/ns/pid is PRESENT but unreadable
// — what a root-owned pid looks like to an unprivileged user, and the only way to stage
// the "holder cannot be inspected" arm without a second pid namespace. As root every pid
// is inspectable, and in a container the pid may not exist at all.
func skipUnlessPidUninspectable(t *testing.T, pid int) {
	t.Helper()
	skipIfRoot(t)
	if _, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/ns/pid"); err == nil || os.IsNotExist(err) {
		t.Skipf("/proc/%d/ns/pid is not a present-but-unreadable holder here (err=%v)", pid, err)
	}
}

// TestPidNamespaceRefusalUninspectableLinux covers the third arm, between "in our
// namespace" and "gone": a readlink that fails for a reason OTHER than ENOENT proves
// nothing about the holder, so it must refuse rather than wave the signal through.
// pid 2 (kthreadd on a normal kernel) is root-owned, so the read is EACCES for us.
func TestPidNamespaceRefusalUninspectableLinux(t *testing.T) {
	skipUnlessPidUninspectable(t, 2)
	if r := pidNamespaceRefusal(2); r != "holder cannot be inspected" {
		t.Errorf("pidNamespaceRefusal(2) = %q, want the inspection refusal", r)
	}
}

// The same fixture through the whole guard: a record that clears every earlier check
// (serve role, a usable pid that is not ours, our own node) must still be refused when
// the pid-namespace read cannot inspect the holder. isServeCmdline sits AFTER that
// guard, so it is seamed true — a refusal here can only come from the namespace check.
func TestHolderSignalRefusalUninspectablePidLinux(t *testing.T) {
	skipUnlessPidUninspectable(t, 2)
	self := nodeID()
	if self == "" {
		t.Skip("no /proc node identity on this host")
	}
	old := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = old })

	r := ownerRecord{Pid: 2, Role: "serve", Node: self}
	if got := holderSignalRefusal(r, "/run/x/s.sock"); got != "holder cannot be inspected" {
		t.Errorf("holderSignalRefusal(uninspectable pid) = %q, want the pid-namespace refusal", got)
	}
}

// TestRealIsServeCmdlineLinux exercises the unseamed /proc reader: our own process is
// not a -serve daemon, and a child whose argv carries our basename plus -serve/-socket
// is recognised.
func TestRealIsServeCmdlineLinux(t *testing.T) {
	const sock = "/run/claustrum-test/s.sock"
	if realIsServeCmdline(os.Getpid(), sock) {
		t.Error("our own test process must not look like a -serve daemon")
	}

	// Re-exec the test binary as a plain sleeper, but with -serve/-socket in its argv so
	// /proc/<pid>/cmdline carries our basename and the flags realIsServeCmdline checks.
	// CLAUSTRUM_TEST_HELPER=sleep steers it into helper mode (no daemon, no fork bomb);
	// the -serve/-socket tokens are ignored by the sleep fixture.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "60", "-serve", "-socket="+sock)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-reaped })

	deadline := time.Now().Add(3 * time.Second)
	for !realIsServeCmdline(cmd.Process.Pid, sock) {
		if time.Now().After(deadline) {
			t.Fatal("realIsServeCmdline never recognised the crafted -serve child")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRealIsServeCmdlineAbsentPidLinux covers the read-error arm: an absent pid has no
// /proc/<pid>/cmdline, so the reader fails and the holder cannot be our serve process.
func TestRealIsServeCmdlineAbsentPidLinux(t *testing.T) {
	if realIsServeCmdline(1<<30, "/run/x/s.sock") {
		t.Error("an absent pid whose cmdline cannot be read must not match")
	}
}
