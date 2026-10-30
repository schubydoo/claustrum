//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A git directory of 4086 bytes, so `commondir` in it is a path of 4096 bytes and its Lstat
// fails with "file name too long", is refused with the hooks prefix and the could-not-be-
// examined text (build 683cc6a9, measured on Linux). Mutation: remove the ENAMETOOLONG
// branch in judgeStrayCommondir.
func TestStrayCommondirNameTooLong(t *testing.T) {
	base := t.TempDir()
	deep := base
	for len(deep) < 4086 {
		// At least 1 byte, so the loop ends: the slash costs 1 and the chunk the rest.
		deep = filepath.Join(deep, strings.Repeat("d", max(1, min(200, 4086-len(deep)-1))))
	}
	if len(deep) != 4086 {
		t.Fatalf("deep path has %d bytes", len(deep))
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	rel := strings.TrimPrefix(deep, base+"/")
	if err := root.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"refs", "objects"} {
		if err := root.Mkdir(filepath.Join(rel, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.WriteFile(filepath.Join(rel, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "w")
	writeFile(t, filepath.Join(work, ".git"), "gitdir: "+deep+"\n", 0o644)
	got := gitDirTrustFor(work)
	want := "config-defined hooks could not be pinned off; git not run: this repository's git directory " +
		"could not be examined (file name too long); if .git is a file, check the path written in it"
	if got.verdict != gitDirRefused || got.refusal != want {
		t.Errorf("verdict %v, refusal %q, want %q", got.verdict, got.refusal, want)
	}
}
