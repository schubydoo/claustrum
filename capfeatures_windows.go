//go:build windows

package main

// osCapabilityFeatures is files.realpath.how_far on Windows. 87375cc7 lists it after
// files.read.real and before server.instance_id (measured on Windows). The reference
// gates the custom-worktree-location capability off there and does not advertise
// git.worktree.external_root in server.capabilities (measured against 7d193f89).
// 683cc6a9 lists no process.spawn.link_bound on Windows either (measured on
// Windows), and b3081b51 lists no process.spawn.cli_temp_root there (measured on Windows).
// c9125eb8 lists no files.mkdir.verified_parent there (measured on Windows).
var osCapabilityFeatures = []string{"files.realpath.how_far"}

// osCapabilityMethods is empty on Windows: c9125eb8 has no files.mkdir there,
// and 4c111e86 has no files.find there (measured on Windows).
var osCapabilityMethods []string
