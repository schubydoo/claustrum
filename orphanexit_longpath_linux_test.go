package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With /proc the daemon dials its long path. A plain file with the bound mtime
// refuses the dial, so the daemon shuts down after 11m0s, as `be1c1ca5` does
// (measured on Linux).
func TestOrphanCheckLongPathDialsThroughProc(t *testing.T) {
	s, sock := bootLongOrphanServer(t)
	replaceWithPlainFile(t, sock, s.sockInfo.ModTime())
	buf := captureLogBuf(t)
	if m := runOrphanChecks(s, sock, 0, 10, 11); m != 11 {
		t.Fatalf("shutdown at minute %d, want 11:\n%s", m, buf.String())
	}
	got := buf.String()
	for _, want := range []string{
		"[Server] socket path " + sock + " no longer leads to this daemon",
		"[Server] socket path " + sock + " did not lead back to this daemon (1/2); re-checking before acting",
		"[Server] orphaned for 11m0s (socket path gone or re-bound, 2 self-probes failed, no connections)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, tooLongNotOrphaned) {
		t.Errorf("wrote the too-long line with /proc:\n%s", got)
	}
}

// With /proc a long path that still leads to the daemon resets the check.
func TestOrphanCheckLongPathStillReachesUs(t *testing.T) {
	s, sock := bootLongOrphanServer(t)
	other := filepath.Join(filepath.Dir(sock), "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	s.sockInfo = fi
	buf := captureLogBuf(t)
	if m := runOrphanChecks(s, sock, 0, 10, 11); m >= 0 {
		t.Fatalf("shut down at minute %d:\n%s", m, buf.String())
	}
	if !strings.Contains(buf.String(), "still reaches this daemon") {
		t.Errorf("no still-reaches line:\n%s", buf.String())
	}
}
