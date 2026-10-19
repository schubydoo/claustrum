//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// statAtCloseConn is a client connection whose Close records whether the socket
// file still exists at that moment. Only Close is called by the teardown.
type statAtCloseConn struct {
	net.Conn
	socket  string
	closed  bool
	statErr error
}

func (c *statAtCloseConn) Close() error {
	_, c.statErr = os.Stat(c.socket)
	c.closed = true
	return nil
}

// The stop tests the socket right after the listener close, before it closes a
// client connection (cells L1 to L3 of round task091-01, Linux VM). So when the
// first connection closes, the socket of this daemon is already gone.
func TestCloseAllRemovesSocketBeforeConnections(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })
	fake := &statAtCloseConn{socket: sock}
	s.mu.Lock()
	s.conns[&conn{nc: fake}] = struct{}{}
	s.mu.Unlock()

	s.signalShutdown()
	s.closeAll(sock)
	if !fake.closed {
		t.Fatal("closeAll did not close the client connection")
	}
	if !os.IsNotExist(fake.statErr) {
		t.Errorf("at the close of a client connection the socket stat gave %v, want not exist: the socket test comes first", fake.statErr)
	}
}

// The stop ends the process groups of its children before it removes its token and
// before it releases daemon.lock (cell L1k of round task091-01, Linux VM, 3 of 3
// runs). The seam records the state of both at the kill of the child.
func TestCloseAllKillsChildrenBeforeTokenAndLock(t *testing.T) {
	var kills, tokenAtKill, releasedAtKill atomic.Int32
	var released atomic.Bool
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "rpc.sock")
	token := filepath.Join(dir, persistedTokenName)
	// The seam is set before the daemon starts, so no goroutine of the daemon reads
	// it while the test writes it.
	realSignal := signalGroup
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
		if signame == "KILL" {
			kills.Add(1)
			if _, err := os.Stat(token); err == nil {
				tokenAtKill.Add(1)
			}
			if released.Load() {
				releasedAtKill.Add(1)
			}
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })

	s, err := newServerOnSocket(sock, testToken, "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })
	t.Cleanup(func() { _ = s.ln.Close() })
	if s.releaseRunDir == nil {
		t.Fatal("the daemon has no run-dir release")
	}
	release := s.releaseRunDir
	s.releaseRunDir = func() {
		released.Store(true)
		release()
	}
	s.startAcceptLoops()
	spawnSleeper(t, dial(t, sock), "c1")

	s.signalShutdown()
	s.closeAll(sock)
	if kills.Load() != 1 {
		t.Fatalf("the stop sent %d group kills, want 1", kills.Load())
	}
	if tokenAtKill.Load() != 1 {
		t.Error("daemon.token was already gone at the kill of the child group: the kill comes first")
	}
	if releasedAtKill.Load() != 0 {
		t.Error("daemon.lock was already released at the kill of the child group: the kill comes first")
	}
	if !released.Load() {
		t.Error("closeAll did not release daemon.lock")
	}
	if _, err := os.Stat(token); !os.IsNotExist(err) {
		t.Errorf("daemon.token after the stop: %v, want removed", err)
	}
}
