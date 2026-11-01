package main

import "testing"

// noFolderHandle is a no-op on Windows: it has no long-path route.
func noFolderHandle(*testing.T) {}
