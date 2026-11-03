package main

import "testing"

// noFolderHandle is a no-op on macOS: it has no /proc route.
func noFolderHandle(*testing.T) {}
