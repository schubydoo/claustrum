//go:build unix

package sockroute

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir returns a /tmp-rooted dir so AF_UNIX paths stay under the macOS
// sun_path limit (t.TempDir embeds the long test name).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// longSocketPath returns a path of exactly total bytes under a fresh temp folder
// that ends in base. base can hold slashes. The folder of the path exists.
func longSocketPath(t *testing.T, total int, base string) string {
	t.Helper()
	root := shortTempDir(t)
	pad := total - len(root) - len(base) - 2
	if pad < 1 || pad > 255 {
		t.Fatalf("cannot build a %d-byte path under %s with base %d bytes", total, root, len(base))
	}
	p := filepath.Join(root, strings.Repeat("d", pad), base)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if len(p) != total {
		t.Fatalf("built a %d-byte path, want %d", len(p), total)
	}
	return p
}

// acceptOnce answers the first connection on ln with "x".
func acceptOnce(ln net.Listener) {
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, "x")
		_ = c.Close()
	}()
}

// dialReadsX dials sock with dial and reads the "x" of acceptOnce.
func dialReadsX(t *testing.T, sock string, dial func(string, time.Duration) (net.Conn, error)) {
	t.Helper()
	c, err := dial(sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer c.Close()
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "x" {
		t.Fatalf("read %q, %v from the listener, want \"x\"", b, err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// A 120-byte socket path with a short last element listens, the socket file is in
// the real folder, a dial reaches it, and the working folder is the same after.
// `be1c1ca5` serves at 120 bytes on Linux and 110 bytes on macOS (measured).
func TestListenAndDialAtALongSocketPath(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	wd := mustGetwd(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("listen at a 120-byte path: %v", err)
	}
	defer ln.Close()
	fi, err := os.Lstat(sock)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no socket file at the real path: %v, %v", fi, err)
	}
	acceptOnce(ln)
	dialReadsX(t, sock, DialUnixPath)
	if got := mustGetwd(t); got != wd {
		t.Errorf("working folder %s after the listen and dial, want %s", got, wd)
	}
}

// A last element over the limit fails with the text of `be1c1ca5`, and a last
// element of exactly the limit listens and dials.
func TestLongSocketLastElementLimit(t *testing.T) {
	over := longSocketPath(t, 200, strings.Repeat("s", MaxSocketPath+1))
	want := fmt.Sprintf("unix %s: the socket path is longer than the %d bytes that this system allows", over, MaxSocketPath)
	if _, err := Listen(over); err == nil || err.Error() != "listen "+want {
		t.Errorf("listen error %v, want %q", err, "listen "+want)
	}
	if _, err := DialUnixPath(over, time.Second); err == nil || err.Error() != "dial "+want {
		t.Errorf("dial error %v, want %q", err, "dial "+want)
	}
	if _, err := os.Lstat(over); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused path left an entry: %v", err)
	}

	at := longSocketPath(t, 200, strings.Repeat("s", MaxSocketPath))
	ln, err := Listen(at)
	if err != nil {
		t.Fatalf("listen with a last element of %d bytes: %v", MaxSocketPath, err)
	}
	defer ln.Close()
	acceptOnce(ln)
	dialReadsX(t, at, DialUnixPath)
}

// The dial of a running daemon never changes folder. Where only that route is left
// it fails with TooLongError.
func TestDialInDaemonWithNoFolderHandle(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = DialSocketInDaemon(sock, time.Second)
	var tooLong *TooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("dial in the daemon: %v, want the too-long error", err)
	}
	acceptOnce(ln)
	dialReadsX(t, sock, DialUnixPath)
}

// A long path in a missing folder fails, and the working folder stays.
func TestLongSocketPathMissingFolder(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	missing := filepath.Join(filepath.Dir(sock)+"x", "rpc.sock")
	wd := mustGetwd(t)
	if ln, err := Listen(missing); err == nil {
		_ = ln.Close()
		t.Fatalf("listen in a missing folder %s worked", missing)
	}
	if got := mustGetwd(t); got != wd {
		t.Errorf("working folder %s after the failed listen, want %s", got, wd)
	}
}
