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

// claudeDirName is `.claude`, the repo child that holds session worktrees under
// <repo>/.claude/worktrees. worktreecontain.go uses it (with worktreesSubdir) to
// recognise a managed worktrees tree. 7d193f89 no longer copies `.claude/` into a
// new worktree (it did at 5db5e4a) — the directory is subject only to the manifest
// rule below, like any other untracked path.
const claudeDirName = ".claude"

// worktreesSubdir is the child of .claude that holds session worktrees.
const worktreesSubdir = "worktrees"

// populateWorktree seeds a freshly created worktree the way 7d193f89 does. A
// `git worktree add` gives a clean checkout of tracked files only, so a declared
// untracked file is missing unless copied — see copyWorktreeIncludes for the rule.
//
// Best-effort: the worktree already exists and the reference reports success, so a
// copy failure must not turn into a failed request.
func populateWorktree(repo, worktree string) {
	copyWorktreeIncludes(repo, worktree)
}

// copyWorktreeIncludes copies the untracked files 7d193f89 seeds a worktree with:
// those that are BOTH named by the `.worktreeinclude` manifest AND ignored by the
// repo's standard gitignore rules. The manifest is an include-filter over the
// git-ignored set, not a copy list of its own — a manifest match that git does not
// ignore is NOT copied (measured against 7d193f89; at 5db5e4a claustrum copied every
// manifest match and also copied `.claude/` unconditionally, both of which 7d193f89
// dropped).
//
// The two sets are the intersection of two `git ls-files` views: the manifest
// matches (--exclude-from) and the standard-ignored files (--exclude-standard).
//
// Line-delimited, and NOT `-z`, which is a deliberate parity choice rather than an
// oversight. git C-quotes any path containing a tab, a quote, a backslash or a
// non-ASCII byte, so those arrive as display forms like "weird\ttab.txt" that do not
// name a real file and are silently skipped — the reference has the same limitation
// (probe-measured), so `-z` would make claustrum copy MORE than the reference.
// Pinned by TestWorktreeIncludeSkipsQuotedNames; see docs/PROTOCOL.md.
func copyWorktreeIncludes(repo, worktree string) {
	if _, err := os.Stat(filepath.Join(repo, worktreeIncludeFile)); err != nil {
		return
	}
	// stdout ONLY, for the same reason as gitListBranches: this splits the result
	// into paths, so a warning on stderr becomes a bogus path.
	//
	// ⚠️ An OPTED-IN gitTimeout (D5) kills either call, and the failure is silent:
	// an early return skips the copy while gitWorktreeCreate still answers
	// {"success":true}. Off by default since D5's flip, which is why it is not
	// reachable today.
	manifest, err := hardenedGitStdout(repo, false, "ls-files", "--others", "--ignored",
		"--exclude-from="+worktreeIncludeFile)
	if err != nil || manifest == "" {
		return
	}
	// The git-ignored set uses the worktree profile so --exclude-standard honours the
	// user's global excludes (~/.config/git/ignore), matching 7d193f89 — a file
	// ignored only by the user's global config is copied when the manifest names it.
	ignored, err := hardenedGitStdout(repo, false, "ls-files", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return
	}
	ignoredSet := make(map[string]struct{})
	for _, rel := range strings.Split(ignored, "\n") {
		if rel = strings.TrimRight(rel, "\r"); rel != "" {
			ignoredSet[rel] = struct{}{}
		}
	}
	for _, rel := range strings.Split(manifest, "\n") {
		// One guarded call rather than `if rel == "" { continue }`: gitStdoutErr
		// strips the trailing newline and an empty `manifest` returned above, so no
		// blank element reaches this loop.
		if rel = strings.TrimRight(rel, "\r"); rel != "" {
			if _, ok := ignoredSet[rel]; ok {
				if dst := safeOverlayDest(worktree, rel); dst != "" {
					copyFile(filepath.Join(repo, rel), dst)
				}
			}
		}
	}
}

// safeOverlayDest resolves the destination for a manifest-copied file inside the
// worktree, creating missing intermediate directories (0755) but REFUSING to
// traverse a ".." or a symlinked component — 7d193f89's safeOverlayDest. A planted
// symlink inside the worktree must not carry a copy outside it. Returns the
// destination path, or "" if the path is unsafe. rel is worktree-relative.
func safeOverlayDest(worktree, rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := worktree
	for _, part := range parts[:len(parts)-1] { // intermediate directories only
		switch part {
		case "", ".":
			continue
		case "..":
			return ""
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			return "" // a symlinked component would carry the copy elsewhere
		case err == nil:
			// a real directory (or file — copyFile will then decline); continue
		default:
			if mkErr := os.Mkdir(cur, 0o755); mkErr != nil && !os.IsExist(mkErr) {
				return ""
			}
		}
	}
	return filepath.Join(cur, parts[len(parts)-1])
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
