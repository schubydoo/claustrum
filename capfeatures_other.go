//go:build !windows

package main

// externalRootCapabilityFeatures adds git.worktree.external_root to the advertised
// server.capabilities features on unix, where custom worktree locations are supported.
// On Windows the capability is gated off (capfeatures_windows.go), so it is omitted —
// matching the reference, which drops the feature from its Windows capabilities frame.
var externalRootCapabilityFeatures = []string{"git.worktree.external_root"}
