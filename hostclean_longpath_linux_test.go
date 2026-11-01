//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The probe of the host cleaner dials a socket path over 107 bytes through /proc, as
// the daemon does. A live listener gives its peer, and a dead socket reads as stale.
func TestProbeSocketAtALongPath(t *testing.T) {
	sock := longSocketPath(t, 120, "run/00000002/rpc.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	if state, pid, _ := probeSocket(sock); state != hcSockLive || pid != os.Getpid() {
		t.Errorf("live long socket: state %d pid %d, want %d and pid %d", state, pid, hcSockLive, os.Getpid())
	}
	_ = ln.Close() // a listener bound by another name keeps its file
	if state, _, _ := probeSocket(sock); state != hcSockStale {
		t.Errorf("dead long socket: state %d, want %d", state, hcSockStale)
	}
}

// A dead run dir 40 days old behind a 120-byte socket path, with an empty daemon.lock
// that no process holds, goes with the measured line.
func TestTidyRemovesADeadRunDirAtALongRoot(t *testing.T) {
	fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	sock := longSocketPath(t, 120, "run/00000002/rpc.sock")
	dir := filepath.Dir(sock)
	runRoot := filepath.Dir(dir)
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	if err := os.WriteFile(filepath.Join(dir, runDirLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	e := hcEntry(t, runRoot, root, "00000002", 40*24*time.Hour)
	buf := captureLogBuf(t)

	var sum hcSummary
	c.tidyRunDirs([]runDirEntry{e}, &sum)

	want := fmt.Sprintf("[hostclean] removed run dir %q: unused for 40 days, nothing answers on its socket, no process holds its lock\n", dir)
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
	if sum.runDirsRemoved != 1 {
		t.Errorf("runDirsRemoved = %d, want 1", sum.runDirsRemoved)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the dead run dir is still there: %v", err)
	}
}

// A pass whose own socket is 120 bytes long reaches its own daemon through /proc and goes on
// to the orphan sweep: `be1c1ca5` ends an orphaned group at a long root (measured on Linux).
func TestPassAtALongOwnSocketReachesTheOrphanSweep(t *testing.T) {
	realDial := hcDial
	f := hcNewPassFixture(t)
	ownSock := longSocketPath(t, 120, "run/abcd1234/rpc.sock")
	ln, err := listenSocket(ownSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	base := filepath.Dir(filepath.Dir(filepath.Dir(ownSock)))
	f.c.roots = &hostRoots{roots: []string{base}, daemonBin: "server"}
	f.c.ownSocket, f.c.ownRunDir = ownSock, filepath.Dir(ownSock)
	hcDial = func(addr string) (net.Conn, error) {
		if addr == ownSock {
			return realDial(addr)
		}
		return f.dial(addr)
	}
	orphan := os.Getpid() + 1
	hcFakeDaemonUID(t, f.proot, f.mk, f.link, orphan, filepath.Join(base, "ccd-cli", "2.1.0"),
		[]string{"claude", "--output-format=stream-json"}, true, "1", f.uid)
	hcPipeStdio(f.link, orphan)
	buf := captureLogBuf(t)

	f.c.Pass()

	if len(f.group) != 1 || f.group[0] != orphan {
		t.Errorf("group signals = %v, want the orphan %d", f.group, orphan)
	}
	want := fmt.Sprintf("[hostclean] ending orphaned Claude Code process group %d (its daemon is gone): SIGTERM", orphan)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
}
