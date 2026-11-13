//go:build unix && !darwin

package files

// findRootPreCut gives no cut on Linux: 4c111e86 walks home, its parent and "/"
// there (measured on Linux, with the HOME of the daemon read from /proc).
func findRootPreCut(string) string { return "" }
