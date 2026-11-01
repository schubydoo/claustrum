//go:build unix

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// bootLongOrphanServer boots a listening daemon at a 120-byte socket path, with its
// accept loops running.
func bootLongOrphanServer(t *testing.T) (*server, string) {
	t.Helper()
	sock := longSocketPath(t, 120, "rpc.sock")
	s, err := newServerOnSocket(sock, "orphan-tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	s.startAcceptLoops()
	t.Cleanup(func() {
		s.signalShutdown()
		s.closeAll(sock)
	})
	return s, sock
}

// replaceWithPlainFile puts a plain file at the socket path with the given mtime.
func replaceWithPlainFile(t *testing.T, sock string, mtime time.Time) {
	t.Helper()
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sock, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// runOrphanChecks runs the checks at the given minutes and returns the minute of
// the shutdown, or -1.
func runOrphanChecks(s *server, sock string, minutes ...int) int {
	base := monoNow()
	s.lastCounted.Store(int64(base))
	var w selfExitWatch
	for _, m := range minutes {
		if s.selfExitCheck(sock, &w, base+time.Duration(m)*time.Minute) {
			return m
		}
	}
	return -1
}

const tooLongNotOrphaned = " is too long for this daemon to dial, but its file is unmodified since this daemon bound it; not orphaned"

// A path that the daemon cannot dial, with a plain file of the bound mtime at it:
// not orphaned, and the daemon stays. `be1c1ca5` logs this line and keeps the
// daemon and its child on macOS (measured).
func TestOrphanCheckLongPathFileUnmodified(t *testing.T) {
	noFolderHandle(t)
	s, sock := bootLongOrphanServer(t)
	replaceWithPlainFile(t, sock, s.sockInfo.ModTime())
	buf := captureLogBuf(t)
	if m := runOrphanChecks(s, sock, 0, 10, 11, 21, 22, 23); m >= 0 {
		t.Fatalf("shut down at minute %d:\n%s", m, buf.String())
	}
	got := buf.String()
	if !strings.Contains(got, "[Server] socket path "+sock+tooLongNotOrphaned+"\n") {
		t.Errorf("no too-long line:\n%s", got)
	}
	if strings.Contains(got, "did not lead back") {
		t.Errorf("counted a failed probe:\n%s", got)
	}
}

// Another mtime counts as a failed probe, and the daemon shuts down after 11m0s, as
// on macOS against `be1c1ca5` (measured).
func TestOrphanCheckLongPathFileModified(t *testing.T) {
	noFolderHandle(t)
	s, sock := bootLongOrphanServer(t)
	replaceWithPlainFile(t, sock, s.sockInfo.ModTime().Add(time.Hour))
	buf := captureLogBuf(t)
	if m := runOrphanChecks(s, sock, 0, 10, 11); m != 11 {
		t.Fatalf("shutdown at minute %d, want 11:\n%s", m, buf.String())
	}
	got := buf.String()
	if strings.Contains(got, tooLongNotOrphaned) || !strings.Contains(got, "[Server] orphaned for 11m0s (") {
		t.Errorf("want the orphan lines and no too-long line:\n%s", got)
	}
}
