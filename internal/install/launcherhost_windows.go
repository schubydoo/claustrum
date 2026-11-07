//go:build windows

package install

// LauncherCheckHostRefusal is the Windows refusal. A process.spawn refuses every
// launcher before this (HostRefusal). A launcher.resolve with a path and a
// launcherCheck gets it, with the fault of the spawn (claustrum's own, not measured).
func LauncherCheckHostRefusal() (fault, reason string) { return FaultWindowsHost, HostRefusal() }
