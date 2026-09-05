//go:build unix

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCapServer listens on path and answers each connection's one request with a
// server.capabilities reply carrying instanceID. It returns a counter of how many
// probes it received, so a test can assert exactly how many self-probes ran. It stands
// in for a SUCCESSOR daemon that rebound the socket: a real, live socket whose
// instanceId is not ours.
func fakeCapServer(t *testing.T, path, instanceID string) *int64 {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("fake listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var count int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt64(&count, 1)
			go func(c net.Conn) {
				defer c.Close()
				_, _ = bufio.NewReader(c).ReadBytes('\n') // consume the request
				_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":1,"result":{"instanceId":%q}}`+"\n", instanceID)
			}(c)
		}
	}()
	return &count
}

func TestPathReachesUsInstanceIDMismatch(t *testing.T) {
	s, sock := bootOrphanServer(t)
	dir := filepath.Dir(sock)
	// A different daemon (foreign instanceId) answering on another path: not us.
	other := filepath.Join(dir, "other.sock")
	fakeCapServer(t, other, "ffffffffffffffffffffffffffffffff")
	if s.pathReachesUs(other) {
		t.Error("pathReachesUs must be false when the reply instanceId is not ours")
	}
	// A server that echoes OUR instanceId: reaches us.
	mine := filepath.Join(dir, "mine.sock")
	fakeCapServer(t, mine, s.instanceID)
	if !s.pathReachesUs(mine) {
		t.Error("pathReachesUs must be true when the reply instanceId matches ours")
	}
}

// TestExitWhenOrphanedRequiresTwoProbes rebinds the socket to a foreign live daemon
// (different instanceId) and asserts the orphaned daemon shuts down only after exactly
// orphanProbeFailuresToExit self-probes. It kills two mutants at once: a pathReachesUs
// that always claims success (the daemon would then never shut down and the select
// times out) and a probe count reduced to one (the foreign server would see one probe,
// not two).
func TestExitWhenOrphanedRequiresTwoProbes(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 10*time.Millisecond)
	s, sock := bootOrphanServer(t)
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	probes := fakeCapServer(t, sock, "ffffffffffffffffffffffffffffffff") // successor with a foreign id
	startOrphanLoop(t, s, sock)
	select {
	case <-s.shutdown:
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned daemon facing a foreign successor did not shut down")
	}
	// Assert a literal 2, not orphanProbeFailuresToExit: comparing against the constant
	// under test would move the expectation with any mutation of it.
	if got := atomic.LoadInt64(probes); got != 2 {
		t.Errorf("shut down after %d self-probes, want exactly 2", got)
	}
}

// shrinkOrphanTimers makes the orphan check fire fast for tests and restores the
// production durations afterward.
func shrinkOrphanTimers(t *testing.T, interval, grace time.Duration) {
	t.Helper()
	oi, og := orphanCheckInterval, orphanGrace
	orphanCheckInterval, orphanGrace = interval, grace
	t.Cleanup(func() { orphanCheckInterval, orphanGrace = oi, og })
}

// bootOrphanServer boots a real listening daemon on a temp socket, with its accept
// loops running so a self-probe can reach it.
func bootOrphanServer(t *testing.T) (*server, string) {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
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

// startOrphanLoop runs exitWhenOrphaned and registers a cleanup that signals shutdown
// and waits for the goroutine to fully return. Registered after shrinkOrphanTimers /
// bootOrphanServer, so it runs FIRST (LIFO) — the goroutine stops touching the shrunk
// timer vars and the server before those cleanups restore/close them.
func startOrphanLoop(t *testing.T, s *server, sock string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() { s.exitWhenOrphaned(sock); close(done) }()
	t.Cleanup(func() {
		s.signalShutdown()
		<-done
	})
	return done
}

func waitConnCount(t *testing.T, s *server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.connCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("connCount stayed below %d", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSocketLooksOrphaned(t *testing.T) {
	s, sock := bootOrphanServer(t)
	if s.socketLooksOrphaned(sock) {
		t.Error("a freshly bound socket must not look orphaned")
	}
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	if !s.socketLooksOrphaned(sock) {
		t.Error("a removed socket must look orphaned")
	}
	// Rebound: a different inode at the same path.
	f, err := os.Create(sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if !s.socketLooksOrphaned(sock) {
		t.Error("a socket path rebound to a different inode must look orphaned")
	}
}

func TestPathReachesUs(t *testing.T) {
	s, sock := bootOrphanServer(t)
	if !s.pathReachesUs(sock) {
		t.Error("pathReachesUs must be true against our own live socket")
	}
	if s.pathReachesUs(sock + ".nope") {
		t.Error("pathReachesUs must be false when the dial fails")
	}
}

func TestExitWhenOrphanedShutsDownWhenSocketGone(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 20*time.Millisecond)
	s, sock := bootOrphanServer(t)
	if err := os.Remove(sock); err != nil { // gone: orphaned, and the self-probe will fail
		t.Fatal(err)
	}
	startOrphanLoop(t, s, sock)
	select {
	case <-s.shutdown:
		// shut itself down, as an orphaned daemon with no clients must
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned daemon with no clients did not shut down")
	}
}

func TestExitWhenOrphanedResetsWhileConnected(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 20*time.Millisecond)
	s, sock := bootOrphanServer(t)
	_ = dial(t, sock) // a real, tracked connection (dial registers its own cleanup)
	waitConnCount(t, s, 1)
	if err := os.Remove(sock); err != nil { // would be orphaned, but a client is attached
		t.Fatal(err)
	}
	startOrphanLoop(t, s, sock)
	select {
	case <-s.shutdown:
		t.Fatal("shut down despite a connected client")
	case <-time.After(300 * time.Millisecond):
		// stayed up across ~30 intervals because a client was connected throughout
	}
}

func TestExitWhenOrphanedProbeReachesUsResets(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 20*time.Millisecond)
	s, sock := bootOrphanServer(t)
	// Make socketLooksOrphaned true (sockInfo points at a different file) while the real
	// socket is still live, so the self-probe reaches us and the daemon does NOT exit.
	other := filepath.Join(filepath.Dir(sock), "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	s.sockInfo = fi
	startOrphanLoop(t, s, sock)
	select {
	case <-s.shutdown:
		t.Fatal("shut down even though the self-probe still reaches this daemon")
	case <-time.After(300 * time.Millisecond):
		// the probe matched our instanceId every grace window, so it kept resetting
	}
}

func TestExitWhenOrphanedNoOpWhenDisabled(t *testing.T) {
	shrinkOrphanTimers(t, 10*time.Millisecond, 20*time.Millisecond)
	s, sock := bootOrphanServer(t)
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	s.instanceID = "" // guard: an empty instanceId disables the self-probe entirely
	done := make(chan struct{})
	go func() { s.exitWhenOrphaned(sock); close(done) }()
	select {
	case <-done:
		// returned at once (no-op)
	case <-time.After(1 * time.Second):
		t.Fatal("exitWhenOrphaned did not no-op with an empty instanceId")
	}
	select {
	case <-s.shutdown:
		t.Fatal("the no-op path still shut the daemon down")
	default:
	}
}
