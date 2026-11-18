package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A create on a folder whose .git holds a commondir that is no small plain file
// gets the trust refusal. As the first request it makes one git call before it, the
// excludes read. The next request makes none.
func TestWorktreeCreateCommondirRefusalCalls(t *testing.T) {
	f := newShadowFixture(t)
	repo := filepath.Join(t.TempDir(), "B")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "commondir"), 0o755); err != nil {
		t.Fatal(err)
	}
	excludesRead := []string{"config", "--includes", "--path", "core.excludesFile"}
	for i, want := range [][]string{excludesRead, nil} {
		raw, calls := f.call(t, "git.worktree_create", map[string]any{
			"baseRepo": repo, "branchName": "w1", "worktreePath": filepath.Join(repo, ".claude", "worktrees", "w1")})
		if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) || !strings.Contains(raw, "is not a small plain file") {
			t.Fatalf("create %d = %s, want the commondir refusal", i+1, raw)
		}
		if want == nil && len(calls) != 0 || want != nil && (len(calls) != 1 || !slices.Equal(calls[0].argv, want)) {
			var got []string
			for _, c := range calls {
				got = append(got, strings.Join(c.argv, " "))
			}
			t.Errorf("create %d made the calls %q, want %q", i+1, got, want)
		}
	}
}
