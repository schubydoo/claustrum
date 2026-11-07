//go:build darwin

package install

// LauncherCheckHostRefusal is the refusal of every launcherCheck on macOS: each
// process.spawn with a launcher and a launcherCheck, and each launcher.resolve with a
// launcherCheck, a resolve with no settings file too (c9125eb8, measured on macOS).
// No file test runs before it.
func LauncherCheckHostRefusal() (fault, reason string) {
	return FaultMacOSHost, "sshHostLauncher cannot be used with a macOS host: an access-control list there can let another account change the launcher without this file check seeing it."
}
