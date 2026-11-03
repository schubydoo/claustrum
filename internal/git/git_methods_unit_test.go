package git

import (
	"context"
	"testing"
)

// repoDir returns BaseRepo when set, otherwise the daemon CWD (".").
func TestGitRepoDir(t *testing.T) {
	if got := (&gitParams{BaseRepo: "/some/repo"}).repoDir(); got != "/some/repo" {
		t.Errorf("repoDir(BaseRepo) = %q, want /some/repo", got)
	}
	if got := (&gitParams{}).repoDir(); got != "." {
		t.Errorf("repoDir(empty) = %q, want .", got)
	}
}

// gitContext must report failure (ok=false) when its context is already done —
// the guard that keeps a wedged git from hanging a request goroutine forever. An
// already-cancelled context makes exec.CommandContext refuse to start, so this is
// deterministic and never spawns a real git.
func TestGitContextTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, ok := gitContext(ctx, ".", "rev-parse", "--is-inside-work-tree"); ok {
		t.Errorf("gitContext(cancelled) = (%q, true), want ok=false", out)
	}
}
