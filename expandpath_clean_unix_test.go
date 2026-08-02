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
	// Two files with distinguishable contents, so "which file did we open?" is a
	// real question with a real answer. link -> b/c, so "link/.." is <home>/b and
	// an OS-resolved "link/../x.txt" reads <home>/b/x.txt, while the lexical
	// reading is <home>/x.txt.
	if err := os.WriteFile(filepath.Join(home, "x.txt"), []byte("in-home"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "b", "x.txt"), []byte("in-b"), 0o600); err != nil {
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

	// The symlink fixture is load-bearing, not decoration: read through both
	// spellings and prove lexical and OS resolution land on DIFFERENT files. This
	// is the measured discriminator itself — reference "in-home" vs claustrum
	// "in-b" — rather than a string comparison one layer upstream of it. Without
	// this, the MkdirAll/Symlink above affect no assertion and could be deleted
	// with the test still passing.
	if got := readFile(t, expandPath("~/link/../x.txt")); got != "in-home" {
		t.Errorf("tilde form read %q, want %q — the clean did not happen lexically", got, "in-home")
	}
	// Literal concatenation again, for the same reason as below: filepath.Join
	// would clean "link/.." away here too, and this assertion would then read
	// <home>/x.txt and pass while proving nothing about OS resolution.
	if got := readFile(t, home+"/link/../x.txt"); got != "in-b" {
		t.Errorf("OS-resolved form read %q, want %q — the fixture is not wired up", got, "in-b")
	}

	// Non-tilde paths are returned untouched. Built as a literal, NOT with
	// filepath.Join: Join cleans its own result, so a Join-built path arrives here
	// already free of the "link/.." this is meant to prove survives, and the
	// assertion would hold no matter what expandPath did.
	abs := home + "/link/../x.txt"
	if got := expandPath(abs); got != abs {
		t.Errorf("expandPath(%q) = %q, want it unchanged", abs, got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test-controlled path under t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
