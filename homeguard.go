package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// wipesHomeDir reports whether a recursive delete of p would take the daemon
// user's home directory with it — true when p IS the home directory, and true
// when p is an ANCESTOR of it ("/home", "/Users", a drive root).
//
// Why this exists, precisely. Two methods hand a caller-supplied path to
// os.RemoveAll: files.extract_tar wipes destDir before unpacking
// (methods_files.go), and git.worktree_remove deletes worktreePath when git
// refuses (methods_git.go). Both paths are `~`-expanded first — bindParams
// calls expandPaths on EVERY request (rpc.go), and expandPath returns the home
// directory verbatim for a bare "~" (expandpath.go). So `"destDir":"~"` reaches
// os.RemoveAll($HOME).
//
// That is not hypothetical. On 2026-08-02 an in-repo fuzzer sent "~" as destDir
// against a live daemon and destroyed the maintainer's home directory.
//
// The guard that was already there — `filepath.IsAbs(p) && !isFilesystemRoot(p)`
// — could not catch it, and the reason is worth recording because it is the
// general lesson: it was written to close a DIFFERENT hole (a Windows volume
// root passing an `== "/"` test, PR #224). "Absolute and not a filesystem root"
// is exactly what a home directory is. The guard was never wrong; it was
// answering a narrower question than the one that mattered.
//
// The question that matters is not "is this path special?" but "does deleting
// this path delete something the caller cannot have meant?", and containment
// answers it directly: RemoveAll(p) destroys everything under p, so if home is
// at or under p, home dies.
//
// DESCENDANTS OF HOME STAY ALLOWED, and that is load-bearing rather than a
// concession: extracting into ~/.claude/... is the daemon's primary real use.
// A guard that rejected the whole home subtree would refuse the product's own
// install path. Only the home directory itself and its ancestors are refused.
//
// This is a footgun guard, NOT a security boundary. A caller holding the socket
// and token can already run arbitrary commands via process.spawn (SECURITY.md),
// so anyone determined to delete a home directory has a shorter route. The job
// here is to stop an accidental, generated, or fat-fingered path — which is the
// shape the incident actually had — so lexical containment is the right depth.
//
// ACKNOWLEDGED LIMITATIONS — this list is meant to be exhaustive, so that a
// reader can trust it. There are two:
//
//  1. Symlinks are not resolved. A path that reaches home through a symlink is
//     accepted. Closing it would mean an EvalSymlinks on every request, whose
//     failure modes (a non-existent destDir is legal here) cost more than the
//     residual risk.
//  2. A path is resolved against the daemon's working directory, not the
//     caller's. That is the correct root — it is the one os.RemoveAll uses — but
//     it means a client cannot predict the guard's verdict on a relative path
//     without knowing where the daemon was started.
func wipesHomeDir(p string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// A daemon with no resolvable home behaves exactly as it did before this
		// guard existed. There is nothing to protect and nothing to compare against.
		return false
	}
	// RESOLVE RELATIVE PATHS FIRST. filepath.Clean does not make a path absolute,
	// so without this every relative input compares unequal to an absolute home
	// and fails BOTH tests below — the guard would answer false for ".." no matter
	// what ".." resolves to. That is not academic. git.worktree_remove performs
	// the same recursive delete with NO IsAbs gate of its own, and PROTOCOL.md
	// already measures that its fallback resolves worktreePath against the
	// DAEMON's working directory — so without this, the guard on that method
	// would miss ".." from a daemon sitting in a home directory, which is
	// os.RemoveAll on home's parent. os.RemoveAll refuses a trailing "." but has
	// no such guard for "..". Measured: unguarded, that spelling really does
	// delete the home directory. Raised in review on #231.
	//
	// filepath.Abs resolves against os.Getwd(), which is exactly the root
	// os.RemoveAll will use, so the guard judges the path the delete actually
	// hits. It is a no-op for files.extract_tar, where IsAbs has already run.
	// If Getwd fails the path stays relative and the guard fails open, matching
	// the unresolvable-home branch above.
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	dest, home := foldPath(filepath.Clean(p)), foldPath(filepath.Clean(home))
	if dest == home {
		return true
	}
	// Compare against dest WITH a trailing separator so a shared name prefix is
	// not mistaken for containment: "/home/claudex" must not be read as holding
	// "/home/claude". Clean leaves a root ("/" or `C:\`) already ending in the
	// separator, so appending unconditionally would build "//" and match nothing.
	//
	// Do NOT justify this with "isFilesystemRoot rejects roots before we get
	// here" — that holds only for files.extract_tar. git.worktree_remove has no
	// root check at all, so THIS branch is the only thing refusing a root there,
	// since every root contains the home directory.
	if !strings.HasSuffix(dest, string(os.PathSeparator)) {
		dest += string(os.PathSeparator)
	}
	return strings.HasPrefix(home, dest)
}

// foldPath normalizes case where the platform's own path comparison ignores it.
//
// Windows resolves `C:\USERS\x` and `C:\Users\x` to the same directory, so a
// guard that compared byte-for-byte there could be walked around by shouting.
// macOS's default filesystem is case-insensitive too, but Go exposes no portable
// way to ask a given path whether it is — and unlike Windows there is no
// platform-wide answer, since a case-sensitive APFS volume is a supported
// choice. Folding unconditionally would then refuse "/Users/Bob" on a machine
// where that genuinely is a different directory from "/Users/bob", turning a
// safety check into a wrong answer. The vector this guard exists for arrives in
// the platform's own spelling ("~", "$HOME", "/Users"), which matches exactly.
//
// The GOOS test lives in a var rather than inline so a test can exercise the
// case-insensitive semantics on any OS. Without that seam the folding branch is
// dead code on every leg but Windows — unreachable, unasserted, and reported as
// uncovered — which is a poor way to hold a safety check. Production never
// reassigns it.
var pathsAreCaseInsensitive = runtime.GOOS == "windows"

func foldPath(p string) string {
	if pathsAreCaseInsensitive {
		return strings.ToLower(p)
	}
	return p
}
