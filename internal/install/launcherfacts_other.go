//go:build !linux

package install

import "errors"

// statLauncherFacts is not built off Linux. macOS and Windows refuse a launcherCheck
// before the walk (LauncherCheckHostRefusal), so the walk never reaches it there.
func statLauncherFacts(string) (launcherFacts, error) {
	return launcherFacts{}, errors.ErrUnsupported
}
