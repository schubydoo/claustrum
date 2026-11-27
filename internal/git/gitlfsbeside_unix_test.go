//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCheckoutPathHoldsTheShellPathGitFolder: with the git of the login-shell
// PATH, the read-tree call of a create in the state shadow gets the folder of
// that git last on its PATH, so that a filter finds a git-lfs beside that git
// (87375cc7, measured on Linux). The other states of a create, and the daemon
// PATH of the other git calls, are claustrum's own, not measured. Mutation: set cmd.Env = env in hardenedGitCheckout (the read-tree
// call keeps the PATH of the daemon).
func TestCheckoutPathHoldsTheShellPathGitFolder(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	prefix := filepath.Join(t.TempDir(), "pfx")
	for _, d := range []string{"bin", filepath.Join("libexec", "git-core")} {
		if err := os.MkdirAll(filepath.Join(prefix, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(gitSlowStubDir(t), "git"), filepath.Join(prefix, "bin", "git")); err != nil {
		t.Fatal(err)
	}
	shellPathGitFixture(t, filepath.Join(prefix, "bin"), t.TempDir())
	daemonPath := os.Getenv("PATH")
	_, calls := f.create(t, nil)
	var readTree int
	for _, c := range calls {
		var paths []string
		for _, kv := range c.env {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				paths = append(paths, v)
			}
		}
		want := daemonPath
		if slices.Contains(c.argv, "read-tree") {
			readTree++
			want = daemonPath + string(os.PathListSeparator) + filepath.Join(prefix, "bin")
		}
		if len(paths) != 1 || paths[0] != want {
			t.Errorf("%v: PATH %q, want %q", c.argv, paths, want)
		}
	}
	if readTree == 0 {
		t.Fatalf("no read-tree call in %d calls", len(calls))
	}
}
