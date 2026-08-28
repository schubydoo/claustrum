//go:build windows

package main

import "fmt"

// externalWorktreeUnsupportedRefusal reports the refusal when a custom worktree
// location (worktreeRoot) is requested on Windows. The reference (7d193f89) gates the
// external-worktree capability off entirely on Windows hosts, refusing before any
// location/containment check (measured byte-for-byte on the VM). verb is "create" or
// "remove". Off Windows this returns "" — custom worktree locations are supported on
// unix (externalunsupported_other.go). A var so a test on the linux coverage cell can
// stub it to exercise the worktree_create/remove guard branches in methods_git.go.
var externalWorktreeUnsupportedRefusal = func(worktreeRoot, verb string) string {
	return fmt.Sprintf("refusing to %s worktree: %s cannot be used: a custom worktree location is not supported on Windows hosts yet", verb, worktreeRoot)
}
