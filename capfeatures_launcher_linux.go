//go:build linux

package main

// launcherOSFeatures adds launcher.checked_by_handle and launcher.local_kinds_only to the
// features of server.capabilities on Linux. 4c111e86 lists them in that order, right
// before server.instance_id (measured on Linux). It lists one more name before them,
// which claustrum does not build.
var launcherOSFeatures = []string{"launcher.checked_by_handle", "launcher.local_kinds_only"}
