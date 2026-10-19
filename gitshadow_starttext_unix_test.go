//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitInfoGitFileThroughAFileRunsNoTest pins a `.git` file whose target cannot
// be read for another reason than a missing path: a regular file stands where a
// folder of the path belongs, so the stat fails with ENOTDIR. git.info keeps the
// answer that it had before cell V3 there: no start test and no member
// (claustrum's own, not measured). Mutation: set goneGitFile on any stat error in
// judgeGitDir.
func TestGitInfoGitFileThroughAFileRunsNoTest(t *testing.T) {
	f := newShadowFixture(t)
	dir := resolveTestRoot(t, t.TempDir())
	g := filepath.Join(dir, "G")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "file"), "x", 0o644)
	target := filepath.Join(dir, "file", "gitdir")
	writeFile(t, filepath.Join(g, ".git"), "gitdir: "+target+"\n", 0o644)
	stubListing(t, 128, "")
	stubStderr(t, "fatal: not a git repository: "+target+"\n", false)
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+filepath.Join("file", "gitdir"))
	raw, calls := f.info(t, g, nil)
	if raw != noRepoLine {
		t.Errorf("git.info = %s\n  want %s", raw, noRepoLine)
	}
	if n := len(testCalls(calls)); n != 0 {
		t.Errorf("%d calls of the start test, want none", n)
	}
}
