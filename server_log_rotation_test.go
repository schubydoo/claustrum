package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenDaemonLogRotatesToOld pins the .old rotation adopted from 4534d86: the
// prior remote-server.log is renamed to remote-server.log.old (content preserved)
// and a fresh own log is created, on every restart. Before this claustrum unlinked
// the prior log, leaving no .old. This lives in a portable (non-unix) file on
// purpose: openDaemonLog's os.Rename-to-.old is untagged and runs on Windows too,
// so the rotation needs coverage on every OS the daemon builds for.
func TestOpenDaemonLogRotatesToOld(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	logPath := filepath.Join(dir, daemonLogName)
	if err := os.WriteFile(logPath, []byte("PRIOR-SESSION"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := openDaemonLog(sock)
	if f == nil {
		t.Fatal("openDaemonLog returned nil for a writable dir")
	}
	_ = f.Close()
	if b, _ := os.ReadFile(logPath); len(b) != 0 {
		t.Errorf("new remote-server.log is not fresh: %q", b)
	}
	old, err := os.ReadFile(logPath + ".old")
	if err != nil || string(old) != "PRIOR-SESSION" {
		t.Errorf("remote-server.log.old = %q, %v; want the prior log preserved", old, err)
	}
}
