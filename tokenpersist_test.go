package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// errInjected drives persistToken's write/close failure arms, which a real
// os.CreateTemp file can't be made to hit on demand.
var errInjected = errors.New("injected failure")

// fakeTokenTemp is a tokenTempFile whose WriteString/Close can be told to fail.
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
	createTokenTemp = func(string) (tokenTempFile, error) {
		return &fakeTokenTemp{name: real.Name(), writeErr: writeErr, closeErr: closeErr}, nil
	}
	t.Cleanup(func() { createTokenTemp = restore })
}

// assertNoPersistedArtifacts fails if daemon.token or a daemon.token-* temp
// survives in dir — the invariant after any persistToken failure arm.
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

// persistToken must write <dir(socket)>/daemon.token containing the raw token
// verbatim (no trailing newline) at mode 0600, and removePersistedToken must
// unlink it — the reconnect contract added upstream in reference build 5db5e4a.
// The file lives beside the SOCKET (not the -token-file); probe-verified against
// the reference, which writes to the socket's dir even when the token-file is
// elsewhere.
func TestPersistTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	const token = "TKN-abc-123+/=" // includes base64 chars; must be byte-exact

	persistToken(socket, token)

	path := filepath.Join(dir, "daemon.token")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("daemon.token not written next to socket: %v", err)
	}
	if string(b) != token {
		t.Fatalf("token bytes = %q, want %q (no newline, verbatim)", b, token)
	}
	// os.CreateTemp yields 0600 on POSIX; Windows has no Unix permission bits
	// (Go reports 0666), so the mode assertion is POSIX-only — same platform
	// caveat the reference daemon inherits from the Go runtime.
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

	removePersistedToken(socket)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("daemon.token still present after removePersistedToken (err=%v)", err)
	}
}

// removePersistedToken on an absent file is a silent no-op (persist may have
// failed, or teardown ran after another cleanup) — it must not error or panic.
func TestRemovePersistedTokenAbsent(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "rpc.sock")
	removePersistedToken(socket) // no daemon.token exists; must not panic
}

// persistToken is best-effort: if the socket's directory does not exist (so the
// temp-file create fails), it logs and returns without panicking, and the daemon
// keeps serving — only file-based reconnect is unavailable. Mirrors the reference.
func TestPersistTokenCreateFailureIsNonFatal(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "no-such-subdir", "rpc.sock")
	persistToken(socket, "tok") // dir missing → CreateTemp errors; must not panic
	if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatalf("persistToken must not create the missing directory (err=%v)", err)
	}
}

// persistToken's rename-failure arm is best-effort: if the final path can't be
// replaced (here daemon.token pre-exists as a directory, so renaming a file onto
// it fails on every platform), it logs, cleans up the temp file, and returns —
// no panic, the existing entry is untouched, and no daemon.token-* temp leaks.
func TestPersistTokenRenameFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	if err := os.Mkdir(filepath.Join(dir, "daemon.token"), 0o700); err != nil {
		t.Fatal(err)
	}
	persistToken(socket, "tok") // temp create/write/close ok; rename onto a dir fails

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

// removePersistedToken's remove-failure arm is best-effort: when daemon.token
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
	removePersistedToken(socket) // stat ok; remove of a non-empty dir fails
	if _, err := os.Stat(tokDir); err != nil {
		t.Fatalf("non-empty daemon.token dir should survive a failed remove: %v", err)
	}
}

// persistToken's write-failure arm: WriteString errors, so the temp is closed +
// removed, the failure is logged, and no daemon.token appears. Best-effort — the
// caller (runServe) keeps serving.
func TestPersistTokenWriteFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	injectTokenTemp(t, dir, errInjected, nil)
	persistToken(filepath.Join(dir, "rpc.sock"), "tok")
	assertNoPersistedArtifacts(t, dir)
}

// persistToken's close-failure arm: WriteString succeeds but Close errors, so the
// temp is removed, the failure is logged, and no daemon.token appears.
func TestPersistTokenCloseFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	injectTokenTemp(t, dir, nil, errInjected)
	persistToken(filepath.Join(dir, "rpc.sock"), "tok")
	assertNoPersistedArtifacts(t, dir)
}

// removePersistedToken's stat-error arm: when the socket dir is actually a file,
// os.Stat(<file>/daemon.token) fails with a non-not-exist error (ENOTDIR on
// POSIX), which is logged rather than swallowed. Where the OS maps that to
// not-exist the arm is simply skipped; either way it must not panic.
func TestRemovePersistedTokenStatErrorIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePersistedToken(filepath.Join(notDir, "rpc.sock")) // stat under a non-dir; must not panic
}

// persistToken overwrites any stale daemon.token from a prior daemon in the same
// dir — the atomic rename replaces it, so a reconnecting client reads the live
// token, never a dead one.
func TestPersistTokenOverwritesStale(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "rpc.sock")
	path := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(path, []byte("STALE"), 0o600); err != nil {
		t.Fatal(err)
	}
	persistToken(socket, "FRESH")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "FRESH" {
		t.Fatalf("token = %q, want FRESH (stale not overwritten)", b)
	}
}

// openDaemonLog is the platform-neutral half of sweep gap N1: daemonizeWithToken
// lives in server.go and is shared, so these semantics apply on Windows too,
// where the e2e in server_daemonize_unix_test.go cannot run.
func TestOpenDaemonLog(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	logPath := filepath.Join(dir, daemonLogName)

	f := openDaemonLog(sock)
	if f == nil {
		t.Fatal("openDaemonLog returned nil for a writable socket directory")
	}
	if _, err := f.WriteString("first run\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log not created beside the socket: %v", err)
	}
	if runtime.GOOS != "windows" {
		// Windows does not model 0600 as an owner-only ACL — the same caveat
		// already documented for daemon.token.
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	}

	// TRUNCATE, not append: the reference starts a fresh log every -serve.
	f2 := openDaemonLog(sock)
	if f2 == nil {
		t.Fatal("second openDaemonLog returned nil")
	}
	if _, err := f2.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	_ = f2.Close()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second\n" {
		t.Errorf("log = %q, want %q — a restart must truncate, not append", b, "second\n")
	}
}

// A socket path whose directory does not exist, or an empty one, must yield nil
// so daemonize falls back to inherited stdio instead of refusing to start.
func TestOpenDaemonLogUnusablePath(t *testing.T) {
	if f := openDaemonLog(""); f != nil {
		_ = f.Close()
		t.Error("empty socket path should not open a log")
	}
	missing := filepath.Join(t.TempDir(), "no-such-dir", "s.sock")
	if f := openDaemonLog(missing); f != nil {
		_ = f.Close()
		t.Error("missing socket directory should not open a log")
	}
}
