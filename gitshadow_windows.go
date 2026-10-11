//go:build windows

package main

import "os"

// shadowLinksWorktrees is false on Windows. The copy of a repository has no
// `worktrees` entry and no reparse point there on 5fd08069 (s3-windows cell A3
// and cell 22 of s3p2b-edges, Windows VM).
const shadowLinksWorktrees = false

// holdShadowLock does nothing on Windows: the open file is the hold. The lock
// file of a live 5fd08069 daemon is in use there and cannot be removed
// (s3-windows cell B6, Windows VM). A file that Go opened has no delete
// sharing, so the same holds for the handle of claustrum.
func holdShadowLock(*os.File) error {
	return nil
}
