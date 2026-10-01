//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The walk resolves a symlink before a later ".." pops it. For
// <T>/lnk/.. with lnk -> X/sub, the pin is X/.git, the root is X, and the slug is
// X's (rows D01 and A03 on Linux and macOS VMs). Mutation: clean ".." before
// filepath.EvalSymlinks.
func TestInfoRootResolvesSymlinkBeforeDotDot(t *testing.T) {
	r := newTrustRepo(t)
	x := filepath.Join(r.base, "X")
	initTrustMain(t, x)
	runGit(t, x, "remote", "add", "origin", "https://github.com/x/x")
	if err := os.Mkdir(filepath.Join(x, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(x, "sub"), filepath.Join(r.T, "lnk"))
	path := filepath.Join(r.T, "lnk") + "/.."
	if got, want := commonDirPinEnv(path), []string{"GIT_COMMON_DIR=" + filepath.Join(x, ".git")}; !slices.Equal(got, want) {
		t.Errorf("pin = %q, want %q", got, want)
	}
	m := infoFields(t, info(t, path))
	if m["root"] != x || m["repo"] != "X" || m["repoSlug"] != "x/x" {
		t.Errorf("info = %v, want root %s, repo X and slug x/x", m, x)
	}
}
