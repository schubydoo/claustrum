//go:build unix

package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server.shutdown signals the teardown, and the teardown closes every connection
// before its slower cleanup. The handler returns its {"ok":true} reply only after
// dropConns starts, so the reply write races the close. The reply reaches the
// client only when its write wins. An idle connection gets a clean EOF.
//
// These tests run the production closeAll on a daemon booted in-process
// (newServerOnSocket, as the lifecycle tests do). No os.Exit runs, and every
// socket is in a temp dir.

// waitConns waits until the daemon has registered n connections and returns them.
func waitConns(t *testing.T, s *server, n int) []*conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		cs := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			cs = append(cs, c)
		}
		s.mu.Unlock()
		if len(cs) == n {
			return cs
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon has %d connections, want %d", len(cs), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The structure, pinned without a race: when the teardown reaches its cleanup
// (here, the run-dir lock release), every client connection is already closed.
// A pre-fix closeAll closed the connections last, after the lock release and
// the child teardown.
func TestShutdownTeardownClosesConnsBeforeCleanup(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })
	// A failed run still stops the accept loop.
	t.Cleanup(func() { _ = s.ln.Close() })
	s.startAcceptLoops()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	c := waitConns(t, s, 1)[0]

	var writeErr error
	probed := false
	release := s.releaseRunDir
	s.releaseRunDir = func() {
		// An empty write on an open conn succeeds. On a closed one it fails.
		_, writeErr = c.nc.Write(nil)
		probed = true
		if release != nil {
			release()
		}
	}
	s.signalShutdown()
	s.closeAll(sock)
	if !probed {
		t.Fatal("closeAll did not run the run-dir release")
	}
	if writeErr == nil {
		t.Error("a client connection was still open when the teardown released the run-dir lock. The teardown must close connections first")
	}
}

// The wire shape, end to end. The shutdown connection gets its ping reply, then
// either nothing or the exact {"ok":true} frame, then EOF. Which one is a race,
// so the test accepts both. A second idle connection gets a clean EOF with no
// bytes. The daemon removes its socket and daemon.token.
func TestShutdownReplyRacesTeardown(t *testing.T) {
	const want = `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}` + "\n"
	for i := 0; i < 10; i++ {
		dir := shortTempDir(t)
		sock := filepath.Join(dir, "s.sock")
		s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
		if err != nil {
			t.Fatalf("newServerOnSocket: %v", err)
		}
		t.Cleanup(func() { s.procs.killAll() })
		s.startAcceptLoops()
		torn := make(chan struct{})
		go func() {
			<-s.shutdown
			s.closeAll(sock)
			close(torn)
		}()
		// A failed run still tears the daemon down, so no goroutine or listener
		// outlives it.
		t.Cleanup(func() {
			s.signalShutdown()
			select {
			case <-torn:
			case <-time.After(5 * time.Second):
				_ = s.ln.Close()
			}
		})

		idle, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		b, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		waitConns(t, s, 2)
		br := bufio.NewReader(b)
		if _, err := io.WriteString(b, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"tok"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		pong, err := br.ReadString('\n')
		if err != nil || !strings.Contains(pong, `"pong":true`) {
			t.Fatalf("ping reply = %q (err %v), want pong", pong, err)
		}
		// Unauthenticated, as -stop sends it.
		if _, err := io.WriteString(b, `{"jsonrpc":"2.0","id":2,"method":"server.shutdown","params":{}}`+"\n"); err != nil {
			t.Fatal(err)
		}
		_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
		rest, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("shutdown connection: no clean EOF: %v", err)
		}
		if got := string(rest); got != "" && got != want {
			t.Errorf("run %d: bytes after the ping reply = %q, want nothing or %q", i, got, want)
		}
		_ = idle.SetReadDeadline(time.Now().Add(5 * time.Second))
		idleBytes, err := io.ReadAll(idle)
		if err != nil || len(idleBytes) != 0 {
			t.Errorf("run %d: idle connection got %q (err %v), want a clean EOF with no bytes", i, idleBytes, err)
		}
		_ = idle.Close()
		_ = b.Close()

		select {
		case <-torn:
		case <-time.After(5 * time.Second):
			t.Fatal("teardown did not finish")
		}
		for _, p := range []string{sock, filepath.Join(dir, persistedTokenName)} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Errorf("run %d: %s survived the teardown (Lstat err = %v)", i, filepath.Base(p), err)
			}
		}
	}
}
