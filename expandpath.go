package main

import (
	"os"
	"path/filepath"
	"strings"
)

// expandPath resolves a leading `~` against the daemon user's home directory,
// matching the reference daemon's handlers.expandPath / process.expandPath.
//
// The rule is deliberately narrow, and every clause below was probe-measured
// against the reference at 5db5e4a on 2026-07-30:
//
//	"~"          -> <home>                 expanded
//	"~/"         -> <home>/                expanded
//	"~/f.txt"    -> <home>/f.txt           expanded
//	"~//f.txt"   -> <home>//f.txt          expanded (the OS collapses the slashes)
//	"~user/f"    -> unchanged              NOT expanded, even for a real user
//	"/tmp/~/f"   -> unchanged              a mid-path ~ is a literal directory name
//	"$HOME/f"    -> unchanged              neither daemon expands environment vars
//
// Replacing just the leading "~" and keeping the remainder verbatim satisfies
// all of these, including the "~//" case, so no path cleaning is applied — a
// caller's spelling is otherwise preserved.
//
// Only "~/" is treated as a separator, not "~\": the reference was measured on
// Unix and its Windows behaviour for a backslash form is unmeasured, so this
// does not guess at it.
//
// A home directory that cannot be resolved leaves the path untouched rather than
// failing the request, so a daemon running without HOME behaves as it did before
// expansion existed.
func expandPath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	// filepath.Clean, so "~/link/../x" resolves LEXICALLY to "~/x" rather than
	// letting the OS walk the symlink and land in its parent. Measured at
	// 5db5e4a with ~/link -> b/c and both ~/x.txt and ~/b/x.txt present:
	//
	//	~/link/../x.txt        reference "in-home"   claustrum "in-b"
	//	<abs>/link/../x.txt    reference "in-b"      claustrum "in-b"
	//
	// So the cleaning applies to the TILDE form only — an absolute path keeps OS
	// resolution on both — which is why it belongs here rather than in the
	// callers. Same request, different file: worth getting exactly right.
	return filepath.Clean(home + p[1:])
}

// pathExpander is implemented by EVERY params struct, not just those carrying
// paths, and bindParams takes it as its parameter type rather than `any`. That
// makes expansion a compile-time obligation: a new params struct does not build
// until its author writes expandPaths and, in doing so, decides whether it has
// paths to expand.
//
// A type assertion inside bindParams was the obvious design and is the wrong
// one: a new path-bearing struct that forgot the method would fail the assertion
// silently and keep the literal-"~" behavior this file exists to remove. The
// failure mode of that bug is not an error — it is a request quietly operating
// on a path named "~", which is how claustrum came to create a literal `~`
// directory inside a user's repo.
//
// Structs with no path fields implement it as a no-op, which doubles as the
// declaration that they were considered.
type pathExpander interface {
	expandPaths()
}

func (p *pathParams) expandPaths() { p.Path = expandPath(p.Path) }
func (p *extractTarParams) expandPaths() {
	p.ArchivePath = expandPath(p.ArchivePath)
	p.DestDir = expandPath(p.DestDir)
}
func (p *gitParams) expandPaths() {
	p.Path = expandPath(p.Path)
	p.BaseRepo = expandPath(p.BaseRepo)
	p.WorktreePath = expandPath(p.WorktreePath)
	// BranchName and SourceBranch are refs, not paths — never expanded.
}
func (p *spawnParams) expandPaths() { p.Cwd = expandPath(p.Cwd) }

// The process-control params address a process by id and carry no filesystem
// paths, so expansion is a no-op. Declared explicitly rather than omitted: the
// interface is what forces that judgement to be made for every params type.
func (p *stdinParams) expandPaths()       {}
func (p *killParams) expandPaths()        {}
func (p *killAndWaitParams) expandPaths() {}
func (p *reattachParams) expandPaths()    {}
