//go:build windows

package main

// externalRootCapabilityFeatures is empty on Windows: the reference gates the
// custom-worktree-location capability off there and does not advertise
// git.worktree.external_root in server.capabilities (measured against 7d193f89 —
// its Windows features list is process.stdin.offset + git.status.baseRepo only).
var externalRootCapabilityFeatures []string
