package main

import (
	"os"
	"path/filepath"
)

// persistedTokenName is the fixed basename the daemon writes its auth token to,
// in the socket's directory. A client reconnecting to an already-running daemon
// (e.g. Claude Desktop reopened after a quit) can read it to recover the token
// and re-authenticate — the original -token-file was unlinked and a -token-fd
// pipe is long closed, so without this the token would be unrecoverable.
//
// Matches the reference daemon, which added this in upstream build 5db5e4a
// (see docs/REFERENCE-BUILDS.md): the file is mode 0600, written atomically at
// startup, and unlinked on graceful shutdown. It lives beside the socket (not
// the -token-file), so it works regardless of how the token was supplied.
//
// The fixed name + socket-dir location ARE the reconnect contract — a client
// derives this path from the socket it already knows — so they are deliberately
// not configurable: changing them would break drop-in compatibility. Two daemons
// sharing one directory would collide on this file, but the deployment model is
// one session dir per daemon (the client provisions the socket dir), so sockets
// never share a parent in practice; this matches the reference exactly. On
// Windows the 0600 bits do not create an owner-only DACL (a Go os.CreateTemp
// limitation the reference shares) — there the session dir under the client's
// per-user app-data provides the confinement instead.
const persistedTokenName = "daemon.token"

// persistTokenDir returns the directory the persisted token lives in — the
// socket's directory, which is the per-session dir the client already knows.
func persistTokenDir(socket string) string { return filepath.Dir(socket) }

// persistToken atomically writes the auth token to <dir(socket)>/daemon.token
// (0600) via a temp file + rename, mirroring the reference: os.CreateTemp with
// the "daemon.token-*" pattern yields an O_EXCL 0600 temp, and the rename makes
// the swap atomic so a reconnecting reader never sees a partial token.
//
// Best-effort by design: a failure is logged and non-fatal — the daemon still
// serves every request; only file-based reconnect is unavailable. The reference
// behaves the same (it logs and carries on).
func persistToken(socket, token string) {
	dir := persistTokenDir(socket)
	f, err := os.CreateTemp(dir, "daemon.token-*")
	if err != nil {
		logErrorf("[daemon] failed to persist token: %v", err)
		return
	}
	tmp := f.Name()
	if _, err := f.WriteString(token); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		logErrorf("[daemon] failed to persist token: %v", err)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		logErrorf("[daemon] failed to persist token: %v", err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, persistedTokenName)); err != nil {
		_ = os.Remove(tmp)
		logErrorf("[daemon] failed to persist token: %v", err)
	}
}

// removePersistedToken unlinks the persisted token file on graceful shutdown,
// mirroring the reference's stat-then-unlink sequence. Absent file → silent
// (persist may have failed, or shutdown ran twice); any other stat error, or a
// failed unlink, is logged. Best-effort: the process is exiting regardless.
func removePersistedToken(socket string) {
	path := filepath.Join(persistTokenDir(socket), persistedTokenName)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			logErrorf("[daemon] failed to stat persisted token: %v", err)
		}
		return
	}
	if err := os.Remove(path); err != nil {
		logErrorf("[daemon] failed to remove persisted token: %v", err)
	}
}
