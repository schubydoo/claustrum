package homeguard

import "runtime"

// homeEnvVar is the variable that os.UserHomeDir reads on this platform.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// wipesHomeDir is WipesHomeDir under the name that the tests of this package
// use.
func wipesHomeDir(p string) bool { return WipesHomeDir(p) }
