//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// gitInfoRoot is the root of git.info on Windows. 89cb6289 asks git there, in place of
// the older `rev-parse --show-toplevel` in the request folder: a listing and then
// `--git-dir=<git dir> --work-tree=<walk root> rev-parse --show-toplevel`, both in
// the git dir (gitDirWorkTreeToplevel). The git dir is the GIT_COMMON_DIR pin. When
// git names the same directory as the walk root, the root is git's answer as printed,
// with forward slashes. Else it is the walk root with forward slashes. Measured on a
// Windows VM (rows W01 to W16 and D03).
//
// With no pin claustrum uses the git directory the walk found. One such state is
// measured: with a daemon GIT_COMMON_DIR the pair and the root are equal to 89cb6289
// in every call (cell B-13). The other states with no pin are not measured.
//
// Not measured either: the spelling of the fallback walk root (W14 and W16 sent the
// on-disk case), and how a relative answer is resolved (W16 only shows it is not
// taken, so claustrum takes no relative answer). claustrum compares by file identity
// (os.SameFile). Rows W08 (a subst drive) and W09 (a junction) answer git's spelling
// while the walk root is spelled another way. A string compare does not give those
// frames. No row isolates the compare.
func gitInfoRoot(walkRoot, walkGitDir string, pin []string) string {
	gitDir := walkGitDir
	if len(pin) > 0 {
		gitDir = strings.TrimPrefix(pin[0], "GIT_COMMON_DIR=")
	}
	out, err := gitDirWorkTreeToplevel(gitDir, walkRoot, pin)
	if err == nil && filepath.IsAbs(out) && sharesFileIdentity(out, walkRoot) {
		return out
	}
	return filepath.ToSlash(walkRoot)
}

// sharesFileIdentity reports whether a and b both exist and are one file on disk
// (os.SameFile).
func sharesFileIdentity(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// existingPathSpelling is the path of the `already exists` refusal of
// git.worktree_create without worktreeRoot. Each component that exists is spelled with
// its on-disk letter case, and the separators stay as sent. f6010b97 and 89cb6289
// name <T>\.claude\worktrees\w1 for a request that spells the parent folders in
// another letter case, on a Windows VM (row W15). Not measured: the volume name and an 8.3 short name
// (claustrum keeps both as sent), and the case rule with worktreeRoot.
func existingPathSpelling(p string) string {
	vol := filepath.VolumeName(p)
	var b strings.Builder
	b.WriteString(vol)
	dir := vol
	rest := p[len(vol):]
	for rest != "" {
		i := strings.IndexAny(rest, `\/`)
		if i == 0 {
			b.WriteByte(rest[0])
			dir += string(filepath.Separator)
			rest = rest[1:]
			continue
		}
		name := rest
		if i > 0 {
			name = rest[:i]
		}
		rest = rest[len(name):]
		b.WriteString(onDiskName(dir, name))
		dir = filepath.Join(dir, name)
	}
	return b.String()
}

// onDiskName is the entry of dir whose name equals name without regard to letter case,
// spelled as the directory holds it, or name when there is none.
func onDiskName(dir, name string) string {
	if name == "." || name == ".." {
		return name
	}
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	for _, e := range entries {
		if e.Name() == name {
			return name
		}
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			return e.Name()
		}
	}
	return name
}

// checkoutWorkTree is the --work-tree value of the read-tree checkout of
// git.worktree_create. On Windows it is the leaf as sent. The resolved leaf of the
// references is measured on Linux and macOS VMs only.
func checkoutWorkTree(leaf, _ string) string {
	return leaf
}

// adminRecordChecked reports whether git.worktree_create compares the new worktree's
// admin record with worktreePath after symlink resolution (adminRecordMismatch). It
// does not on Windows. The check was measured on a macOS VM only. On Windows git
// records a path with forward slashes and resolved links, and a byte compare refuses
// that. Leaving the check off there is claustrum's choice.
const adminRecordChecked = false
