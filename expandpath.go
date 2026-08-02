package main

import (
	"os"
	"path/filepath"
	"strings"
)

// expandPath resolves a leading `~` against the daemon user's home directory,
// matching the reference daemon's handlers.expandPath / process.expandPath.
//
// The rule is deliberately narrow. Every clause below was probe-measured against
// the reference at 5db5e4a on 2026-08-02, reading the string the reference ECHOES
// back (git.worktree_create reflects worktreePath verbatim into result.path), with
// the equivalent absolute-form request sent in the same run as the control:
//
//	"~"          -> <home>                 expanded, NOT cleaned — verbatim
//	"~/"         -> <home>                 expanded, trailing separator stripped
//	"~/f.txt"    -> <home>/f.txt           expanded
//	"~//f.txt"   -> <home>/f.txt           expanded, doubled separator collapsed
//	"~/a/./b"    -> <home>/a/b             expanded, "." resolved
//	"~/a/x/../b" -> <home>/a/b             expanded, ".." resolved LEXICALLY
//	"~user/f"    -> unchanged              NOT expanded, even for a real user
//	"/tmp/~/f"   -> unchanged              a mid-path ~ is a literal directory name
//	"$HOME/f"    -> unchanged              neither daemon expands environment vars
//
// So the reference cleans the expanded remainder, and the cleaning applies to the
// TILDE form ONLY: every absolute-form control came back uncleaned, keeping the
// caller's spelling and OS resolution. Bare "~" is the exception in the other
// direction — it returns the home directory verbatim, so a HOME of "/home/me/"
// echoes back with its trailing slash intact while "~/" does not.
//
// CORRECTION, 2026-08-02: this table previously recorded "~/" -> <home>/ and
// "~//f.txt" -> <home>//f.txt as probe-measured, and concluded that no cleaning
// was applied. Those two rows were never actually measured. The probe behind them
// asserted with files.validate, whose reply is {valid,isDir} and carries no path
// at all, so it could not have observed a spelling difference — the recorded
// values were an inference, and the inference was wrong. Anything reading the old
// table as fact should re-check against this one.
//
// Only "~/" is treated as a separator, not "~\". Measured on Windows 11 against
// the reference at 5db5e4a on 2026-08-02, same method (echoed result.path, with
// the absolute form as the per-row control):
//
//	"~\a7"       -> "~\a7"                 NOT expanded — "~\" is not a tilde form
//	"~/a1"       -> <home>\a1              expanded, "/" rewritten to "\"
//	"~/a4\x\..\w"-> <home>\a4\w            expanded, "\" IS a separator for ".."
//	"~/a5/"      -> <home>\a5              trailing separator stripped
//	"~//a6"      -> <home>\a6              doubled separator collapsed
//	"~"          -> <home>                 verbatim: a home of "C:\h\" keeps its "\"
//
// So Windows gets the same tilde-only cleaning as Unix, in Windows separator
// terms, and every absolute-form control came back verbatim there too. That is
// why the clean is NOT build-tagged to Unix: filepath.Join reproduces each row
// above exactly. The home itself comes from USERPROFILE, not HOME, which is what
// os.UserHomeDir already does.
//
// This paragraph previously said the Windows behaviour was unmeasured and that
// the function would not guess at it. It is measured now; the rows are above.
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
	// Bare "~" returns the home directory untouched. The reference does not clean
	// this one: measured at 5db5e4a with HOME set to "<R>/", "<R>/./" and "<R>//",
	// it echoed each back verbatim, while "~/" under the same HOME came back
	// cleaned. Cleaning here would diverge on any HOME with a trailing separator.
	if p == "~" {
		return home
	}
	// Everything after "~/" is joined and cleaned, which resolves ".." LEXICALLY
	// rather than letting the OS walk a symlink into its parent. Measured at
	// 5db5e4a with ~/link -> b/c and both ~/x.txt and ~/b/x.txt present:
	//
	//	~/link/../x.txt        reference "in-home"   claustrum "in-b"
	//	<abs>/link/../x.txt    reference "in-b"      claustrum "in-b"
	//
	// That row establishes the ".." case by which FILE was read. The other three
	// operations Join applies — collapsing "//", resolving ".", and stripping a
	// trailing separator — are established separately, by the echoed-string table
	// above. All four belong here rather than in the callers, because the absolute
	// form keeps OS resolution on both daemons. Same request, different file.
	return filepath.Join(home, p[2:])
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
