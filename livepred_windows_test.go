//go:build windows

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// On Windows livePredecessorPID is 0 even with a live listener on the socket, so the
// launcher wait ends at the first connect that works. 5fd08069 exits 0 there while the
// path still leads to the predecessor (round task006-01, cell C3, Windows VM).
func TestLivePredecessorPIDZeroOnWindows(t *testing.T) {
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	l, err := net.Listen("unix", sock) // a live daemon would be accepting here
	if err != nil {
		t.Fatalf("listen(unix): %v", err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	old := daemonStartTimeout
	daemonStartTimeout = 2 * time.Second
	t.Cleanup(func() { daemonStartTimeout = old })
	if pid := livePredecessorPID(sock); pid != 0 {
		t.Errorf("livePredecessorPID on Windows = %d, want 0", pid)
	}
	if ok, saw := waitForDaemonAccept(sock, livePredecessorPID(sock)); !ok || saw {
		t.Errorf("waitForDaemonAccept over a live listener = (%v, %v), want (true, false)", ok, saw)
	}
}

// TestLauncherWaitsOverAStaleSocket: the stale socket file of a killed daemon is on disk.
// The launcher wait does not take that file for the socket of the new daemon. It waits
// until the new daemon binds, and dials it once. In rows WN03 and WN05 on a Windows VM,
// the log of the new daemon holds one connection pair right after the listening line.
func TestLauncherWaitsOverAStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	old, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen(unix): %v", err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false) // a killed daemon leaves its socket file
	_ = old.Close()

	done := make(chan bool, 1)
	go func() { ok, _ := waitForDaemonAccept(sock, livePredecessorPID(sock)); done <- ok }()
	select {
	case <-done:
		t.Fatal("the launcher wait returned over the stale socket file, before a new daemon bound")
	case <-time.After(300 * time.Millisecond):
	}

	// The new daemon: remove the stale file, bind, accept.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen(unix): %v", err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("the launcher wait gave up although the new daemon bound")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the launcher wait did not return after the new daemon bound")
	}
	// The dial of the launcher returns when the connection is in the backlog. Wait until the
	// accept loop counted it, then settle, then count.
	for deadline := time.Now().Add(5 * time.Second); accepted.Load() < 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := accepted.Load(); n != 1 {
		t.Errorf("the new daemon got %d connections from the launcher, want 1", n)
	}
}

// On Windows a failed chmod of the socket does not end the start: it prints one line
// and returns no error. The listener and the socket path are not used.
func TestSocketChmodFailedGoesOnOnWindows(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	got := socketChmodFailed(nil, "", nil, errors.New("seam: chmod refused"))
	os.Stderr = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	if got != nil {
		t.Errorf("socketChmodFailed = %v, want nil on Windows", got)
	}
	if want := "claustrum: chmod socket: seam: chmod refused\n"; !strings.Contains(string(out), want) {
		t.Errorf("stderr = %q, want %q", out, want)
	}
}
