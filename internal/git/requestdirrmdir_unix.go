//go:build unix

package git

import (
	"os"
	"path/filepath"

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// rmdirRequestDir removes the empty folder name in folder, whose identity at
// the mkdir is parent. It is a variable so that a test can see the call.
var rmdirRequestDir = rmdirInFolder

// removeEmptyRequestDir removes name, a folder of a request that requestDir
// made by its path in folder, the folder of the daemon of mode 0300. removeEntry
// fails there at the open of that folder. With mode 0300 4c111e86 then removes
// an empty request folder, and a folder with content stays (measured on
// Linux). claustrum runs one rmdir, never a tree, after
// homeguard.WipesHomeDir. The rmdir runs only while folder still has the
// identity parent (rmdirInFolder). A file at the name stays (claustrum's own,
// not measured). macOS is not measured.
func removeEmptyRequestDir(folder, name string, parent os.FileInfo) {
	if homeguard.WipesHomeDir(filepath.Join(folder, name)) {
		return
	}
	_ = rmdirRequestDir(folder, name, parent)
}
