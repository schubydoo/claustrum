//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A socket FILE left behind by a crashed daemon (path present, nobody accepting)
// dials ECONNREFUSED: that is a stale socket, so there is no predecessor to wait
// for. Any other dial failure — here EACCES on an unwritable socket file — is
// treated conservatively as "possibly live" and reports the inode.
func TestLivePredecessorIdentStaleAndUnclassifiedSocket(t *testing.T) {
	// Short directory, not t.TempDir(): this test's name pushes the socket path past
	// macOS's 104-byte sun_path limit (see harness_test.go for the same pattern).
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket file should survive Close with unlink disabled: %v", err)
	}
	if livePredecessorIdent(sock) != nil {
		t.Error("a stale socket file (connection refused) should report no predecessor")
	}

	skipIfRoot(t)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sock, 0o600) })
	if livePredecessorIdent(sock) == nil {
		t.Error("a permission-denied dial is not proof of death; must report the inode")
	}
}

// close is a documented no-op on Unix and must be safe on a nil receiver, matching
// the nil-receiver contract of signal.
func TestProcGroupCloseUnixIsNoOp(t *testing.T) {
	(*procGroup)(nil).close()
	(&procGroup{}).close()
}

// When byte shellOutputLogLimit lands mid-rune the cut walks back to the rune
// start, so the logged prefix never carries a torn code point.
func TestTruncateShellOutputWalksBackToRuneBoundary(t *testing.T) {
	// 199 ASCII bytes + a 2-byte rune: byte 200 is the rune's continuation byte.
	s := strings.Repeat("a", shellOutputLogLimit-1) + "é"
	got := truncateShellOutput(s)
	want := strings.Repeat("a", shellOutputLogLimit-1) + "..."
	if got != want {
		t.Errorf("truncateShellOutput = %q (len %d), want %q", got, len(got), want)
	}
}

// A hash over a directory fails at read time, not open time; the error surfaces.
func TestSha256FileDirectory(t *testing.T) {
	if _, err := sha256File(t.TempDir()); err == nil {
		t.Fatal("expected an error hashing a directory")
	}
}

// resolveAsFarAsExists returns the canonical path directly when the path exists
// and contains a symlink (the fast path, before any ancestor-splitting).
func TestResolveAsFarAsExistsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if got, want := resolveAsFarAsExists(link), canonicalPath(real); got != want {
		t.Errorf("resolveAsFarAsExists(link) = %q, want %q", got, want)
	}
}

// removePipeNameFileIfOwned owning an rpc.pipe it cannot unlink logs and returns;
// shutdown must not fail on a read-only socket directory.
func TestRemovePipeNameFileIfOwnedRemoveError(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	if err := writePipeNameFile(sock, `\\.\pipe\x`); err != nil {
		t.Fatal(err)
	}
	path := pipeNameFilePath(sock)
	owned, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	removePipeNameFileIfOwned(sock, owned)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should survive a failed unlink: %v", err)
	}
}
