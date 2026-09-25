//go:build unix

package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// -stop unlinks the socket path only after a FAILED connect. After a successful
// connect it leaves the path alone. Measured against f6010b97 and 90fca6e6 on a
// Linux VM:
//
//	failed connect: stale socket         removed
//	                own listener, 0000   removed (connect EACCES)
//	                regular file         removed
//	                empty directory      removed
//	                non-empty directory  kept
//	                dir not writable     kept (the unlink fails EACCES)
//	connect ok:     foreign listener     kept, and still reachable by path
//
// The live-daemon case attributes nothing, because a daemon removes its own
// socket when it stops. Only the arms with no claustrum daemon show what -stop
// itself does.
//
// unix-only on purpose: these arms turn on POSIX AF_UNIX semantics. A bound
// socket path is an ordinary directory entry that can be unlinked out from
// under a live listener. Windows does not promise that.

// stopPrints runs -stop against sock and checks the stdout bytes.
func stopPrints(t *testing.T, sock, want string) {
	t.Helper()
	var out bytes.Buffer
	runStop(sock, &out)
	if got := out.String(); got != want {
		t.Errorf("runStop printed %q, want %q", got, want)
	}
}

// stageStaleSocket binds sock and closes the listener without unlinking it, as a
// killed daemon leaves it. Go's UnixListener unlinks a path it created on Close,
// so SetUnlinkOnClose(false) is required, or the test has nothing to assert.
func stageStaleSocket(t *testing.T, sock string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("net.Listen(unix) returned %T, want *net.UnixListener", ln)
	}
	ul.SetUnlinkOnClose(false)
	if err := ul.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("could not stage a stale socket at %s: %v", sock, err)
	}
}

// The failed-connect unlink arms. Each one prints "none" (there is no
// daemon.lock) and then checks whether the path is gone.
func TestRunStopFailedConnectUnlinkRules(t *testing.T) {
	cases := []struct {
		name     string
		stage    func(t *testing.T, sock string)
		wantGone bool
	}{
		{"stale socket", stageStaleSocket, true},
		{"regular file", func(t *testing.T, sock string) {
			if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"empty directory", func(t *testing.T, sock string) {
			if err := os.Mkdir(sock, 0o700); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"non-empty directory", func(t *testing.T, sock string) {
			if err := os.Mkdir(sock, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sock, "keep"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(shortTempDir(t), "rpc.sock")
			tc.stage(t, sock)
			stopPrints(t, sock, "none\n")
			_, err := os.Lstat(sock)
			if gone := os.IsNotExist(err); gone != tc.wantGone {
				t.Errorf("after -stop: path gone = %v, want %v (Lstat err = %v)", gone, tc.wantGone, err)
			}
		})
	}
}

// A live listener whose socket refuses the connect with EACCES (mode 0000) loses
// its path, and -stop prints "none". Root ignores the mode. The connect then
// succeeds, and the arm tests nothing.
func TestRunStopUnlinksEACCESListenerPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the socket mode, so the connect does not fail")
	}
	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	stopPrints(t, sock, "none\n")
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("EACCES listener path still present after -stop (Lstat err = %v)", err)
	}
}

// A stale socket in a directory this user cannot write stays: the unlink fails
// and -stop still prints "none". The reference case was a root-owned directory.
// A 0555 directory gives the same EACCES without root.
func TestRunStopKeepsSocketInUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the directory mode, so the unlink does not fail")
	}
	dir := filepath.Join(shortTempDir(t), "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "rpc.sock")
	stageStaleSocket(t, sock)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	stopPrints(t, sock, "none\n")
	if _, err := os.Lstat(sock); err != nil {
		t.Errorf("socket in an unwritable dir is gone after -stop (Lstat err = %v), want kept", err)
	}
}

// A live listener that is NOT a claustrum daemon accepts the connect. -stop
// prints "stopped" and leaves the path in place, so the listener stays reachable
// by path. A pre-fix claustrum unlinked it (measured, 10/10), and the
// references no longer do.
func TestRunStopKeepsForeignListenerPath(t *testing.T) {
	// The foreign listener accepts and never replies, so runStop also has to sit
	// out its reply deadline. Shrink it so the test does not pay the full 2s.
	old := stopReplyTimeout
	stopReplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopReplyTimeout = old })

	sock := filepath.Join(shortTempDir(t), "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	t.Cleanup(func() {
		for {
			select {
			case c := <-accepted:
				_ = c.Close()
			default:
				return
			}
		}
	})

	stopPrints(t, sock, "stopped\n")

	select {
	case c := <-accepted:
		_ = c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("the foreign listener never accepted -stop's connection")
	}

	// The path must still reach the same listener. A dial that the listener
	// accepts proves both that the path exists and that it is still bound.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial by path after -stop: %v. The path must stay reachable", err)
	}
	_ = c.Close()
	select {
	case c := <-accepted:
		_ = c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("the listener did not accept the dial by path after -stop")
	}
}
