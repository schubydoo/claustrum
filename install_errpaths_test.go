package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// refusedURL returns an http URL on a port that was just listening and no
// longer is, so a connection attempt fails fast and deterministically.
func refusedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr + "/cli.zst"
}

// runInstall reports an ensureCLI failure through the CliError fact (never a
// crash or a bare error print) so the caller's JSON parse always succeeds.
func TestRunInstallReportsEnsureError(t *testing.T) {
	f := captureInstallFacts(t, installOpts{
		cliDir:     t.TempDir(),
		cliVersion: "v9.9.9",
		// no -cli-url and no -cli-zst: the CLI is missing with no way to get it
	})
	if f.CliWasPresent {
		t.Error("cliWasPresent = true for a missing CLI")
	}
	if !strings.Contains(f.CliError, "missing and no --cli-url or --cli-zst") {
		t.Errorf("CliError = %q, want missing-source error", f.CliError)
	}
}

// A -cli-url whose server is unreachable surfaces as "download failed", not a
// bare transport error.
func TestEnsureCLIDownloadRefused(t *testing.T) {
	dir := t.TempDir()
	err := ensureCLI(installOpts{cliURL: refusedURL(t)}, filepath.Join(dir, "v1"))
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Errorf("ensureCLI refused download = %v, want download failed", err)
	}
}

// A cliPath whose parent chain runs through a regular file fails at the
// MkdirAll step, before any decompression work.
func TestEnsureCLIMkdirDenied(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: zstPath}, filepath.Join(blocker, "sub", "v1")); err == nil {
		t.Error("ensureCLI under a file parent succeeded, want mkdir error")
	}
}

// An occupied cliPath is cleared, not fatal. rename(2) refuses to replace a
// non-empty directory, so the reference removes whatever is there first and the
// install succeeds. Measured at 5db5e4a: with a non-empty directory at cliPath
// the reference exits 0 with no cliError and a regular file in place, while
// claustrum reported `rename …: file exists` and left the blocker.
//
// This test previously asserted the opposite — that the install MUST fail —
// which is the old claustrum behaviour, not the reference's.
func TestEnsureCLIClearsOccupiedPath(t *testing.T) {
	dir := t.TempDir()
	// A DOTTED leaf, matching the real cliPath (a version string) and the sibling
	// TestEnsureCLIFromZst. Not cosmetic: isRunnable shells out via os/exec, whose
	// Windows lookup treats a name with no dot as needing a PATHEXT suffix, so it
	// probes "v1.EXE"/"v1.COM" and never the file itself. An extension-less leaf
	// therefore fails isRunnable on Windows for a reason that has nothing to do
	// with what this test is asserting.
	cliPath := filepath.Join(dir, "1.0.0")
	if err := os.MkdirAll(filepath.Join(cliPath, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliPath, "occupied", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: zstPath}, cliPath); err != nil {
		t.Fatalf("ensureCLI onto an occupied path = %v, want success", err)
	}
	// Split, not `a || b`: the combined form is what made the Windows failure
	// ambiguous — "should now hold the installed CLI" is equally consistent with
	// the blocker surviving and with the CLI landing but not being executable.
	if !isRegularFile(cliPath) {
		t.Error("cliPath is not a regular file — the blocker was not replaced")
	}
	if !isRunnable(cliPath) {
		t.Error("cliPath is a regular file but not runnable — the install landed, the exec probe failed")
	}
}

