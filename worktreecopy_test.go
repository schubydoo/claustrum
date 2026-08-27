package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// treeOf lists a directory's contents relative to root, excluding .git, so a
// copied worktree can be compared as a whole rather than file by file.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // the root itself is not an entry
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func eqTree(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
		}
	}
}

// TestPopulateWorktree pins 7d193f89's worktree seeding. `git worktree add` checks
// out tracked files only, so a declared untracked file is missing unless copied.
// 7d193f89 copies exactly the untracked files that are BOTH named by the manifest
// AND git-ignored — no `.claude/` (5db5e4a copied it; 7d193f89 does not), and no
// manifest match that git does not ignore.
//
// Every expectation was probe-measured against the reference at 7d193f89. In this
// fixture only `secret.env` is both git-ignored and in the manifest; `notes/` and
// `plain-untracked.txt` are in the manifest but not ignored, `logs/` is ignored but
// not in the manifest, and `.claude/` is never copied regardless.
func TestPopulateWorktree(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses symlinks and POSIX modes")
	}

	fixture := func(t *testing.T, withClaude, withManifest bool) (repo, wt string) {
		t.Helper()
		root := t.TempDir()
		repo = filepath.Join(root, "repo")
		runGit(t, root, "init", "-b", "master", "repo")
		writeFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n", 0o644)
		writeFile(t, filepath.Join(repo, ".gitignore"), "secret.env\nlogs/\n", 0o644)
		runGit(t, repo, "add", "tracked.txt", ".gitignore")

		if withClaude {
			writeFile(t, filepath.Join(repo, ".claude", "settings.json"), "{}\n", 0o644)
			writeFile(t, filepath.Join(repo, ".claude", "nested", "deep.txt"), "deep\n", 0o644)
			writeFile(t, filepath.Join(repo, ".claude", ".hidden"), "hid\n", 0o644)
			if err := os.Symlink("settings.json", filepath.Join(repo, ".claude", "link.json")); err != nil {
				t.Fatal(err)
			}
		}
		if withManifest {
			writeFile(t, filepath.Join(repo, ".worktreeinclude"),
				"secret.env\nnotes/\nplain-untracked.txt\nlink.txt\n", 0o644)
		}
		writeFile(t, filepath.Join(repo, "secret.env"), "S\n", 0o644)          // gitignored + in manifest -> copied
		writeFile(t, filepath.Join(repo, "notes", "n1.txt"), "n\n", 0o644)     // in manifest, NOT ignored
		writeFile(t, filepath.Join(repo, "plain-untracked.txt"), "p\n", 0o644) // in manifest, NOT ignored
		writeFile(t, filepath.Join(repo, "other-untracked.txt"), "o\n", 0o644) // in neither
		writeFile(t, filepath.Join(repo, "logs", "l1.txt"), "l\n", 0o644)      // gitignored, NOT in manifest
		if err := os.Symlink("tracked.txt", filepath.Join(repo, "link.txt")); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "commit", "-m", "init")
		return repo, filepath.Join(root, "wt")
	}

	// The seeded tree is the same whether or not `.claude/` exists — it is never
	// copied — and holds only the tracked files plus the one git-ignored manifest
	// match.
	withManifest := []string{".gitignore", "secret.env", "tracked.txt"}

	t.Run("claude_present_is_not_copied", func(t *testing.T) {
		repo, wt := fixture(t, true, true)
		runGit(t, repo, "worktree", "add", "-b", "b1", wt)
		populateWorktree(repo, wt)
		eqTree(t, treeOf(t, wt), withManifest, "claude-present")
	})

	t.Run("no_manifest_copies_nothing", func(t *testing.T) {
		repo, wt := fixture(t, true, false)
		runGit(t, repo, "worktree", "add", "-b", "b2", wt)
		populateWorktree(repo, wt)
		eqTree(t, treeOf(t, wt), []string{".gitignore", "tracked.txt"}, "no-manifest")
	})

	t.Run("no_claude_dir", func(t *testing.T) {
		repo, wt := fixture(t, false, true)
		runGit(t, repo, "worktree", "add", "-b", "b3", wt)
		populateWorktree(repo, wt)
		eqTree(t, treeOf(t, wt), withManifest, "no-claude")
	})
}

// TestCopyFileDoesNotPreserveMode pins a reference behaviour that looks like a
// bug and is reproduced deliberately: the copy is created 0666-subject-to-umask
// and the source mode is discarded, so an executable arrives NON-executable.
//
// Probe-measured by varying the launcher's umask — 022 gives 0644, 077 gives
// 0600, 000 gives 0666 — for sources that were 0755, 0640 and 0400 alike.
func TestCopyFileDoesNotPreserveMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "exec.sh")
	writeFile(t, src, "#!/bin/sh\n", 0o755)
	dst := filepath.Join(dir, "out", "exec.sh")
	copyFile(src, dst)

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 != 0 {
		t.Errorf("copy is executable (%o); the reference discards the source mode", fi.Mode().Perm())
	}
	// The umask decides the rest, so assert the property rather than a constant.
	if fi.Mode().Perm()&^0o666 != 0 {
		t.Errorf("copy mode %o has bits outside 0666", fi.Mode().Perm())
	}
}

// TestCopyFileSkipsNonRegular guards the symlink rule from the other direction:
// copyFile must not follow a link and materialize its target.
func TestCopyFileSkipsNonRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target.txt"), "secret\n", 0o644)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "link.txt")
	copyFile(link, dst)
	if _, err := os.Lstat(dst); err == nil {
		t.Error("copyFile materialized a symlink; the reference copies neither the link nor its target")
	}
}

