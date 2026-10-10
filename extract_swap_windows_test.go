//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFilesExtractTarRefusesJunctionSwappedDestDir is the Windows twin of
// TestFilesExtractTarRefusesSwappedDestDir. Another process puts a junction at
// destDir between the mkdir and the open. The request then fails before an entry
// is written and before the marker remove, so the folder behind the junction
// keeps its content. Both folders are temp folders of this test, so a missing
// guard can reach nothing else. No reference build is measured there.
func TestFilesExtractTarRefusesJunctionSwappedDestDir(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(filepath.Join(other, ".synced"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(other, ".synced", "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	archive := filepath.Join(t.TempDir(), "a.tgz")
	writeTgz(t, archive, []tgzEntry{{name: "a.txt", body: "x\n"}}, 0)

	t.Cleanup(func() { openDestRoot = os.OpenRoot })
	openDestRoot = func(name string) (*os.Root, error) {
		// The swap: the new empty destDir goes, and a junction to the other
		// folder takes its place.
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		makeJunction(t, name, other)
		return os.OpenRoot(name)
	}
	// The junction goes before the temp folders are cleaned, as a link.
	t.Cleanup(func() { _ = os.Remove(dest) })

	n, err := extractTarGz(archive, dest)
	if want := `open destDir: "dest" changed while it was opened`; err == nil || err.Error() != want {
		t.Errorf("extractTarGz = %v, want %s", err, want)
	}
	if n != 0 {
		t.Errorf("fileCount = %d, want 0", n)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "keep\n" {
		t.Errorf("the file in the other folder = %q (err %v), want it unchanged", b, err)
	}
	left, err := os.ReadDir(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name() != ".synced" || !left[0].IsDir() {
		t.Errorf("the other folder holds %v, want only its .synced folder", left)
	}
}