// A -cli-version that resolves outside -cli-dir must be refused BEFORE any
// filesystem effect. cliPath is filepath.Join(cliDir, cliVersion) and Join
// cleans, so "../victim" lands beside cliDir — where ensureCLI's os.RemoveAll
// would delete it recursively and install the CLI in its place.
//
// Measured without the guard: the victim directory and its file were destroyed
// and replaced by the CLI binary. The reference at 5db5e4a does exactly the
// same, so this is a deliberate claustrum-only hardening, not a parity fix —
// invisible on every honest path, where the version is a bare string.
//
// The assertion is on the SURVIVING file, not just the error: an error alone
// would also be reported if the guard ran after the delete.
func TestEnsureCLIRefusesVersionEscapingCliDir(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(victim, "keep.txt")
	if err := os.WriteFile(keep, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	f := captureInstallFacts(t, installOpts{
		cliDir: cliDir, cliVersion: filepath.Join("..", "victim"), cliZst: zstPath,
	})
	if !strings.Contains(f.CliError, "single path component") {
		t.Errorf("CliError = %q, want an escape refusal", f.CliError)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the sibling directory's file was destroyed: %v", err)
	}
	if fi, err := os.Stat(victim); err != nil || !fi.IsDir() {
		t.Errorf("victim is no longer a directory (err=%v) — it was replaced by the CLI", err)
	}
}

// The symlink traversal a LEXICAL containment check does not catch, and the
// reason the guard requires a single path component instead.
//
// With cliDir/link -> /outside, the version "link/1.0.0" is lexically inside
// cliDir, so a filepath.Rel-based check accepts it. os.RemoveAll then follows
// the symlink at open time and deletes /outside/1.0.0 recursively. Measured
// against the first version of this guard: the directory was destroyed and
// replaced by the CLI binary. The reference at 5db5e4a does the same.
//
// Unix-only for the fixture (os.Symlink on Windows needs a privilege the CI
// runner does not have); the guard itself is platform-independent and
// TestIsSingleComponent covers it everywhere.
func TestEnsureCLIRefusesSymlinkTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege")
	}
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	outside := filepath.Join(root, "outside", "1.0.0")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(cliDir, "link")); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	f := captureInstallFacts(t, installOpts{
		cliDir: cliDir, cliVersion: "link/1.0.0", cliZst: zstPath,
	})
	if !strings.Contains(f.CliError, "single path component") {
		t.Errorf("CliError = %q, want a refusal", f.CliError)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("content outside cliDir was destroyed through the symlink: %v", err)
	}
	if fi, err := os.Stat(outside); err != nil || !fi.IsDir() {
		t.Errorf("outside dir is no longer a directory (err=%v) — replaced by the CLI", err)
	}
}

// A version that IS a symlink, as the final component, stays safe and is not
// refused: os.RemoveAll unlinks a symlink rather than following it, so the
// link's target keeps its contents and only the link is replaced.
func TestEnsureCLIFinalComponentSymlinkIsSafe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege")
	}
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cliDir, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	f := captureInstallFacts(t, installOpts{
		cliDir: cliDir, cliVersion: "1.0.0", cliZst: zstPath,
	})
	if f.CliError != "" {
		t.Errorf("CliError = %q, want the install to succeed", f.CliError)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the symlink's target was emptied: %v", err)
	}
	if !isRegularFile(filepath.Join(cliDir, "1.0.0")) {
		t.Error("cliPath should be the installed CLI, replacing the symlink")
	}
}

func TestIsSingleComponent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Every real version format the client can send.
		{"1.0.86", true},
		{"2.0.0-beta.1", true},
		{"5db5e4a12f88487e47c2c48259b69a2d630bb3f7", true},
		{"latest", true},
		{"1.0.86+build.5", true},
		{".fetch-x", true}, // a leading dot is just a name, not a traversal

		{"", false},
		{".", false},  // resolves cliPath to the cli-dir ITSELF
		{"..", false}, // resolves to the cli-dir's parent
		{"../victim", false},
		{"a/b", false},          // any nesting: the intermediate is the symlink risk
		{"link/1.0.0", false},   // the measured symlink traversal
		{`a\b`, false},          // rejected on Unix too, on purpose
		{"sub/../1.0.0", false}, // cleans to a child, still refused — no nesting at all
	}
	for _, tc := range cases {
		if got := isSingleComponent(tc.in); got != tc.want {
			t.Errorf("isSingleComponent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// httpGet propagates a dial failure from client.Get.
func TestHTTPGetConnectionError(t *testing.T) {
	if _, err := httpGet(refusedURL(t)); err == nil {
		t.Error("httpGet to a refused port succeeded, want error")
	}
}

// httpGet propagates a body read that dies mid-stream: the server advertises
// more bytes than it sends, so ReadAll hits an unexpected EOF.
func TestHTTPGetTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(1024))
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	if _, err := httpGet(srv.URL); err == nil {
		t.Error("httpGet with truncated body succeeded, want error")
	}
}

// pruneCLI is best-effort: an unlistable cliDir is a silent no-op, and
// subdirectories inside cliDir are never candidates for pruning.
func TestPruneCLIEdges(t *testing.T) {
	pruneCLI(filepath.Join(t.TempDir(), "absent"), 1) // must not panic

	dir := t.TempDir()
	sub := filepath.Join(dir, "not-a-cli")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pruning is newest-mtime-first; pin distinct mtimes so the order can't
	// collapse on a coarse-resolution filesystem.
	now := time.Now()
	for i, name := range []string{"1.0.0", "1.0.1"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("cli"), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-1) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	pruneCLI(dir, 1)
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("pruneCLI removed the subdirectory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.1")); err != nil {
		t.Errorf("pruneCLI removed the newest version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("pruneCLI kept the older version (err=%v), want pruned", err)
	}
}
