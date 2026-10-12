//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallWorktreeIndexCopy pins the copy of installWorktreeIndex on Windows.
// The file of the checkout is never moved: it stays with its bytes, as on
// 5fd08069 (cell V5i of val-s3p2b2-5-windows, Windows VM). A full copy lands at
// dst. A write that fails partway leaves no dst and no temporary file, so git
// never reads a partial index.
func TestInstallWorktreeIndexCopy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failCopy bool
	}{{"full copy", false}, {"failed copy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(t.TempDir(), "index")
			dst := filepath.Join(dir, "index")
			if err := os.WriteFile(src, []byte("0123456789"), 0o644); err != nil {
				t.Fatal(err)
			}
			rename, write := indexRename, indexWrite
			t.Cleanup(func() { indexRename, indexWrite = rename, write })
			indexRename = func(from, to string) error {
				if from == src {
					t.Errorf("the index of the checkout was moved to %s, want a copy", to)
				}
				return rename(from, to)
			}
			if tc.failCopy {
				indexWrite = func(f *os.File, b []byte) error {
					_, _ = f.Write(b[:4])
					return errors.New("no space left on device")
				}
			}
			if err := installWorktreeIndex(src, dir); err != nil {
				t.Errorf("installWorktreeIndex = %v, want nil on Windows", err)
			}
			got, err := os.ReadFile(dst)
			if tc.failCopy {
				if err == nil {
					t.Errorf("dst = %q after a failed copy, want no index", got)
				}
			} else if string(got) != "0123456789" {
				t.Errorf("dst = %q, %v, want the full index", got, err)
			}
			if left, err := os.ReadFile(src); err != nil || string(left) != "0123456789" {
				t.Errorf("the index of the checkout after the copy = %q, %v, want it to stay", left, err)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.Name() != "index" {
					t.Errorf("leftover %s in the admin dir", e.Name())
				}
			}
		})
	}
}
