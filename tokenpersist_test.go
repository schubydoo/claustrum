package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
