//go:build !windows

package main

// osCapabilityFeatures adds git.worktree.external_root and process.spawn.link_bound
// to the advertised server.capabilities features on unix. The reference lists
// neither on Windows (external_root: 7d193f89, link_bound: 683cc6a9).
var osCapabilityFeatures = []string{"git.worktree.external_root", "process.spawn.link_bound"}
