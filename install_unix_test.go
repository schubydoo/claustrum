//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The cli-dir chain is created owner-only (0700), matching the reference, while
// the installed CLI itself stays 0755. Probe-measured under umask 022 against
// 5db5e4a: every directory the reference creates comes out drwx------, where
// claustrum's came out drwxr-xr-x and left the CLI directory world-traversable.
//
// Unix-only because the assertion is about POSIX permission bits and because it
// pins the umask — on Windows neither is meaningful.
//
// The umask is set deliberately: with a restrictive umask (0077) MkdirAll(0755)
// also yields 0700, so the test would pass against the *unfixed* code and prove
// nothing. Forcing 022 is what makes it a real regression test.
//
// The path is nested so intermediate components are checked too, not just the
// leaf — MkdirAll applies the mode to every component it creates.
func TestEnsureCLICreatesOwnerOnlyDirs(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := t.TempDir()
	zst := zstdOf(t, fakeCLI(t, 0))
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zst, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zst)
	root := filepath.Join(dir, "clidir")
	cliPath := filepath.Join(root, "nested", "1.0.0")
	o := installOpts{cliZst: zstFile, cliChecksum: hex.EncodeToString(sum[:])}

	if err := ensureCLI(o, cliPath); err != nil {
		t.Fatalf("ensureCLI: %v", err)
	}

	for _, d := range []string{root, filepath.Dir(cliPath)} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("dir %s mode = %04o, want 0700 (the reference is owner-only)", d, got)
		}
	}

	fi, err := os.Stat(cliPath)
	if err != nil {
		t.Fatalf("stat %s: %v", cliPath, err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("installed CLI mode = %04o, want 0755 (unchanged by this fix)", got)
	}
}
