package main

import (
	"path/filepath"
	"testing"

	"github.com/schubydoo/claustrum/internal/sockroute"
)

// noFolderHandle takes the /proc route away for one test.
func noFolderHandle(t *testing.T) {
	t.Helper()
	old := sockroute.SetProcSelfFDForTest(filepath.Join(t.TempDir(), "none"))
	t.Cleanup(func() { sockroute.SetProcSelfFDForTest(old) })
}
