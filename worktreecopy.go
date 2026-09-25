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
// recognise a managed worktrees tree. The reference ALSO seeds a new worktree with
// the repo's git-ignored `.claude/` files, outside the manifest rule below: see
// copyClaudeDir in worktreeclaude.go.
const claudeDirName = ".claude"

// worktreesSubdir is the child of .claude that holds session worktrees.
const worktreesSubdir = "worktrees"

// populateWorktree seeds a freshly created worktree the way 7d193f89 does. A
// `git worktree add` gives a clean checkout of tracked files only, so a declared
// untracked file is missing unless copied — see copyWorktreeIncludes for the rule.
//
// Best-effort: the worktree already exists, so a copy failure does not turn into a
// failed request.
// The manifest copy runs first, then the `.claude/` copy. The order is observable
// when both name the same path, because the second copy overwrites the first.
func populateWorktree(repo, worktree string) {
	copyWorktreeIncludes(repo, worktree)
	copyClaudeDir(repo, worktree)
}

// copyWorktreeIncludes copies the untracked files that the `.worktreeinclude`
// manifest names AND the standard gitignore rules ignore. The manifest is an
// include-filter over the git-ignored set, not a copy list of its own. A manifest
// match that git does not ignore is NOT copied (measured against 7d193f89).
//
// `.claude/` is NOT part of this pass. It has its own, always-on pass with no
// manifest involved: copyClaudeDir in worktreeclaude.go.
//
// The steps were measured against f6010b97 on a macOS VM. The version parse,
// opening rules, counts, batches and error arms were re-checked on a Linux VM.
//
//   - The manifest must be a regular file at the repo root. A symlink or a
//     directory copies nothing and runs no git. An empty regular file still
//     runs `git version` and the scan, and it copies nothing.
//   - `git version` selects the scan. Git 2.32.0 or later gets the new scan in
//     worktreeinclude.go. Older git, output that does not parse, or a failed
//     `git version` gets the old scan (oldWorktreeIncludeScan).
//   - The new scan falls back to the old scan when the ignored files are too many.
//
// Both scans pass git a temp copy of the manifest bytes, not the manifest path.
//
// `-z` on every view. git C-quotes any path containing a tab, a quote, a
// backslash or a non-ASCII byte. The quoted form names no real file, so a
// line-delimited read drops it. The reference copies all four shapes (measured
// against 19f30c46 and 90fca6e6).
//
// stdout ONLY, for the same reason as gitListBranches: this splits the result
// into paths, so a warning on stderr becomes a bogus path.
//
// ⚠️ An OPTED-IN gitTimeout (D5) kills any of these calls, and the failure is
// silent. The copy loses what that call gives it, and gitWorktreeCreate still
// answers {"success":true}. D5 is off by default.
func copyWorktreeIncludes(repo, worktree string) {
	manifest, ok := readWorktreeInclude(repo)
	if !ok {
		return
	}
	tmp, err := os.CreateTemp("", includeTempPrefix+"*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(manifest)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		return
	}

	var paths []string
	fallback := true
	if gitSelectsIncludeScan() {
		paths, fallback = scanWorktreeIncludes(repo, tmp.Name(), manifest)
	}
	if fallback {
		paths = oldWorktreeIncludeScan(repo, tmp.Name())
	}
	for _, rel := range paths {
		// A path with a trailing `/` is a nested repository. git lists it as one
		// entry. The reference creates nothing for it, so neither does claustrum.
		// A runtime-state path under `.claude/` is skipped even when the manifest
		// names it, matching 90fca6e6. See isClaudeRuntimeState.
		if strings.HasSuffix(rel, "/") || isClaudeRuntimeState(rel) {
			continue
		}
		if dst := safeOverlayDest(worktree, rel); dst != "" {
			copyFile(filepath.Join(repo, rel), dst)
		}
	}
}

// readWorktreeInclude reads the manifest from the working tree. It does not
// follow a symlink: both references copy nothing for a symlinked manifest. An
// empty regular file is a manifest. f6010b97 still runs the scan for it.
func readWorktreeInclude(repo string) ([]byte, bool) {
	path := filepath.Join(repo, worktreeIncludeFile)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// oldWorktreeIncludeScan is the old scan. f6010b97 runs it for git below 2.32.0
// and for the fallback. `git ls-files` lists the ignored files that the manifest
// names, and `git check-ignore` keeps the ones that the standard ignore rules
// match. The check honours the user's global excludes, so a file ignored only by
// ~/.config/git/ignore is copied when the manifest names it. If either call
// fails, nothing is copied.
func oldWorktreeIncludeScan(repo, excludeFile string) []string {
	named, err := hardenedGitStdout(repo, false, "--no-literal-pathspecs", "ls-files", "--others",
		"--ignored", "--exclude-from="+excludeFile, "-z", "--", ":(exclude)"+claudeDirName+"/"+worktreesSubdir)
	if err != nil || named == "" {
		return nil
	}
	paths, _ := checkIgnored(repo, splitNUL(named))
	return paths
}

// safeOverlayDest resolves the destination for a manifest-copied file inside the
// worktree, creating missing intermediate directories (0755) but REFUSING to
// traverse a ".." or a symlinked component. A planted symlink inside the worktree
// must not carry a copy outside it. Returns the destination path, or "" if the path
// is unsafe. rel is worktree-relative.
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
// or 0400.
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
