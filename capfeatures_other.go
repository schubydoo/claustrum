//go:build !windows

package main

// osCapabilityFeatures adds git.worktree.external_root, process.spawn.link_bound and
// process.spawn.cli_temp_root to the advertised server.capabilities features on unix.
// The reference lists none of them on Windows (external_root: 7d193f89, link_bound:
// 683cc6a9, cli_temp_root: b3081b51). b3081b51 lists cli_temp_root after a terminal
// name that claustrum does not list, so it comes before server.instance_id.
var osCapabilityFeatures = []string{"git.worktree.external_root", "process.spawn.link_bound", "process.spawn.cli_temp_root"}
