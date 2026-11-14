//go:build !windows

package main

// osCapabilityFeatures adds git.worktree.external_root, process.spawn.link_bound,
// process.spawn.cli_temp_root and process.spawn.cli_temp_root_end to the advertised
// server.capabilities features on unix.
// The reference lists none of them on Windows (external_root: 7d193f89, link_bound:
// 683cc6a9, cli_temp_root: b3081b51). b3081b51 lists cli_temp_root after a terminal
// name that claustrum does not list, so it comes before server.instance_id.
// c9125eb8 lists files.mkdir.verified_parent after files.read.real, before
// external_root, on Linux and macOS (measured).
// 4c111e86 lists git.worktree_create.own_lfs_store after link_bound on Linux and
// macOS (measured), for the private LFS store of a create (lfsstore.go). It lists
// git.worktree_create.lfs_error_codes after it (measured on Linux and macOS).
// 4c111e86 lists cli_temp_root_end right after cli_temp_root on Linux and macOS,
// and not on Windows (measured).
// On Linux the two launcher features of launcherOSFeatures follow.
var osCapabilityFeatures = append([]string{"files.mkdir.verified_parent", "git.worktree.external_root", "process.spawn.link_bound", "git.worktree_create.own_lfs_store", "git.worktree_create.lfs_error_codes", "process.spawn.cli_temp_root", "process.spawn.cli_temp_root_end"}, launcherOSFeatures...)

// osCapabilityMethods is files.mkdir, which c9125eb8 lists after plugins.prune and
// before files.realpath on Linux and macOS (measured). 4c111e86 lists files.find
// before files.mkdir there (measured on Linux and macOS).
var osCapabilityMethods = []string{"files.find", "files.mkdir"}
