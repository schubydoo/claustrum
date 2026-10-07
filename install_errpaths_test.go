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
	// TestEnsureCLIFromZst. Not cosmetic: the run shells out via os/exec, whose
	// Windows lookup treats a name with no dot as needing a PATHEXT suffix, so it
	// probes "v1.EXE"/"v1.COM" and never the file itself. An extension-less leaf
	// therefore fails the run on Windows for a reason that has nothing to do
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

// A -cli-version that resolves outside -cli-dir must be refused before the
// install writes or removes anything at a path built from it. The sweeps of
// the cli-dir are not behind this rule. cliPath is
// filepath.Join(cliDir, cliVersion) and Join cleans, so "../victim" lands
// beside cliDir — where ensureCLI's os.RemoveAll would delete it recursively
// and install the CLI in its place.
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
// replaced by the CLI binary.
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

// A version whose name the sweep claims INSTALLS, as on the reference. Measured
// on a Linux VM against 4534d86 through f6010b97: the install succeeds and the
// same run keeps the fresh CLI, a cache hit keeps it at any age, and a later
// install sweeps it once its mtime is more than 10 minutes old. claustrum refused
// these versions before (the retired D7).
func TestEnsureCLIInstallsSweptVersionNames(t *testing.T) {
	for _, v := range []string{".fetch-x", "1.0.zst"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			cliDir := filepath.Join(root, "clidir")
			blob := func() string {
				t.Helper()
				p := filepath.Join(root, "cli.zst")
				if err := os.WriteFile(p, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			}
			// On Windows the file is <v>.exe, and "1.0.zst.exe" is no swept name.
			// No row measures that case. The test follows isSweptName.
			cliPath := installCLIPath(cliDir, v)
			swept := isSweptName(filepath.Base(cliPath))

			f := captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: v, cliZst: blob()})
			if f.CliError != "" {
				t.Fatalf("CliError = %q, want the install to succeed", f.CliError)
			}
			if !isRegularFile(cliPath) {
				t.Fatal("the fresh CLI was swept in the run that installed it")
			}

			old := time.Now().Add(-601 * time.Second)
			if err := os.Chtimes(cliPath, old, old); err != nil {
				t.Fatal(err)
			}
			f = captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: v})
			if !f.CliWasPresent || !isRegularFile(cliPath) {
				t.Fatalf("cache hit: present=%v, file kept=%v; a cache hit must not sweep",
					f.CliWasPresent, isRegularFile(cliPath))
			}

			f = captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: "2.0.0", cliZst: blob()})
			if f.CliError != "" {
				t.Fatalf("second install CliError = %q, want success", f.CliError)
			}
			if _, err := os.Lstat(cliPath); os.IsNotExist(err) != swept {
				t.Errorf("%s, 601 s old: gone=%v after a later install's sweep, want %v (lstat err %v)",
					filepath.Base(cliPath), os.IsNotExist(err), swept, err)
			}
		})
	}
}

// The sweep's name rule, and the validator's acceptance of every name it claims.
func TestSweptNameRule(t *testing.T) {
	for _, name := range []string{".fetch-x", ".fetch-", "1.0.zst", ".zst"} {
		if !isSweptName(name) {
			t.Errorf("isSweptName(%q) = false, want true", name)
		}
		if err := validateCLIVersion(name); err != nil {
			t.Errorf("validateCLIVersion(%q) = %v, want nil — the reference installs it", name, err)
		}
	}
	for _, name := range []string{"1.0.86", "latest", "README", ".FETCH-u", "a.ZST", "a.zst.bak", "zst"} {
		if isSweptName(name) {
			t.Errorf("isSweptName(%q) = true, want false", name)
		}
	}
}

