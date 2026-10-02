//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// On Windows claustrum does no predecessor probe — livePredecessorIdent is a nil stub,
// since the probe has no observable effect there (matching the reference). Assert it
// returns nil even with a LIVE listener on the socket (where the unix implementation
// returns the socket's identity). Guards the build-tag split.
func TestLivePredecessorIdentNilOnWindows(t *testing.T) {
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
	if fi := livePredecessorIdent(sock); fi != nil {
		t.Errorf("livePredecessorIdent on Windows = %v, want nil (stub; the probe is a no-op there)", fi)
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
	stale := staleSocketIdent(sock)
	if stale == nil {
		t.Fatal("staleSocketIdent = nil with a socket file on disk")
	}

	done := make(chan bool, 1)
	go func() { done <- waitForDaemonAccept(sock, livePredecessorIdent(sock), stale) }()
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

// TestStaleSocketIdentNoFile: with no socket file on disk there is nothing to wait over.
func TestStaleSocketIdentNoFile(t *testing.T) {
	if fi := staleSocketIdent(filepath.Join(t.TempDir(), "rpc.sock")); fi != nil {
		t.Errorf("staleSocketIdent = %v with no file, want nil", fi)
	}
}
