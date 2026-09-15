//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
