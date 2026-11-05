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
// each name is tested first, so those two paths fail. No production caller takes that
// order.
func spelledWalkFails(p string, stat func(string) (fs.FileInfo, error), foldFirst bool) bool {
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
		fi, err := stat(cur)
		if err != nil || (i < len(names)-1 && !fi.IsDir()) {
			return true
		}
	}
	return false
}
