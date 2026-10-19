package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// staleRecordLive reports whether the path that the gitdir record of a stale
// entry names, as written, still exists (D27). The Windows step keeps such an
// entry. Its compare ignores letter case, so in a folder with case sensitivity
// on, a live worktree W1 matches a create of w1. 5fd08069 deletes the entry of
// W1 there (cell W-CS of round task084-02, Windows VM). The guard is
// claustrum's own, by the maintainer's decision of 2026-10-18. A Lstat that
// fails with another error than "does not exist" keeps the entry too.
func staleRecordLive(record []byte) bool {
	_, err := os.Lstat(strings.TrimSpace(string(record)))
	return !errors.Is(err, fs.ErrNotExist)
}

// resolveAsFarAsExists canonicalises p by resolving the symlinks of its deepest
// existing ancestor and re-attaching the non-existent tail — so a path whose leaf is
// gone (a deleted worktree) still resolves a symlinked ancestor such as macOS
// /var -> /private/var, where canonicalPath alone falls back to the raw path.
func resolveAsFarAsExists(p string) string {
	p = filepath.Clean(p)
	if r := canonicalPath(p); r != p {
		return r // p itself exists and had symlinks to resolve
	}
	dir, base := filepath.Split(p)
	if dir == "" {
		return p
	}
	return filepath.Join(canonicalPath(filepath.Clean(dir)), base)
}