// The download blob prefix is the OTHER housekeeping name rule, and it needs the
// same sharing. Its failure is the mirror of the sweep's: a version with this
// prefix is not deleted, it is exempted from pruneCLI's census forever — never
// counted against -cli-keep, never evicted, and not swept either, since neither
// pass claims the prefix by construction.
func TestDownloadBlobNameRuleIsShared(t *testing.T) {
	for _, name := range []string{".blob-x", ".blob-", ".blob-123456"} {
		if !isDownloadBlobName(name) {
			t.Errorf("isDownloadBlobName(%q) = false, want true", name)
		}
		if err := validateCLIVersion(name); err == nil {
			t.Errorf("validateCLIVersion(%q) = nil, but pruneCLI would never count it", name)
		}
		// It must ALSO stay outside the sweep — that is the invariant the blob
		// itself relies on, and widening isSweptName to cover it would silently
		// re-break the retry.
		if isSweptName(name) {
			t.Errorf("isSweptName(%q) = true; the sweep must not claim the download blob", name)
		}
	}
	for _, name := range []string{"1.0.86", "latest", "blob-x", ".blobby"} {
		if isDownloadBlobName(name) {
			t.Errorf("isDownloadBlobName(%q) = true, want false", name)
		}
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
	if _, err := fetchBytes(t, refusedURL(t)); err == nil {
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
	if _, err := fetchBytes(t, srv.URL); err == nil {
		t.Error("httpGet with truncated body succeeded, want error")
	}
}

// pruneCLI is best-effort: an unlistable cliDir is a silent no-op, and a
// subdirectory with content stays, also past the keep value.
func TestPruneCLIEdges(t *testing.T) {
	pruneCLI(filepath.Join(t.TempDir(), "absent"), 1) // must not panic

	dir := t.TempDir()
	sub := filepath.Join(dir, "not-a-cli")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pruning is newest-mtime-first; pin distinct mtimes so the order can't
	// collapse on a coarse-resolution filesystem.
	now := time.Now()
	if err := os.Chtimes(sub, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
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
	if !isRegularFile(filepath.Join(sub, "inner")) {
		t.Error("pruneCLI removed a subdirectory with content")
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.1")); err != nil {
		t.Errorf("pruneCLI removed the newest version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("pruneCLI kept the older version (err=%v), want pruned", err)
	}
}

// The rename fails while the staging file is still present. Distinct from
// TestEnsureCLIKeepsDestinationWhenStagingVanishes, where the staging file is
// the thing that went missing — here it survives and the destination path is
// what rename(2) rejects, so the staging file must be cleaned up.
//
// The fixture is a final component longer than NAME_MAX. It is a real
// filesystem error rather than an injected one, and it reaches this branch only
// because the clear above is now narrowed to directories: os.Lstat on the
// over-long path fails, so no RemoveAll is attempted, and rename is the first
// call to report the problem. (Clearing unconditionally made RemoveAll fail
// first, which returned the "clearing stale dir at" error instead.)
//
// validateCLIVersion accepts it: a long name is still one path component.
func TestEnsureCLIRenameFailureCleansUpStaging(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	tooLong := strings.Repeat("v", 300) // > NAME_MAX on every supported filesystem

	err := ensureCLI(installOpts{cliDir: cliDir, cliVersion: tooLong, cliZst: zstPath},
		filepath.Join(cliDir, tooLong))
	if err == nil {
		t.Fatal("ensureCLI onto an unusable path succeeded, want a rename error")
	}
	if strings.Contains(err.Error(), "clearing stale dir at ") {
		t.Errorf("error = %q, want the rename error — nothing needed clearing", err)
	}
	if strings.Contains(err.Error(), "staging file vanished") {
		t.Errorf("error = %q, but the staging file was present", err)
	}
	// The staging file must not be left behind as litter.
	assertNoStagingLeftover(t, filepath.Join(cliDir, tooLong))
}

// sha256File surfaces the open error for a missing blob instead of hashing nothing.
func TestSha256FileMissing(t *testing.T) {
	if _, err := sha256File(filepath.Join(t.TempDir(), "absent.zst")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// zstdDecompress cannot create its output when the destination directory does not
// exist; the os.Create error surfaces instead of a silent no-op.
func TestZstdDecompressCreateError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "missing-dir", "cli")
	if err := zstdDecompressBytes(t, zstdOf(t, []byte("payload")), dest); err == nil {
		t.Fatal("decompress into a nonexistent directory succeeded, want an error")
	}
}

// zstdDecompress takes a PATH, so a source that is not there fails at os.Open —
// before the zstd reader and before the destination is created. ensureCLI retries
// stageAndInstall once and re-reads that path, so a source that went away between
// the attempts has to surface as an error.
func TestZstdDecompressOpenError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "cli")
	if err := zstdDecompress(filepath.Join(dir, "absent.zst"), dest); err == nil {
		t.Fatal("decompress of a missing source succeeded, want the os.Open error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination exists after the open failure (stat err = %v), want it untouched", err)
	}
}

// When the CLI dir is unusable AND the OS temp dir is too, fetchToFile has nowhere
// to land the blob and reports the second CreateTemp error. os.TempDir reads TMPDIR
// on Unix and TMP/TEMP on Windows (GetTempPath does not verify the path exists —
// measured on the Windows VM), so pointing all three at a missing dir fails the
// fallback on every leg.
func TestFetchToFileFailsWhenNoTempDirUsable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	tmp := filepath.Join(missing, "tmp")
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	if _, _, err := fetchToFile(srv.URL, missing); err == nil {
		t.Fatal("fetchToFile with no usable temp dir succeeded, want an error")
	}
}

// The staging file can vanish mid-install: it lives in the ".fetch-*" namespace
// that every concurrent install's sweep claims, for the decompress and chmod
// window (see stageAndInstall).
//
// A CLI that is installed at cliPath must survive that. Only a folder is cleared
// before the rename. A file is replaced by the rename itself, so a rename whose
// source is gone leaves it alone.
//
// The chmod seam removes the staging file, as a concurrent sweep does, with no
// sleeps. It removes it on the retry too, so the install fails.
func TestEnsureCLIKeepsDestinationWhenStagingVanishes(t *testing.T) {
	old := chmodStaged
	chmodStaged = func(p string, _ os.FileMode) error { return os.Remove(p) }
	t.Cleanup(func() { chmodStaged = old })

	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(cliDir, "1.0.0")
	const installed = "the previously installed CLI"
	if err := os.WriteFile(cliPath, []byte(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ensureCLI(installOpts{cliDir: cliDir, cliVersion: "1.0.0", cliZst: zstPath}, cliPath)
	if err == nil || !strings.Contains(err.Error(), "staging file vanished") {
		t.Fatalf("ensureCLI = %v, want the staging-vanished error", err)
	}
	if b, readErr := os.ReadFile(cliPath); readErr != nil || string(b) != installed {
		t.Errorf("the destination was destroyed for an install that could not finish: err %v, content %q", readErr, b)
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
// Deterministic, no sleeps: the chmod seam removes the staging file on its FIRST
// call only. So the rename finds its source gone once, and the retry's copy
// survives.
func TestEnsureCLIRetriesWhenStagingIsSweptOnce(t *testing.T) {
	old := chmodStaged
	var calls int
	chmodStaged = func(p string, m os.FileMode) error {
		calls++
		if calls == 1 {
			return os.Remove(p)
		}
		return old(p, m)
	}
	t.Cleanup(func() { chmodStaged = old })

	root := t.TempDir()
	cliDir := filepath.Join(root, "clidir")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(root, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(cliDir, "1.0.0")
	if err := ensureCLI(installOpts{cliDir: cliDir, cliVersion: "1.0.0", cliZst: zstPath}, cliPath); err != nil {
		t.Fatalf("ensureCLI = %v, want the retry to recover from a swept staging file", err)
	}
	if calls != 2 {
		t.Fatalf("the staging step ran %d times, want 2 (one retry)", calls)
	}
	if !isRegularFile(cliPath) || !isRunnable(cliPath) {
		t.Error("the CLI was not installed after the retry")
	}
}
