package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errStatRefused is the sentinel the os.Stat seam fails with, so the logged line
// is asserted against text this file owns.
var errStatRefused = errors.New("seam: stat refused")

// persistToken's identity read happens AFTER the rename, so a failure there means
// the token file is on disk but this daemon cannot prove the inode is its own.
// The contract for that case is "own nothing": return nil — which makes teardown
// skip the unlink entirely — log the failure, and leave the written file alone.
func TestPersistTokenStatFailureOwnsNothing(t *testing.T) {
	// The stub hands back the file's REAL identity alongside the error on purpose:
	// persistToken must own nothing because the call failed, not because the
	// payload happened to be nil. With a nil payload, dropping the error arm would
	// still yield a nil FileInfo and the ownership assertions would prove nothing.
	old := statPersistedToken
	statPersistedToken = func(path string) (os.FileInfo, error) {
		fi, _ := os.Stat(path)
		return fi, errStatRefused
	}
	t.Cleanup(func() { statPersistedToken = old })

	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	dest := filepath.Join(dir, persistedTokenName)

	var fi os.FileInfo
	out := captureLog(t, func() { fi = persistToken(sock, "stat-seam-token") })

	if fi != nil {
		t.Errorf("persistToken = %v, want nil — a daemon that cannot identify the inode must own nothing", fi)
	}
	want := "failed to stat persisted token: " + errStatRefused.Error()
	if !strings.Contains(out, want) {
		t.Errorf("log = %q, want it to contain %q", out, want)
	}

	// The rename already succeeded, so the file must still be there, whole: the
	// arm declines ownership, it does not roll the write back. A reconnecting
	// client can still read it; only this daemon's teardown unlink is given up.
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("%s missing after a failed stat: %v", persistedTokenName, err)
	}
	if string(b) != "stat-seam-token" {
		t.Errorf("%s = %q, want the token that was written", persistedTokenName, b)
	}

	// And with nil ownership, teardown must leave that file for whoever does own
	// it — the exact consequence of returning nil rather than a stale FileInfo.
	removePersistedToken(sock, fi)
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("removePersistedToken deleted a file this daemon does not own: %v", err)
	}
}
