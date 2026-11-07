//go:build !darwin && !windows

package install

// LauncherCheckHostRefusal is empty on Linux, where the file check runs.
func LauncherCheckHostRefusal() (fault, reason string) { return "", "" }
