//go:build unix

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// waitForDaemonAccept must return as soon as the socket accepts a connection.
func TestWaitForDaemonAcceptReturnsWhenListening(t *testing.T) {
	dir, err := os.MkdirTemp("", "wda")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	done := make(chan struct{})
	go func() { waitForDaemonAccept(sock, make(chan struct{})); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("waitForDaemonAccept did not return for a listening socket")
	}
}

// A child that dies before listening must end the wait immediately, not after
// the full deadline. This is what keeps a doomed start fast: with the socket
// path occupied the reference's launcher returns in ~0.07s, not ~10s.
func TestWaitForDaemonAcceptReturnsWhenChildExits(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 30 * time.Second // long enough that a deadline return is obvious
	t.Cleanup(func() { daemonStartTimeout = old })

	exited := make(chan struct{})
	close(exited)

	start := time.Now()
	waitForDaemonAccept(filepath.Join(t.TempDir(), "never-exists.sock"), exited)
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("waited %v for an already-exited child, want an immediate return", el)
	}
}

// With neither a listener nor a child exit, the wait is bounded by
// daemonStartTimeout rather than blocking forever.
func TestWaitForDaemonAcceptGivesUpAtDeadline(t *testing.T) {
	old := daemonStartTimeout
	daemonStartTimeout = 250 * time.Millisecond
	t.Cleanup(func() { daemonStartTimeout = old })

	start := time.Now()
	waitForDaemonAccept(filepath.Join(t.TempDir(), "never-exists.sock"), make(chan struct{}))
	el := time.Since(start)
	if el < 200*time.Millisecond {
		t.Errorf("returned after %v, want at least the %v deadline", el, daemonStartTimeout)
	}
	if el > 5*time.Second {
		t.Errorf("returned after %v, want bounded by the %v deadline", el, daemonStartTimeout)
	}
}

// -serve must create a missing socket directory (0700) and come back only once
// the daemon is accepting. Measured against the reference at 5db5e4a: it leaves
// d sub(700) / rpc.sock(600) / daemon.token(600) / remote-server.log(600) and the
// socket is already present the instant the launcher returns. claustrum used to
// refuse to start at all when the directory was absent, and when it did start it
// returned before the socket existed.
func TestServeCreatesSocketDirAndWaitsForAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}
	bin := buildClaustrum(t)

	// Short base path keeps the AF_UNIX path under the macOS sun_path limit.
	base, err := os.MkdirTemp("", "svs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	sub := filepath.Join(base, "sub") // deliberately absent
	sock := filepath.Join(sub, "rpc.sock")
	tokFile := filepath.Join(base, "tok")
	if err := os.WriteFile(tokFile, []byte("tok-serve-startup"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-file", tokFile)
	cmd.Env = append(os.Environ(), "CLAUDE_SSH_DAEMON_CHILD=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("-serve launcher failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if c, err := net.Dial("unix", sock); err == nil {
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"tok-serve-startup"}` + "\n"))
			_ = c.Close()
		}
	})

	// The launcher has returned. The socket must ALREADY be connectable — no
	// polling here, that is the whole point of the assertion.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("socket not accepting at the instant -serve returned: %v", err)
	}
	_ = c.Close()

	fi, err := os.Stat(sub)
	if err != nil {
		t.Fatalf("socket directory was not created: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("socket dir mode = %04o, want 0700", got)
	}
}
