//go:build !linux

package install

// lookLauncherPath and launcherMoved do nothing off Linux. macOS and Windows refuse a
// launcherCheck before the walk (LauncherCheckHostRefusal), and claustrum lists no
// launcher.checked_by_handle there.
func lookLauncherPath(string) ([]struct{}, error) { return nil, nil }

func launcherMoved(string, []struct{}) (fault, reason string) { return "", "" }
