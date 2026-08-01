//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A tilde path is cleaned LEXICALLY, so "~/link/../x" means "~/x" even when
// "link" is a symlink pointing somewhere else. Letting the OS resolve it instead
// walks the symlink and lands in ITS parent — a different file for the same
// request.
//
// Measured at 5db5e4a with ~/link -> b/c and both ~/x.txt and ~/b/x.txt present:
//
//	~/link/../x.txt      reference "in-home"   claustrum "in-b"
//	<abs>/link/../x.txt  reference "in-b"      claustrum "in-b"
//
// The second row is why the clean lives in expandPath and not in the callers:
// an ABSOLUTE path keeps ordinary OS resolution on both daemons. An earlier
// probe compared only tilde paths with no symlink in them, where lexical and OS
// resolution agree — so it saw no difference and wrongly called this parity.
func TestExpandPathCleansTildeLexically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "b", "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b/c", filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ in, want string }{
		{"~/link/../x.txt", filepath.Join(home, "x.txt")},
		{"~/b/c/../x.txt", filepath.Join(home, "b", "x.txt")},
		{"~/./a", filepath.Join(home, "a")},
		{"~//a", filepath.Join(home, "a")},
		{"~", home},
		{"~/a", filepath.Join(home, "a")},
	}
	for _, tc := range cases {
		if got := expandPath(tc.in); got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Non-tilde paths are returned untouched — no cleaning, matching the
	// reference, which resolves an absolute "link/.." through the OS.
	abs := filepath.Join(home, "link", "..", "x.txt")
	if got := expandPath(abs); got != abs {
		t.Errorf("expandPath(%q) = %q, want it unchanged", abs, got)
	}
}
