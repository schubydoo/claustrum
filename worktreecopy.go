package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// worktreeIncludeFile is the repo-root manifest naming untracked files to seed a
// new worktree with. The name is the reference's and is not configurable.
const worktreeIncludeFile = ".worktreeinclude"

// claudeDirName is copied into every new worktree regardless of the manifest.
const claudeDirName = ".claude"

// populateWorktree seeds a freshly created worktree the way the reference does.
// A `git worktree add` gives a clean checkout of tracked files only, so anything
// untracked — agent configuration, local env files — is missing unless copied.
// Under claustrum these worktrees came up bare.
//
// Two independent steps, both probe-measured against the reference at 5db5e4a:
//
//  1. `.claude/` is copied recursively and UNCONDITIONALLY, including nested
//     directories and dotfiles inside it. It does not need to appear in the
//     manifest, and an absent directory is skipped silently.
//  2. `.worktreeinclude` is an INCLUDE manifest in .gitignore syntax. Untracked
//     files matching it are copied even when they are also gitignored; without
//     the manifest, no untracked file is copied at all.
//
// Best-effort throughout: the worktree already exists and the reference reports
// success, so a copy failure must not turn into a failed request.
func populateWorktree(repo, worktree string) {
	copyClaudeDir(repo, worktree)
	copyWorktreeIncludes(repo, worktree)
}

// copyClaudeDir copies <repo>/.claude into the worktree. Absent ⇒ nothing to do.
func copyClaudeDir(repo, worktree string) {
	src := filepath.Join(repo, claudeDirName)
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return
	}
	copyDirRecursive(src, filepath.Join(worktree, claudeDirName))
}

// copyWorktreeIncludes copies the untracked files the manifest selects.
//
// The selection is `git ls-files --others --ignored --exclude-from=<manifest>`,
// deliberately WITHOUT --exclude-standard: that flag would fold .gitignore into
// the ignore set, so every gitignored file would be listed whether the manifest
// named it or not. Verified — with it, a gitignored `logs/l1.txt` the manifest
// never mentions is selected; without it, only manifest matches are.
//
// Line-delimited, and NOT `-z`, which is a deliberate parity choice rather than
// an oversight. git C-quotes any path containing a tab, a quote, a backslash or
// a non-ASCII byte, so those arrive here as display forms like
// "weird\ttab.txt" or "weird-caf\303\251.txt" and do not name a real file —
// they are silently skipped. The reference has exactly the same limitation
// (probe-measured: of six manifest-matched files it copied only the two whose
// names git prints bare), so `-z` would make claustrum copy MORE than the
// reference. Pinned by TestWorktreeIncludeSkipsQuotedNames; see
// docs/PROTOCOL.md, which tells manifest authors to stick to plain names.
func copyWorktreeIncludes(repo, worktree string) {
	if _, err := os.Stat(filepath.Join(repo, worktreeIncludeFile)); err != nil {
		return
	}
	// stdout ONLY, for the same reason as gitListBranches: this splits the result
	// into paths, so a warning on stderr becomes a bogus path. No wire impact —
	// the bogus path simply fails Lstat and is skipped — but it is the identical
	// defect and there is no reason to leave one of the two behind.
	//
	// ⚠️ An OPTED-IN gitTimeout (D5) kills this call, and the failure is silent: the
	// early return below skips every manifest-selected file while gitWorktreeCreate
	// still answers {"success":true}. Nothing reaches the wire, so no frame test and
	// no battery run can see it. Off by default since D5's flip, which is the only
	// reason it is not reachable today.
	out, err := gitStdoutErr(repo, "ls-files", "--others", "--ignored",
		"--exclude-from="+worktreeIncludeFile)
	if err != nil || out == "" {
		return
	}
	for _, rel := range strings.Split(out, "\n") {
		// One guarded call rather than `if rel == "" { continue }`: gitStdoutErr
		// strips the trailing newline and an empty `out` returned above, so no blank
		// element reaches this loop and a bare `continue` would be a statement
		// coverage can never reach. Same shape as the git.status porcelain loop.
		if rel = strings.TrimRight(rel, "\r"); rel != "" {
			copyFile(filepath.Join(repo, rel), filepath.Join(worktree, rel))
		}
	}
}

// copyDirRecursive copies a directory tree, entry by entry.
//
// Symlinks are skipped rather than recreated or dereferenced — probe-measured:
// a symlink named by the manifest does not appear in the reference's worktree.
// Skipping is also the safe reading, since following one would let a link inside
// the repo pull an arbitrary file into the worktree.
func copyDirRecursive(src, dst string) {
	ents, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return
	}
	for _, e := range ents {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case e.Type()&os.ModeSymlink != 0:
			continue
		case e.IsDir():
			copyDirRecursive(s, d)
		default:
			copyFile(s, d)
		}
	}
}

// copyFile copies one regular file, creating parent directories as needed.
// Anything that is not a regular file — a symlink, a socket, a device — is
// skipped.
//
// The copy is created 0666-subject-to-umask and the SOURCE MODE IS NOT
// PRESERVED, matching the reference. Probe-measured by varying the launcher's
// umask: with 022 every copy lands 0644, with 077 every copy lands 0600, with
// 000 every copy lands 0666 — regardless of whether the source was 0755, 0640
// or 0400. So the reference creates the file and never chmods it.
//
// Two consequences worth knowing, both inherited deliberately rather than
// "fixed" (see docs/PROTOCOL.md):
//   - an executable listed in the manifest arrives NON-executable
//   - a source file deliberately kept private (say 0400) is widened to whatever
//     the umask allows
func copyFile(src, dst string) {
	fi, err := os.Lstat(src)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return
	}
	_ = out.Close()
}
