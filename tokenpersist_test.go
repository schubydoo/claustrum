package main

import (
	"os"
	"path/filepath"
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
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
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
