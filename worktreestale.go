package main

import "path/filepath"

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
