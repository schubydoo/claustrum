//go:build windows

package main

// osCapabilityFeatures is empty on Windows: the reference gates the
// custom-worktree-location capability off there and does not advertise
// git.worktree.external_root in server.capabilities (measured against 7d193f89).
// 683cc6a9 lists no process.spawn.link_bound on Windows either (Windows VM).
var osCapabilityFeatures []string
