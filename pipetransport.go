package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// pipeNameFileName is the fixed basename the daemon writes the chosen named-pipe
// name to, in the socket's directory — the pipe analogue of daemon.token. A
// client that already knows the socket path reads <dir>/rpc.pipe to discover the
// opaque pipe name to dial; claustrum, not the client, chooses that name.
//
// Like rpc.sock and daemon.token, this file is written atomically before the
// pipe begins accepting (and before the daemon prints its ready line) and removed
// on graceful shutdown; it is left behind only on an unclean kill/crash. The
// fixed name + socket-dir location ARE the discovery contract, so they are
// deliberately not configurable.
const pipeNameFileName = "rpc.pipe"

// pipeNameFilePath is where rpc.pipe lives: beside the socket, the per-session
// directory the client already knows.
func pipeNameFilePath(socket string) string {
	return filepath.Join(filepath.Dir(socket), pipeNameFileName)
}

// createPipeTemp opens the temp file writePipeNameFile renames into place. A
// package var (reusing the tokenTempFile seam from tokenpersist.go) so tests can
// inject a writer that fails at a chosen step; production always uses
// os.CreateTemp with the "rpc.pipe-*" pattern.
var createPipeTemp = func(dir string) (tokenTempFile, error) {
	return os.CreateTemp(dir, "rpc.pipe-*")
}

// writePipeNameFile atomically publishes the chosen pipe name to rpc.pipe via a
// temp file + rename, so a reader never observes a partial name. Mirrors the
// daemon.token write. The name is not a secret (the owner-only pipe DACL is the
// access control), but the file is created 0600 for tidiness/parity.
func writePipeNameFile(socket, name string) error {
	return writeFileViaTemp(createPipeTemp, filepath.Dir(socket), pipeNameFilePath(socket), name)
}

// removePipeNameFile unlinks rpc.pipe on graceful shutdown, mirroring
// removePersistedToken: an absent file is silent (the pipe was never started, or
// shutdown ran twice); any other stat error or a failed unlink is logged.
// Best-effort — the process is exiting regardless. Safe to call unconditionally.
func removePipeNameFile(socket string) {
	path := pipeNameFilePath(socket)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			logErrorf("[Server] failed to stat pipe-name file: %v", err)
		}
		return
	}
	if err := os.Remove(path); err != nil {
		logErrorf("[Server] failed to remove pipe-name file: %v", err)
	}
}

// removePipeNameFileIfOwned unlinks rpc.pipe on graceful shutdown ONLY when the file
// on disk is still the inode this daemon published (os.SameFile against owned) — the
// same handoff protection removePersistedToken/removeSocketIfOwned apply to the token
// and socket. rpc.pipe is claustrum's own CT-5 artifact, but a restart's successor can
// republish it before this predecessor tears down, so it earns the same guard so the
// departing daemon cannot delete the successor's pipe pointer. A nil owned (no pipe
// served this boot) is a no-op; the unconditional removePipeNameFile above is used for
// the startup stale-clear, where there is no successor to protect.
func removePipeNameFileIfOwned(socket string, owned os.FileInfo) {
	if owned == nil {
		return
	}
	path := pipeNameFilePath(socket)
	cur, err := os.Stat(path)
	if err != nil {
		return
	}
	if os.SameFile(cur, owned) {
		if err := os.Remove(path); err != nil {
			logErrorf("[Server] failed to remove pipe-name file: %v", err)
		}
	}
}

// ownerOnlySDDL builds the security descriptor for the named pipe: a protected
// DACL (P — no inheritance) granting GENERIC_ALL (GA) to exactly one principal,
// the daemon user's SID, and to no one else. There is no ACE for Everyone
// (S-1-1-0), Authenticated Users (S-1-5-11), or anonymous (S-1-5-7), and the
// DACL is present (not NULL, which would grant everyone), so this is the pipe
// analogue of the AF_UNIX socket's 0600 owner-only mode.
func ownerOnlySDDL(sid string) string {
	return "D:P(A;;GA;;;" + sid + ")"
}

// pipePath returns the full Windows named-pipe path for an instance id. Kept
// platform-neutral (pure string building) so it is unit-testable off Windows.
func pipePath(instanceID string) string {
	return `\\.\pipe\claustrum-` + instanceID
}

// newInstanceID returns a random hex token that makes the pipe name unique per
// daemon boot, so a stale pipe from a crashed predecessor can never be reused or
// collided with. The client learns the resulting name from rpc.pipe, so it need
// not be predictable.
func newInstanceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate pipe instance id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
