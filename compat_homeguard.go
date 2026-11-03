package main

import "github.com/schubydoo/claustrum/internal/homeguard"

// The names of internal/homeguard under the names that the code of package main
// uses.

func isFilesystemRoot(p string) bool { return homeguard.IsFilesystemRoot(p) }
