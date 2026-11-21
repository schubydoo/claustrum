package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// baseRepoWalkStat is os.Stat behind a seam, so a test can fail the walk of
// spelledWalkFails at one path. Production never reassigns it.
var baseRepoWalkStat = os.Stat

// linkWalkFails reports whether the walk of baseRepoWalkFails and of the git.list_branches
// path fails on p. On Linux and macOS it is the walk of filepath.EvalSymlinks
// (evalSymlinks). With baseRepoWalksAsSpelled (Windows) it is spelledWalkFails, and
// foldFirst picks its order of "..".
func linkWalkFails(p string, foldFirst bool) bool {
	if baseRepoWalksAsSpelled {
		return spelledWalkFails(p, baseRepoWalkStat, foldFirst)
	}
	_, err := evalSymlinks(p)
	return err != nil
}

// spelledWalkFails walks p by its names as sent. Each name joins the path before it,
// and stat must accept the result, which follows a junction or a link. A name with more
// names after it must be a directory. A ".." drops the name before it by text, and a
// "." is skipped. So a junction J in <out>\J\..\repo leads to <out>\repo. stat is os.Stat.
//
// With foldFirst a ".." drops the name before it before that name is tested, so only the
// names that remain are walked. The baseRepo of git.worktree_create and
// git.worktree_remove and the git.list_branches path take that order. b3081b51 judges
// <T>\missing\.. and <T>\t.txt\.. as <T> there (measured on Windows). Without foldFirst
// each name is tested first, so those two paths fail. The walk of statusBaseWalkFails
// folds first too.
func spelledWalkFails(p string, stat func(string) (fs.FileInfo, error), foldFirst bool) bool {
	return walkSpelled(p, stat, stat, foldFirst)
}

// statusBaseWalkFails reports whether the walk of a git.status baseRepo fails. On Linux
// and macOS it is the walk of evalSymlinks. On Windows the names before the last name
// take spelledWalkFails with foldFirst, so a ".." after a missing name or a junction
// drops that name
// (4c111e86, measured on Windows; <T>\t.txt\.. there is claustrum's own, not
// measured). The last name only needs an entry, so a
// dangling junction passes, and the later os.Stat in gitStatus answers it after the
// excludes read (measured on Windows). A link as the last name is followed, as the
// filepath.EvalSymlinks gate before it did (from the code, not measured).
func statusBaseWalkFails(p string) bool {
	if !baseRepoWalksAsSpelled {
		return linkWalkFails(p, false)
	}
	return walkSpelled(p, baseRepoWalkStat, lastNameEntry, true)
}

// lastNameEntry is os.Lstat, except that it follows a symbolic link.
func lastNameEntry(p string) (fs.FileInfo, error) {
	fi, err := os.Lstat(p)
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return os.Stat(p)
	}
	return fi, err
}

// walkSpelled is the walk of spelledWalkFails. lastStat tests the last name, and stat
// tests each name before it.
func walkSpelled(p string, stat, lastStat func(string) (fs.FileInfo, error), foldFirst bool) bool {
	vol := filepath.VolumeName(p)
	cur, rest := vol, p[len(vol):]
	if rest != "" && os.IsPathSeparator(rest[0]) {
		cur += string(filepath.Separator)
	}
	var names []string
	for _, n := range strings.FieldsFunc(rest, func(r rune) bool { return r < utf8.RuneSelf && os.IsPathSeparator(uint8(r)) }) {
		switch {
		case n == ".":
		case n == ".." && foldFirst && len(names) > 0 && names[len(names)-1] != "..":
			names = names[:len(names)-1]
		default:
			names = append(names, n)
		}
	}
	for i, n := range names {
		cur = filepath.Join(cur, n)
		if n == ".." {
			continue
		}
		test := stat
		if i == len(names)-1 {
			test = lastStat
		}
		fi, err := test(cur)
		if err != nil || (i < len(names)-1 && !fi.IsDir()) {
			return true
		}
	}
	return false
}
