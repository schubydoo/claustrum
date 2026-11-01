package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// noFolderHandle takes the /proc route away for one test.
func noFolderHandle(t *testing.T) {
	t.Helper()
	old := procSelfFD
	procSelfFD = filepath.Join(t.TempDir(), "none")
	t.Cleanup(func() { procSelfFD = old })
}

// The limit of Linux is the one in the text of `be1c1ca5` (measured on Linux).
func TestMaxSocketPathLinux(t *testing.T) {
	if maxSocketPath != 107 {
		t.Errorf("maxSocketPath = %d, want 107", maxSocketPath)
	}
}

// With /proc the long path goes by /proc/self/fd/<n>/<base> of a handle of the
// folder, as `be1c1ca5` does (measured on Linux, strace).
func TestFolderHandleNameUsesProc(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	var got string
	ln, ok, err := folderHandleName(filepath.Dir(sock), "rpc.sock", func(name string) (net.Listener, error) {
		got = name
		return net.Listen("unix", name)
	})
	if !ok || err != nil {
		t.Fatalf("folderHandleName: ok=%v err=%v", ok, err)
	}
	keepSocketFileAtClose(ln)
	defer ln.Close()
	rest, found := strings.CutPrefix(got, "/proc/self/fd/")
	n, _, _ := strings.Cut(rest, "/")
	if _, aerr := strconv.Atoi(n); !found || aerr != nil || !strings.HasSuffix(got, "/rpc.sock") {
		t.Errorf("bound by %q, want /proc/self/fd/<n>/rpc.sock", got)
	}
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("no socket at the real path: %v", err)
	}
}

// A name that does not lead to the folder of the handle is not used.
func TestFolderHandleNameRefusesAMissingProc(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	called := false
	_, ok, _ := folderHandleName(filepath.Dir(sock), "rpc.sock", func(name string) (net.Listener, error) {
		called = true
		return net.Listen("unix", name)
	})
	if ok || called {
		t.Errorf("used a /proc name with no /proc: ok=%v called=%v", ok, called)
	}
}

// With no /proc a long path still listens and dials by a change of folder, and the
// working folder is the same after. `be1c1ca5` does that with a stand-in /proc
// (measured on Linux).
func TestLongSocketPathWithoutProc(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	wd := mustGetwd(t)
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatalf("listen with no /proc: %v", err)
	}
	defer ln.Close()
	if mustGetwd(t) != wd {
		t.Fatal("the listen did not change back")
	}
	acceptOnce(ln)
	dialReadsX(t, sock, dialUnixPath)
	if got := mustGetwd(t); got != wd {
		t.Errorf("working folder %s after the dial, want %s", got, wd)
	}
}

// With /proc the daemon dials a long path with no change of folder.
func TestDialInDaemonThroughProc(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acceptOnce(ln)
	dialReadsX(t, sock, dialSocketInDaemon)
	var tooLong *socketTooLongError
	if _, err := dialSocketInDaemon(sock+"x", time.Second); errors.As(err, &tooLong) {
		t.Errorf("a missing long path gave the too-long error: %v", err)
	}
}

// A stand-in /proc whose handle names lead to another folder: the /proc route is
// not used, and nothing is made in the other folder.
func TestFolderHandleNameRefusesAnotherFolder(t *testing.T) {
	fake := shortTempDir(t)
	other := shortTempDir(t)
	for n := 0; n < 1024; n++ {
		if err := os.Symlink(other, filepath.Join(fake, strconv.Itoa(n))); err != nil {
			t.Fatal(err)
		}
	}
	old := procSelfFD
	procSelfFD = fake
	t.Cleanup(func() { procSelfFD = old })
	sock := longSocketPath(t, 120, "rpc.sock")
	called := false
	_, ok, _ := folderHandleName(filepath.Dir(sock), "rpc.sock", func(name string) (net.Listener, error) {
		called = true
		return net.Listen("unix", name)
	})
	if ok || called {
		t.Errorf("used a /proc name that leads to another folder: ok=%v called=%v", ok, called)
	}
	if _, err := os.Lstat(filepath.Join(other, "rpc.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("made an entry in the other folder: %v", err)
	}
}
