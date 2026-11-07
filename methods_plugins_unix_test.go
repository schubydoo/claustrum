//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginsPruneUnreadableRunDir: a run dir that the daemon cannot list names no
// sibling, so it gives the sibling text and not the text of another name shape. A
// mutant that dropped the test of an empty sibling name gives the other text here.
func TestPluginsPruneUnreadableRunDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the modes")
	}
	// A short temp root, not t.TempDir(): the socket path must stay under the macOS
	// sockaddr_un limit (~104 bytes).
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(base, "run")
	t.Cleanup(func() {
		_ = os.Chmod(runDir, 0o755)
		_ = os.RemoveAll(base)
	})
	if err := os.MkdirAll(filepath.Join(runDir, "c0ffee01"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(runDir, "c0ffee01", "rpc.sock"))
	if err != nil {
		t.Skipf("AF_UNIX listen unsupported here: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Search but no read: the own socket stays reachable and the listing fails.
	if err := os.Chmod(runDir, 0o300); err != nil {
		t.Fatal(err)
	}
	res := mustPrune(t, &server{ln: ln, procs: newTestProcManager(t)})
	if want := "skipped: another daemon is running (cannot read " + runDir + ": "; !strings.HasPrefix(res.Legacy, want) {
		t.Errorf("Legacy = %q, want prefix %q", res.Legacy, want)
	}
}

// TestSiblingDaemonAliveUnstattableSocket is the one per-socket arm that is genuinely
// POSIX-shaped: a self-referential symlink fails to stat with ELOOP, which is neither
// not-found nor not-a-directory, so the sibling cannot be ruled out and the shared legacy
// plugins are kept. Windows reports ERROR_CANT_RESOLVE_FILENAME for the same fixture and
// needs the symlink privilege to build it, so that leg is left out.
//
// The other two per-socket arms are NOT unix-only and live in the untagged
// TestSiblingDaemonAliveDialArms, where the Windows leg runs them too.
func TestSiblingDaemonAliveUnstattableSocket(t *testing.T) {
	base, sibRun := mkSiblingRunDir(t, "cccccccc")
	sock := filepath.Join(sibRun, "rpc.sock")
	if err := os.Symlink(sock, sock); err != nil {
		t.Fatal(err)
	}
	got := siblingDaemonAlive(base, siblingOwnID)
	if !strings.HasPrefix(got, "cannot inspect "+sock+": ") {
		t.Errorf("unstattable socket: got %q, want the cannot-inspect verdict for %s", got, sock)
	}
}
