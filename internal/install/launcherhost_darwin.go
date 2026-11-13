//go:build darwin

package install

// LauncherCheckHostRefusal is the refusal of every launcherCheck on macOS: each
// process.spawn with a launcher and a launcherCheck, and each launcher.resolve with a
// launcherCheck and no path, a resolve with no settings file too (c9125eb8, measured
// on macOS). A resolve with a path gets it too (claustrum's own, not measured). No file
// test runs before it. The text follows 4c111e86 (measured on macOS for a resolve with
// no path). For a process.spawn and a resolve with a path the same text is claustrum's
// own, not measured.
func LauncherCheckHostRefusal() (fault, reason string) {
	return FaultMacOSHost, "This launcher can't be used on a macOS host: an access-control list there can let another account change it unseen. Use a Linux host."
}
