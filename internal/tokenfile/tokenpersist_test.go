package tokenfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// errInjected drives Save's write/close failure arms, which a real
// os.CreateTemp file can't be made to hit on demand.
var errInjected = errors.New("injected failure")

// fakeTokenTemp is a TempFile whose WriteString/Close can be told to fail.
// name points at a real temp file so the error arms' os.Remove(tmp) still cleans
// up, letting the test assert no temp leaks.
type fakeTokenTemp struct {
	name     string
	writeErr error
	closeErr error
}

func (f *fakeTokenTemp) Name() string                    { return f.name }
func (f *fakeTokenTemp) WriteString(string) (int, error) { return 0, f.writeErr }
func (f *fakeTokenTemp) Close() error                    { return f.closeErr }

// injectTokenTemp swaps createTokenTemp for one returning the given fake, backed
// by a real temp file in dir, and restores it at test end.
func injectTokenTemp(t *testing.T, dir string, writeErr, closeErr error) {
	t.Helper()
	real, err := os.CreateTemp(dir, "daemon.token-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = real.Close()
	restore := createTokenTemp
	createTokenTemp = func(string) (TempFile, error) {
		return &fakeTokenTemp{name: real.Name(), writeErr: writeErr, closeErr: closeErr}, nil
	}
	t.Cleanup(func() { createTokenTemp = restore })
}

// assertNoPersistedArtifacts fails if daemon.token or a daemon.token-* temp
// survives in dir — the invariant after any Save failure arm.
func assertNoPersistedArtifacts(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "daemon.token")); !os.IsNotExist(err) {
		t.Fatalf("daemon.token must not exist after a persist failure (err=%v)", err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "daemon.token-") {
			t.Fatalf("temp file leaked after a persist failure: %q", e.Name())
		}
	}
}

// Save must write <dir(socket)>/daemon.token containing the raw token
// verbatim (no trailing newline) at mode 0600, and DropOwn must
// unlink it — the reconnect contract added upstream in reference build 5db5e4a.
// The file lives beside the SOCKET (not the -token-file); probe-verified against
// the reference, which writes to the socket's dir even when the token-file is
// elsewhere.
func TestPersistTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	const token = "TKN-abc-123+/=" // includes base64 chars; must be byte-exact

	owned := Save(socket, token)
	if owned == nil {
		t.Fatal("Save returned nil FileInfo for a successful write")
	}

	path := filepath.Join(dir, "daemon.token")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("daemon.token not written next to socket: %v", err)
	}
	if string(b) != token {
		t.Fatalf("token bytes = %q, want %q (no newline, verbatim)", b, token)
	}
	// os.CreateTemp yields 0600 on POSIX; Windows has no Unix permission bits
	// (Go reports 0666), so the mode assertion is POSIX-only.
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil {
			t.Fatalf("stat: %v", err)
		} else if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("mode = %o, want 600", perm)
		}
	}

	// No stray temp files (daemon.token-*) left behind after the atomic rename.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "daemon.token" && e.Name() != "rpc.sock" {
			t.Fatalf("unexpected leftover in socket dir: %q", e.Name())
		}
	}

	DropOwn(socket, owned)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("daemon.token still present after DropOwn (err=%v)", err)
	}
}

// DropOwn must LEAVE the file when a restart's successor has already republished
// daemon.token to a new inode — the departing predecessor's owned FileInfo no longer
// os.SameFile-matches, so it must not delete the successor's token out from under the new
// daemon. Extends claustrum's socket-ownership guard (unlinkOwnSocket) to daemon.token.
// The successor's inode is forced distinct by creating it ALONGSIDE the original, then
// renaming into place (a delete-recreate could reuse the freed inode). On Windows it is
// measured: 5fd08069 keeps the token of the successor. An identity from os.Stat there
// reads the file id by the path at the compare, so it named the successor's file and the
// token went.
func TestRemovePersistedTokenLeavesSuccessor(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	path := filepath.Join(dir, "daemon.token")

	owned := Save(socket, "PRED") // predecessor's token + identity
	if owned == nil {
		t.Fatal("Save returned nil")
	}
	// Successor republishes daemon.token with a DISTINCT inode (coexist then rename).
	succ := filepath.Join(dir, "daemon.token.succ")
	if err := os.WriteFile(succ, []byte("SUCC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(succ, path); err != nil {
		t.Fatal(err)
	}

	// The departing predecessor must NOT delete the successor's token.
	DropOwn(socket, owned)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("successor's daemon.token was deleted by the predecessor: %v", err)
	}
	if string(b) != "SUCC" {
		t.Fatalf("daemon.token = %q, want the successor's SUCC", b)
	}
	// And the current owner (successor's identity) DOES remove it.
	cur, _ := os.Stat(path)
	DropOwn(socket, cur)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("DropOwn did not remove its own token")
	}
}

