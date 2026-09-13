//go:build !linux

package main

// reapOrphans is a no-op off linux. The orphan registry's identity (boot id, pid
// namespace inode, /proc start-times) is linux-specific, and the child records are
// written only on linux (see childrecord_other.go), so there is nothing to reap. Darwin
// and Windows are reconciled in a later slice, matching how the exec-child trampoline and
// the child registry landed linux-first.
func reapOrphans(runDir, ownInstance string) {}
