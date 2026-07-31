package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Tests for git.info's repoSlug / defaultBranch fields, added by the reference
// daemon in 7c2f88d. See docs/PROTOCOL.md and docs/UPSTREAM-TRACKING.md.

// parseRepoSlug reduces a remote URL to owner/repo only when the path after the
// host is exactly two segments — a probe-verified quirk of the reference.
func TestParseRepoSlug(t *testing.T) {
	cases := []struct{ url, want string }{
		{"git@github.com:acme/widgets.git", "acme/widgets"},
		{"https://github.com/acme/widgets.git", "acme/widgets"},
		{"https://github.com/acme/nosuffix", "acme/nosuffix"},
		{"ssh://git@github.com/acme/proj.git", "acme/proj"},
		{"https://user:pass@github.com/acme/proj.git", "acme/proj"},
		{"https://github.com/acme/proj/", "acme/proj"},
		{"git@github.com:acme/proj", "acme/proj"},
		{"https://github.com/Acme-Org/My_Repo.git", "Acme-Org/My_Repo"},
		{"https://github.com/acme/my-repo.git", "acme/my-repo"},
		{"https://github.com/acme/my.repo.git", "acme/my.repo"},
		// Three-segment paths (GitLab subgroups) yield "" — the reference requires
		// exactly two segments, it does not take the last two.
		{"https://gitlab.com/group/sub/proj.git", ""},
		{"", ""},
		{"not a url", ""},
	}
	for _, tc := range cases {
		if got := parseRepoSlug(tc.url); got != tc.want {
			t.Errorf("parseRepoSlug(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// git.info populates repoSlug from remote.origin.url and defaultBranch from
// refs/remotes/origin/HEAD; both are empty (but present) when unset.
func TestGitInfoRepoSlugAndDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("remote", "add", "origin", "git@github.com:acme/gizmo.git")
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	run("update-ref", "refs/remotes/origin/main", strings.TrimSpace(string(head)))
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": dir}))
	var got gitInfoResult
	decodeReply(t, []byte(raw), &got)
	if got.RepoSlug != "acme/gizmo" {
		t.Errorf("repoSlug = %q, want acme/gizmo", got.RepoSlug)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("defaultBranch = %q, want main", got.DefaultBranch)
	}
}
