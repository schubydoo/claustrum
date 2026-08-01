//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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

// The two tests below moved here from install_errpaths_test.go after the Windows
// CI leg failed on both. Each depends on a POSIX permission bit denying an
// operation — an unremovable entry, an unwritable directory — and os.Chmod on
// Windows only toggles the read-only attribute, which does not restrict
// directories. So ensureCLI SUCCEEDED there and both "want an error" assertions
// failed. They are unix-only in the same sense TestEnsureCLICreatesOwnerOnlyDirs
// above is: the mechanism under test is a POSIX permission, not the install
// logic.

// When the blocker cannot be removed, the failure is reported with the
// reference's "clearing stale dir at " prefix rather than a bare rename error.
// Measured at 5db5e4a: an undeletable entry under cliPath yields
// `clearing stale dir at <path>: unlinkat <path>/locked/x: permission denied`.
func TestEnsureCLIReportsUnclearablePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable")
	}
	dir := t.TempDir()
	cliPath := filepath.Join(dir, "v1")
	locked := filepath.Join(cliPath, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { // cannot unlink x inside
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureCLI(installOpts{cliZst: zstPath}, cliPath)
	if err == nil {
		t.Fatal("ensureCLI with an unclearable cliPath succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "clearing stale dir at ") {
		t.Errorf("error = %q, want the reference's \"clearing stale dir at \" prefix", err)
	}
}

// The staging file cannot be created when the cli-dir exists but is not
// writable. MkdirAll succeeds (the directory is already there), so this is the
// only path that reaches the CreateTemp failure branch.
func TestEnsureCLIStagingFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable")
	}
	dir := t.TempDir()
	cliDir := filepath.Join(dir, "clidir")
	if err := os.Mkdir(cliDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cliDir, 0o700) })

	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureCLI(installOpts{cliZst: zstPath}, filepath.Join(cliDir, "v1"))
	if err == nil {
		t.Fatal("ensureCLI into an unwritable cli-dir succeeded, want a staging error")
	}
	if !strings.Contains(err.Error(), "staging cli: ") {
		t.Errorf("error = %q, want a \"staging cli: \" prefix", err)
	}
}

// The staging file can vanish mid-install: it lives in the ".fetch-*" namespace
// that every concurrent install's sweep claims, and claustrum holds it open for
// the whole decompress + chmod + isRunnable window (the reference does not — it
// extracts in place, so it has no in-flight file there to lose).
//
// The destination must survive that. Ordering RemoveAll before Rename destroyed
// it: the CLI already installed at cliPath was deleted and nothing replaced it,
// leaving an EMPTY cli-dir and a working install gone.
//
// The fixture makes the race deterministic instead of timing-dependent: the
// stand-in CLI deletes ITSELF when isRunnable execs it. The exec survives the
// unlink, so isRunnable still returns true and the code proceeds to the rename
// with its source already gone — exactly the interleaving a concurrent sweep
// produces, with no sleeps.
//
// Unix-only: it relies on a shell stand-in that can unlink "$0", and on unlink
// during exec, neither of which Windows offers.
func TestEnsureCLIKeepsDestinationWhenStagingVanishes(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(cliDir, "1.0.0")
	if err := os.WriteFile(cliPath, []byte("the previously installed CLI"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stand-in that removes its own path during the isRunnable probe.
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, []byte("#!/bin/sh\nrm -f -- \"$0\"\nexit 0\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ensureCLI(installOpts{cliDir: cliDir, cliVersion: "1.0.0", cliZst: zstPath}, cliPath)
	if err == nil {
		t.Fatal("ensureCLI succeeded although its staging file was removed, want an error")
	}
	if _, statErr := os.Stat(cliPath); statErr != nil {
		t.Errorf("the destination was destroyed for an install that could not finish: %v", statErr)
	}
}
