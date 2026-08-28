//go:build !windows

package main

import "testing"

// Off Windows, custom worktree locations (worktreeRoot) are supported — the guard is a
// no-op, so the external-worktree checks run normally (matching the reference on unix).
func TestExternalWorktreeSupportedOnUnix(t *testing.T) {
	if msg := externalWorktreeUnsupportedRefusal("/ext", "create"); msg != "" {
		t.Errorf("custom worktree location should be supported on unix, got refusal %q", msg)
	}
}
