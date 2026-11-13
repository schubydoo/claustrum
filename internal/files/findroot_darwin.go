package files

import "github.com/schubydoo/claustrum/internal/homeguard"

// findRootPreCut is the home test of files.find on macOS (findMacRootCut). Home
// is the HOME of the daemon (homeguard.WipesHomeDir).
func findRootPreCut(root string) string {
	return findMacRootCut(root, homeguard.WipesHomeDir)
}
