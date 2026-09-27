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
	"time"
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
// the whole decompress + chmod + isRunnable window (the reference shows no
// in-flight file across the --version half of that window; measured 2026-08-08 it
// does have one mid-download, so the difference is the window, not staging as
// such — see stageAndInstall).
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

// The concurrent-install case Greptile reported: another install's sweep
// reclaims our staging file, and the install must still SUCCEED rather than
// report "staging file vanished".
//
// A name-only sweep guard cannot fix this. In the staggered ordering the other
// install staged BEFORE this one began, so its file is indistinguishable from
// litter by name — which is why the fix is a bounded retry rather than a smarter
// sweep, and why the sweep itself stays unconditional.
//
// Deterministic, no sleeps: the stand-in CLI removes its own path the FIRST time
// isRunnable execs it and leaves a marker, so the retry's copy survives. The exec
// survives the unlink, so isRunnable still returns true and the rename is reached
// with its source already gone — exactly what a concurrent sweep produces.
func TestEnsureCLIRetriesWhenStagingIsSweptOnce(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "swept-once")
	script := "#!/bin/sh\nif [ ! -f " + marker + " ]; then : > " + marker + "; rm -f -- \"$0\"; fi\nexit 0\n"
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, []byte(script)), 0o600); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(cliDir, "1.0.0")
	if err := ensureCLI(installOpts{cliDir: cliDir, cliVersion: "1.0.0", cliZst: zstPath}, cliPath); err != nil {
		t.Fatalf("ensureCLI = %v, want the retry to recover from a swept staging file", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the fixture never removed its own staging file, so nothing was retried: %v", err)
	}
	if !isRegularFile(cliPath) {
		t.Error("the CLI was not installed after the retry")
	}
}

// A hash over a directory fails at read time, not open time; the error surfaces.
func TestSha256FileDirectory(t *testing.T) {
	if _, err := sha256File(t.TempDir()); err == nil {
		t.Fatal("expected an error hashing a directory")
	}
}

// The sweep never follows a symlink out of the cli-dir. It judges a ".fetch-*"
// symlink by the LINK's own mtime (an lstat) and removes only the link. The
// target, a file or a directory with a file in it, stays in every case.
// Measured on a Linux VM against 4534d86 through f6010b97: an old link to a
// fresh target goes, and a fresh link to an old target stays.
//
// Go has no portable lchtimes, so the link's age comes from the "now" handed to
// the sweep: the link is created at real time, and the target's mtime is set.
func TestSweepFetchTempsJudgesTheLinkNotItsTarget(t *testing.T) {
	setup := func(t *testing.T) (cliDir, outFile, outDir string) {
		t.Helper()
		root := t.TempDir()
		cliDir = filepath.Join(root, "cli")
		outFile = filepath.Join(root, "outside-file")
		outDir = filepath.Join(root, "outside-dir")
		if err := os.Mkdir(cliDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outFile, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "inner"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outFile, filepath.Join(cliDir, ".fetch-lnk")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outDir, filepath.Join(cliDir, ".fetch-lnkdir")); err != nil {
			t.Fatal(err)
		}
		return cliDir, outFile, outDir
	}
	setTargets := func(t *testing.T, at time.Time, paths ...string) {
		t.Helper()
		for _, p := range paths {
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	targetsKept := func(t *testing.T, outFile, outDir string) {
		t.Helper()
		if !isRegularFile(outFile) || !isRegularFile(filepath.Join(outDir, "inner")) {
			t.Error("the sweep reached through a symlink and removed its target")
		}
	}
	linkExists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("old link, fresh target", func(t *testing.T) {
		cliDir, outFile, outDir := setup(t)
		now := time.Now().Add(time.Hour) // the links are an hour old at this "now"
		setTargets(t, now, outFile, outDir)
		sweepFetchTemps(cliDir, now)
		for _, l := range []string{".fetch-lnk", ".fetch-lnkdir"} {
			if linkExists(filepath.Join(cliDir, l)) {
				t.Errorf("%s survived; an old link goes whatever its target's age", l)
			}
		}
		targetsKept(t, outFile, outDir)
	})

	t.Run("fresh link, old target", func(t *testing.T) {
		cliDir, outFile, outDir := setup(t)
		setTargets(t, time.Now().Add(-time.Hour), outFile, outDir)
		sweepFetchTemps(cliDir, time.Now())
		for _, l := range []string{".fetch-lnk", ".fetch-lnkdir"} {
			if !linkExists(filepath.Join(cliDir, l)) {
				t.Errorf("%s was swept; a fresh link stays whatever its target's age", l)
			}
		}
		targetsKept(t, outFile, outDir)
	})
}

// The ldd probe's bound kills ldd's WHOLE process group and drops the output ldd
// wrote. The stub prints a musl banner at once, backgrounds a sleeper that holds
// the output pipe, and waits. Measured on a Linux VM from 19f30c46 on: the
// reference answers at the bound, reports glibc (no marker), and leaves no
// sleeper. Three things fail without the fix: a direct-child kill leaves the
// sleeper alive and holding the pipe, no bound blocks for 30 s, and keeping the
// partial output reports musl.
func TestRunLddVersionKillsTheGroupAtTheBound(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "sleeper.pid")
	stub := "#!/bin/sh\necho 'musl libc (x86_64)'\nsleep 30 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "ldd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	noMusl := func(string) ([]string, error) { return nil, nil }

	// 3 s, not less: the stub must write its pid file before the bound fires, and
	// macOS shell start-up is slow on a loaded runner.
	const bound = 3 * time.Second
	start := time.Now()
	got := detectLibcWith(bound, runLddVersion, noMusl)
	elapsed := time.Since(start)

	pid := readGrandchildPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if got != "glibc" {
		t.Errorf("detectLibcWith = %q, want glibc: output written before the bound must be dropped", got)
	}
	// The group kill closes the pipe at once. A surviving sleeper adds
	// lddKillGrace (2 s) on top of the bound.
	if elapsed >= bound+lddKillGrace {
		t.Errorf("the probe took %s, want under %s: a child held the pipe past the bound", elapsed, bound+lddKillGrace)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatalf("sleeper pid %d still alive: the bound did not kill ldd's process group", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The bound covers the ldd process only. An ldd that exits in time keeps its
// output, even when a child holds the pipe and the answer lands past the bound.
// Measured on a Linux VM against f6010b97: a stub that prints a musl banner,
// exits at 3.5 s and leaves a `sleep` child on its stdout answers musl at 5.54 s,
// and the child survives. The test scales that down: a 3 s bound and an exit
// at 2 s, so the answer lands near 4 s, past the bound.
func TestRunLddVersionKeepsOutputOfAnLddThatExitedInTime(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	stub := "#!/bin/sh\necho 'musl libc (x86_64)'\nsleep 2\nsleep 10 &\necho $! > " + pidFile + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ldd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	noMusl := func(string) ([]string, error) { return nil, nil }

	const bound = 3 * time.Second
	start := time.Now()
	got := detectLibcWith(bound, runLddVersion, noMusl)
	elapsed := time.Since(start)

	pid := readGrandchildPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if elapsed <= bound {
		t.Fatalf("the probe answered at %s, inside the %s bound; the fixture must land past it", elapsed, bound)
	}
	if got != "musl" {
		t.Errorf("detectLibcWith = %q at %s, want musl: ldd exited in time, so its output stands", got, elapsed)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Error("the child holding the pipe was killed; after ldd exits, nothing is killed")
	}
}
