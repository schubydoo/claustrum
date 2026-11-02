package main

import (
	"os"

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// The names of internal/homeguard under the names that the code of package main
// uses.

func wipesHomeDir(p string) bool { return homeguard.WipesHomeDir(p) }

func folderHoldsHome(cliPath string, fi os.FileInfo) bool {
	return homeguard.FolderHoldsHome(cliPath, fi)
}

func isFilesystemRoot(p string) bool { return homeguard.IsFilesystemRoot(p) }
