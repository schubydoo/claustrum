//go:build unix

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unix-only. The fixture denies the unlink with a POSIX permission bit, and
// os.Chmod on Windows only toggles the read-only attribute — it does not
// restrict a directory, so the unlink SUCCEEDS there and the "failed to remove"
// log never happens. The os.Geteuid() == 0 skip does not help: Geteuid returns
// -1 on Windows, so it never fires.
//
// Same reason TestEnsureCLIReportsUnclearablePath and TestEnsureCLIStagingFailure
// live in install_unix_test.go. The behaviour under test is portable; the way
// this test PROVOKES it is not.
// The token file is supposed to be consumed. When the unlink fails the daemon
// still starts — the token was read successfully — but it says so, because a
// token file left on disk is a credential nobody knows is still there.
func TestChildTokenLogsUnlinkFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable")
	}
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	tf := filepath.Join(locked, "tok")
	if err := os.WriteFile(tf, []byte("still-valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { // readable, not writable: unlink fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	got, err := childToken(tf)
	if err != nil || got != "still-valid" {
		t.Fatalf("childToken = %q, %v; want the token to be usable despite the unlink failure", got, err)
	}
	if !strings.Contains(buf.String(), "failed to remove --token-file") {
		t.Errorf("unlink failure was not logged; got %q", buf.String())
	}
}