// The copy helpers are best-effort by design: a failure must leave the worktree
// alone rather than turn a successful git.worktree_create into an error. These
// cover the give-up paths.
func TestWorktreeCopyFailuresAreSilent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX directory permissions to provoke failures")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits these cases rely on")
	}

	t.Run("copyFile_unreadable_source", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "locked.txt")
		writeFile(t, src, "x\n", 0o000)
		dst := filepath.Join(dir, "out", "locked.txt")
		copyFile(src, dst) // must not panic
		if _, err := os.Stat(dst); err == nil {
			t.Error("an unreadable source produced a destination file")
		}
	})

	t.Run("copyFile_undestinable_target", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "a.txt")
		writeFile(t, src, "x\n", 0o644)
		blocked := filepath.Join(dir, "ro")
		if err := os.Mkdir(blocked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
		copyFile(src, filepath.Join(blocked, "sub", "a.txt")) // must not panic
	})

	// A manifest present but git failing (not a repo) must be a no-op.
	t.Run("copyWorktreeIncludes_git_fails", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, worktreeIncludeFile), "x\n", 0o644)
		wt := filepath.Join(dir, "wt")
		if err := os.Mkdir(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		copyWorktreeIncludes(dir, wt) // dir is not a git repo
		if got := treeOf(t, wt); len(got) != 0 {
			t.Errorf("worktree got %v, want nothing copied when git fails", got)
		}
	})
}

// safeOverlayDest creates missing intermediate directories for a manifest copy but
// refuses to traverse a ".." or a symlinked component, so a planted link inside the
// worktree cannot carry a copy outside it.
func TestSafeOverlayDest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	wt := t.TempDir()

	// A nested path with a "." segment: the "." is skipped, intermediate dirs are
	// created, and the leaf dest returned.
	dst := safeOverlayDest(wt, "a/./b/file.txt")
	if want := filepath.Join(wt, "a", "b", "file.txt"); dst != want {
		t.Errorf("nested dest = %q, want %q", dst, want)
	}
	if fi, err := os.Stat(filepath.Join(wt, "a", "b")); err != nil || !fi.IsDir() {
		t.Errorf("intermediate dirs not created: %v", err)
	}

	// A ".." component is refused outright.
	if got := safeOverlayDest(wt, "x/../../etc/passwd"); got != "" {
		t.Errorf(`".." dest = %q, want "" (refused)`, got)
	}

	// A symlinked intermediate is refused, and nothing is written through it.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(wt, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if got := safeOverlayDest(wt, "link/evil.txt"); got != "" {
		t.Errorf("symlinked-intermediate dest = %q, want %q (refused)", got, "")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Error("safeOverlayDest wrote through a symlink out of the worktree")
	}
}

// TestWorktreeIncludeSkipsQuotedNames pins a REFERENCE LIMITATION that claustrum
// reproduces on purpose. `git ls-files` C-quotes any path containing a tab, a
// quote, a backslash or a non-ASCII byte, and the line-delimited output is
// parsed as-is, so such a file is silently not copied.
//
// Probe-measured: given six manifest-matched files the reference copied only the
// two whose names git prints bare — plain and space-containing. Using `-z` here
// would fix the limitation and thereby DIVERGE, copying files the reference
// leaves behind, so the behaviour is asserted rather than corrected.
func TestWorktreeIncludeSkipsQuotedNames(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("tabs and quotes are not legal in Windows filenames")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "master", "repo")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "t\n", 0o644)
	// The weird files must be git-ignored to be copy candidates at all (7d193f89
	// copies only manifest matches that git also ignores); the quoted-name skip is
	// what this test isolates on top of that.
	writeFile(t, filepath.Join(repo, ".gitignore"), "weird*\n", 0o644)
	runGit(t, repo, "add", "tracked.txt", ".gitignore")
	writeFile(t, filepath.Join(repo, worktreeIncludeFile), "weird*\n", 0o644)
	for _, name := range []string{
		"weird-plain.txt",  // printed bare      -> copied
		"weird space.txt",  // printed bare      -> copied
		"weird\ttab.txt",   // C-quoted          -> skipped
		"weird\"quote.txt", // C-quoted          -> skipped
		"weird-café.txt",   // C-quoted (UTF-8)  -> skipped
	} {
		writeFile(t, filepath.Join(repo, name), "x\n", 0o644)
	}
	runGit(t, repo, "commit", "-m", "init")

	wt := filepath.Join(root, "wt")
	runGit(t, repo, "worktree", "add", "-b", "b", wt)
	populateWorktree(repo, wt)

	eqTree(t, treeOf(t, wt),
		[]string{".gitignore", "tracked.txt", "weird space.txt", "weird-plain.txt"},
		"quoted-name manifest matches")
}

// copyFile's two remaining give-up paths, which the directory-permission cases
// above cannot reach: they fail at MkdirAll before the file is ever opened.
func TestCopyFileOpenAndWriteFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on POSIX open/write errors")
	}

	// Destination exists as a DIRECTORY: MkdirAll succeeds, OpenFile fails.
	t.Run("destination_is_a_directory", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "a.txt")
		writeFile(t, src, "x\n", 0o644)
		dst := filepath.Join(dir, "out", "a.txt")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		copyFile(src, dst) // must give up quietly
		fi, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() {
			t.Error("copyFile replaced a directory with a file")
		}
	})

	// Write failure mid-copy: /dev/full accepts the open and fails every write
	// with ENOSPC, which is the only portable way to reach the io.Copy branch.
	t.Run("write_fails", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("/dev/full is a Linux device")
		}
		if _, err := os.Stat("/dev/full"); err != nil {
			t.Skip("/dev/full not present")
		}
		dir := t.TempDir()
		src := filepath.Join(dir, "a.txt")
		writeFile(t, src, "some bytes to write\n", 0o644)
		copyFile(src, "/dev/full") // must return without panicking
	})
}
