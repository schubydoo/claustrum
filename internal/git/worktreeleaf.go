package git

import (
	"path/filepath"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// finalLeafPath is pathcanon.FinalPath behind a seam, so a test on Linux can stand
// in for the path of a Windows handle. Production never reassigns it.
var finalLeafPath = pathcanon.FinalPath

// leafResolve is the leaf of git.worktree_create with its links resolved, for the
// checkpoint and its later tests. It is filepath.EvalSymlinks (evalSymlinks). With
// leafResolvesByHandle (Windows), a leaf that EvalSymlinks cannot resolve gets the
// path of an open handle (finalLeafPath). On Windows EvalSymlinks fails at a junction
// before the last name. b3081b51 creates a worktree through such a junction, at
// <out>\J\.claude\worktrees\w1 and at <out>\JP\repo\.claude\worktrees\w1 (measured on
// Windows).
func leafResolve(p string) (string, error) {
	r, _, err := leafResolveHow(p)
	return r, err
}

// leafResolveHow is leafResolve, and byHandle reports that the path is the path of a
// handle (finalLeafPath).
func leafResolveHow(p string) (r string, byHandle bool, err error) {
	r, err = evalSymlinks(p)
	if err == nil || !leafResolvesByHandle {
		return r, false, err
	}
	if h, herr := finalLeafPath(p); herr == nil {
		return h, true, nil
	}
	return "", false, err
}

// leafByHandle is the path of an open handle of p, when leafResolvesByHandle is set
// and filepath.EvalSymlinks fails on p. ok is false in every other case.
func leafByHandle(p string) (string, bool) {
	if !leafResolvesByHandle {
		return "", false
	}
	if _, err := evalSymlinks(p); err == nil {
		return "", false
	}
	h, err := finalLeafPath(p)
	return h, err == nil
}

// leafRecordPath is the leaf as the gitdir record of the staging step names it,
// before its slashes change (stagedRecordText, stagedRecordRefusal). It is
// resolveAsFarAsExists, or leafByHandle on Windows. b3081b51 writes the record of the
// leaf with the junction resolved (measured on Windows).
func leafRecordPath(leaf string) string {
	leaf = filepath.Clean(leaf)
	if h, ok := leafByHandle(leaf); ok {
		return h
	}
	return resolveAsFarAsExists(leaf)
}
