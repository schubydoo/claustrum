//go:build linux

package main

// launcherOSFeatures adds the three launcher features launcher.checked_by_handle,
// launcher.local_kinds_only and launcher.in_managed_folder to the features of
// server.capabilities on Linux. 4c111e86 lists the first two in that order, right
// before server.instance_id (measured on Linux). It lists one more name before them,
// which claustrum does not build. 13047a51 adds launcher.in_managed_folder after them
// (measured on Linux).
var launcherOSFeatures = []string{"launcher.checked_by_handle", "launcher.local_kinds_only", "launcher.in_managed_folder"}