// DropOwn is a silent no-op when there is nothing this daemon owns to
// remove: a nil owned (persist failed → nothing was written), and a non-nil owned
// whose file is now absent (teardown ran twice, or a successor removed it). Neither
// must error or panic.
func TestRemovePersistedTokenAbsent(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "rpc.sock")
	DropOwn(socket, nil) // nothing persisted → no-op
	// A non-nil owned whose file no longer exists: stat fails → no-op.
	owned := Save(socket, "tok")
	_ = os.Remove(filepath.Join(filepath.Dir(socket), "daemon.token"))
	DropOwn(socket, owned) // file gone; must not panic
}

// StatOpen fails when the path does not open, and then returns no identity.
func TestStatOpenFileMissingPath(t *testing.T) {
	fi, err := StatOpen(filepath.Join(t.TempDir(), "absent"))
	if err == nil || fi != nil {
		t.Errorf("StatOpen of an absent path = %v, %v, want nil and an error", fi, err)
	}
}

// Save is best-effort: if the socket's directory does not exist (so the
// temp-file create fails), it logs and returns without panicking, and the daemon
// keeps serving — only file-based reconnect is unavailable.
func TestPersistTokenCreateFailureIsNonFatal(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "no-such-subdir", "rpc.sock")
	Save(socket, "tok") // dir missing → CreateTemp errors; must not panic
	if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatalf("Save must not create the missing directory (err=%v)", err)
	}
}

// Save's rename-failure arm is best-effort: if the final path can't be
// replaced (here daemon.token pre-exists as a directory, so renaming a file onto
// it fails on every platform), it logs, cleans up the temp file, and returns —
// no panic, the existing entry is untouched, and no daemon.token-* temp leaks.
func TestPersistTokenRenameFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	if err := os.Mkdir(filepath.Join(dir, "daemon.token"), 0o700); err != nil {
		t.Fatal(err)
	}
	Save(socket, "tok") // temp create/write/close ok; rename onto a dir fails

	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == "daemon.token" && !e.IsDir() {
			t.Fatal("daemon.token should remain the pre-existing directory")
		}
		if strings.HasPrefix(e.Name(), "daemon.token-") {
			t.Fatalf("temp file leaked after a failed rename: %q", e.Name())
		}
	}
}

// DropOwn's remove-failure arm is best-effort: when daemon.token
// exists but can't be unlinked (here it's a non-empty directory, so os.Remove
// fails on every platform), it logs and returns without panicking, and the entry
// survives. stat succeeds, so this exercises the remove-error path specifically.
func TestRemovePersistedTokenRemoveFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	tokDir := filepath.Join(dir, "daemon.token")
	if err := os.Mkdir(tokDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokDir, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// owned must os.SameFile-match the on-disk entry for the remove to be attempted,
	// so hand it the directory's own identity; os.Remove of a non-empty dir then fails.
	owned, err := os.Stat(tokDir)
	if err != nil {
		t.Fatal(err)
	}
	out := captureLog(t, func() { DropOwn(socket, owned) }) // stat ok, SameFile matches; remove fails
	if !strings.Contains(out, "[daemon] failed to remove persisted token: ") {
		t.Errorf("log %q lacks the line of the failed token remove", out)
	}
	if _, err := os.Stat(tokDir); err != nil {
		t.Fatalf("non-empty daemon.token dir should survive a failed remove: %v", err)
	}
}

// Save's write-failure arm: WriteString errors, so the temp is closed +
// removed, the failure is logged, and no daemon.token appears. Best-effort — the
// caller (runServe) keeps serving.
func TestPersistTokenWriteFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	injectTokenTemp(t, dir, errInjected, nil)
	Save(filepath.Join(dir, "rpc.sock"), "tok")
	assertNoPersistedArtifacts(t, dir)
}

// Save's close-failure arm: WriteString succeeds but Close errors, so the
// temp is removed, the failure is logged, and no daemon.token appears.
func TestPersistTokenCloseFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	injectTokenTemp(t, dir, nil, errInjected)
	Save(filepath.Join(dir, "rpc.sock"), "tok")
	assertNoPersistedArtifacts(t, dir)
}

// DropOwn's stat-error arm: when the socket dir is actually a file,
// os.Stat(<file>/daemon.token) fails (ENOTDIR on POSIX). Any stat failure is a silent
// no-op (DropOwn logs only a failed unlink of a file it still owns) — it
// must not panic. A non-nil owned reaches the stat that then fails.
func TestRemovePersistedTokenStatErrorIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := os.Stat(notDir)
	if err != nil {
		t.Fatal(err)
	}
	DropOwn(filepath.Join(notDir, "rpc.sock"), owned) // stat under a non-dir; must not panic
}

// Save overwrites any stale daemon.token from a prior daemon in the same
// dir — the atomic rename replaces it, so a reconnecting client reads the live
// token, never a dead one.
func TestPersistTokenOverwritesStale(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	path := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(path, []byte("STALE"), 0o600); err != nil {
		t.Fatal(err)
	}
	Save(socket, "FRESH")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "FRESH" {
		t.Fatalf("token = %q, want FRESH (stale not overwritten)", b)
	}
}
