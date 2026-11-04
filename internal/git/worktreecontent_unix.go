//go:build unix

package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// lacksExecBit reports whether fi has no execute bit for its owner. Only the
// owner bit counts (claustrum's own, not measured).
func lacksExecBit(fi fs.FileInfo) bool {
	return fi.Mode()&0o100 == 0
}

// committedLinkFault follows the chain of the link at path in leaf, one name at
// a time, through links inside and outside the new worktree. Only a link inside
// the new worktree counts, the link itself too. A circle there is too long
// (b3081b51, measured on Linux and macOS). A chain that leaves the worktree
// through a system link passes with 8 links in it (measured on Linux and
// macOS). More than contentLinkLimit links there is too long (measured on
// macOS). A chain that reaches /proc or /dev/fd, by a name or by a
// link, passes through them (measured on Linux and macOS). A name that cannot
// be read, and a walk past linkWalkBound links in all, end the walk with no
// fault (claustrum's own, not measured).
func committedLinkFault(leaf, path string) linkFault {
	root, err := filepath.EvalSymlinks(leaf)
	if err != nil {
		return linkNoFault
	}
	cur := root
	pending := strings.Split(path, "/")
	inside, all := 0, 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
		default:
			next := filepath.Join(cur, name)
			fi, err := os.Lstat(next)
			if err != nil {
				return linkNoFault
			}
			if fi.Mode()&fs.ModeSymlink == 0 {
				cur = next
				break
			}
			if all++; all > linkWalkBound {
				return linkNoFault
			}
			if underFolder(next, root) {
				if inside++; inside > contentLinkLimit {
					return linkTooLong
				}
			}
			target, err := os.Readlink(next)
			if err != nil {
				return linkNoFault
			}
			if filepath.IsAbs(target) {
				cur = "/"
			}
			pending = append(strings.Split(target, "/"), pending...)
		}
		if underFolder(cur, "/proc") || underFolder(cur, "/dev/fd") {
			return linkThroughProc
		}
	}
	return linkNoFault
}

// linkWalkBound ends the walk of a chain that circles outside the new worktree
// (claustrum's own, not measured).
const linkWalkBound = 40

// underFolder reports whether p is dir or a path below it.
func underFolder(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
