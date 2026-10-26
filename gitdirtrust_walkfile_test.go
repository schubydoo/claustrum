package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWalkGitFileParse covers each part of the parse of a `.git` file that the walk
// of the trust check reads (readWalkGitFile).
func TestWalkGitFileParse(t *testing.T) {
	holder := t.TempDir()
	abs := filepath.Join(holder, "elsewhere", "g")
	slashX := filepath.Clean("/x")
	if runtime.GOOS == "windows" {
		slashX = filepath.Join(holder, "x")
	}
	cases := []struct {
		name, text, want string
		ok               bool
	}{
		{"absolute", "gitdir: " + abs + "\n", abs, true},
		{"relative", "gitdir: sub/g\n", filepath.Join(holder, "sub", "g"), true},
		{"no blank", "gitdir:" + abs + "\n", "", false},
		{"trailing blank kept", "gitdir: sub/g \n", filepath.Join(holder, "sub", "g "), true},
		{"leading second blank kept", "gitdir:  sub", filepath.Join(holder, " sub"), true},
		{"CR LF", "gitdir: sub/g\r\n\r\n", filepath.Join(holder, "sub", "g"), true},
		{"NUL", "gitdir: sub/g\x00junk\n", filepath.Join(holder, "sub", "g"), true},
		{"empty", "gitdir: \r\n", "", false},
		{"empty before NUL", "gitdir: \x00x", holder, true},
		{"slash path", "gitdir: /x\n", slashX, true},
		{"absolute cleaned", "gitdir: " + abs + "//../h\n", filepath.Join(holder, "elsewhere", "h"), true},
		{"cleaned", "gitdir: a//b/../g\n", filepath.Join(holder, "a", "g"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".git")
			if err := os.WriteFile(p, []byte(c.text), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := readWalkGitFile(p, holder)
			if got != c.want || ok != c.ok {
				t.Errorf("readWalkGitFile(%q) = %q, %v, want %q, %v", c.text, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestWalkGitFileSize: a file of exactly 1 MiB is read, one byte more names nothing.
func TestWalkGitFileSize(t *testing.T) {
	holder := t.TempDir()
	head := "gitdir: g"
	for _, c := range []struct {
		size int
		ok   bool
	}{{gitFileMaxBytes, true}, {gitFileMaxBytes + 1, false}} {
		p := filepath.Join(t.TempDir(), ".git")
		text := head + strings.Repeat("\n", c.size-len(head))
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := readWalkGitFile(p, holder); ok != c.ok {
			t.Errorf("size %d: ok = %v, want %v", c.size, ok, c.ok)
		}
	}
}

// TestWalkGitFileNoBlankEndsWalk: a `.git` file without the blank after the colon
// ends the walk with "no repository", even with a repository above it.
func TestWalkGitFileNoBlankEndsWalk(t *testing.T) {
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(top, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir:"+filepath.Join(top, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := findGitDir(sub); ok {
		t.Errorf("findGitDir = %q, true, want no repository", g)
	}
}
