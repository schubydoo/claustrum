//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// nofileTarget returns the soft limit raiseInheritedFileLimit aims for, or skips
// when the hard limit is too low to observe the raise or the platform caps the soft
// limit below the target (some macOS configurations). It leaves the soft limit
// lowered to 4096 (restored by the caller's t.Cleanup) so a subsequent raise is
// observable.
func nofileTargetLowered(t *testing.T, orig syscall.Rlimit) uint64 {
	t.Helper()
	want := min(uint64(65536), orig.Max)
	const low = 4096
	if want <= low {
		t.Skipf("hard limit %d too low to observe the raise", orig.Max)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: want, Max: orig.Max}); err != nil {
		t.Skipf("platform caps RLIMIT_NOFILE below %d: %v", want, err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: low, Max: orig.Max}); err != nil {
		t.Skipf("cannot lower the soft limit: %v", err)
	}
	return want
}

// TestRaiseInheritedFileLimit proves the daemon raises its own soft RLIMIT_NOFILE to
// min(hard, 65536). A no-op raiseInheritedFileLimit would leave it at the lowered 4096.
func TestRaiseInheritedFileLimit(t *testing.T) {
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Skipf("Getrlimit unsupported: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig) })
	want := nofileTargetLowered(t, orig)

	raiseInheritedFileLimit()

	var got syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cur != want {
		t.Errorf("soft limit = %d, want %d", got.Cur, want)
	}
}

// TestRaiseInheritedFileLimitChildInherits proves the load-bearing behavior: a child
// process spawned after the raise inherits the raised soft limit, not the daemon's
// lowered one. The soft limit is lowered to 4096 first, so a no-op raise would make
// the child report 4096 instead of the target. This exercises the Go runtime's
// across-exec limit handling that carries the raise to the child.
//
// The child is a shell, not the Go test binary: a Go process re-raises its own soft
// limit at runtime startup, which would mask the value it inherited. `ulimit` is a
// shell builtin, so a shell is required. The command is a fixed literal with no
// interpolation.
func TestRaiseInheritedFileLimitChildInherits(t *testing.T) {
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Skipf("Getrlimit unsupported: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig) })
	want := nofileTargetLowered(t, orig)

	raiseInheritedFileLimit()

	out, err := exec.Command("sh", "-c", "ulimit -Sn").CombinedOutput()
	if err != nil {
		t.Skipf("cannot run the child shell: %v (%s)", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != strconv.FormatUint(want, 10) {
		t.Errorf("child soft limit = %q, want %d", got, want)
	}
}

// TestRaiseInheritedFileLimitErrors covers the read-error and fallback paths via the
// getRlimit/setRlimit seams. A Getrlimit failure attempts no Setrlimit; a failed
// first Setrlimit falls back to the original limit.
func TestRaiseInheritedFileLimitErrors(t *testing.T) {
	origGet, origSet := getRlimit, setRlimit
	t.Cleanup(func() { getRlimit, setRlimit = origGet, origSet })

	// Getrlimit fails: no Setrlimit is attempted.
	setCalls := 0
	getRlimit = func(int, *syscall.Rlimit) error { return syscall.EPERM }
	setRlimit = func(int, *syscall.Rlimit) error { setCalls++; return nil }
	raiseInheritedFileLimit()
	if setCalls != 0 {
		t.Errorf("Getrlimit error: setRlimit called %d times, want 0", setCalls)
	}

	// First Setrlimit fails: the fallback restores the original limit.
	var attempts []syscall.Rlimit
	getRlimit = func(_ int, l *syscall.Rlimit) error { *l = syscall.Rlimit{Cur: 1024, Max: 8192}; return nil }
	firstFailed := false
	setRlimit = func(_ int, l *syscall.Rlimit) error {
		attempts = append(attempts, *l)
		if !firstFailed {
			firstFailed = true
			return syscall.EINVAL
		}
		return nil
	}
	raiseInheritedFileLimit()
	if len(attempts) != 2 {
		t.Fatalf("want 2 Setrlimit attempts, got %d", len(attempts))
	}
	if attempts[0].Cur != min(uint64(65536), 8192) {
		t.Errorf("first attempt Cur = %d, want %d", attempts[0].Cur, min(uint64(65536), 8192))
	}
	if attempts[1].Cur != 1024 {
		t.Errorf("fallback Cur = %d, want 1024 (original)", attempts[1].Cur)
	}
}
