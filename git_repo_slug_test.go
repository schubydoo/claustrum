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

// The reference's full repoSlug rule, measured directly. Every expectation below
// is the repoSlug the reference at 5db5e4a returned for that remote URL, taken
// from a differential run of all 42 shapes through both daemons. 15 of them used
// to differ: claustrum was host- and charset-agnostic and emitted a slug for
// GitLab, Bitbucket, self-hosted GHE, "www.github.com", and for owners and repo
// names GitHub itself cannot have.
func TestParseRepoSlugMatchesReference(t *testing.T) {
	cases := []struct{ url, want string }{
		// -- scheme and host gating
		{"https://github.com/acme/gizmo.git", "acme/gizmo"},
		{"http://github.com/acme/gizmo.git", "acme/gizmo"},
		{"ssh://git@github.com/acme/gizmo.git", "acme/gizmo"},
		{"git://github.com/acme/gizmo.git", "acme/gizmo"},
		{"git+ssh://git@github.com/acme/gizmo.git", ""}, // scheme matched whole
		{"file:///srv/git/acme/gizmo.git", ""},
		{"git@github.com:acme/gizmo.git", "acme/gizmo"}, // scp-like
		{"github.com:acme/gizmo.git", "acme/gizmo"},     // scp-like, no userinfo
		{"https://user:pw@github.com/acme/gizmo.git", "acme/gizmo"},
		{"https://GITHUB.COM/acme/gizmo.git", "acme/gizmo"}, // host is case-insensitive
		{"https://www.github.com/acme/gizmo.git", ""},
		{"https://github.com./acme/gizmo.git", ""}, // trailing dot is a different host
		{"https://gitlab.com/acme/gizmo.git", ""},
		{"https://ghe.corp.internal/acme/gizmo.git", ""},
		{"https://github.com:443/acme/gizmo.git", ""}, // a port is a different host
		{"ssh://git@github.com:22/acme/gizmo.git", ""},

		// -- owner charset: alphanumerics with INTERIOR hyphens only
		{"https://github.com/ac-me/gizmo.git", "ac-me/gizmo"},
		{"https://github.com/ac--me/gizmo.git", "ac--me/gizmo"},
		{"https://github.com/ACME/gizmo.git", "ACME/gizmo"},
		{"https://github.com/a/gizmo.git", "a/gizmo"},
		{"https://github.com/9acme/gizmo.git", "9acme/gizmo"},
		{"https://github.com/-acme/gizmo.git", ""},
		{"https://github.com/acme-/gizmo.git", ""},
		{"https://github.com/acme_corp/gizmo.git", ""}, // '_' is legal in a REPO, not an owner
		{"https://github.com/acme.co/gizmo.git", ""},   // '.' likewise

		// -- repo charset: looser than the owner's
		{"https://github.com/acme/gizmo", "acme/gizmo"},
		{"https://github.com/acme/giz_mo", "acme/giz_mo"},
		{"https://github.com/acme/giz.mo", "acme/giz.mo"},
		{"https://github.com/acme/gizmo-", "acme/gizmo-"}, // trailing hyphen IS allowed here
		{"https://github.com/acme/-gizmo", ""},            // leading hyphen is not
		{"https://github.com/acme/+gizmo", ""},
		{"https://github.com/acme/.", ""},
		{"https://github.com/acme/..", ""},
		{"https://github.com/acme/.git", ""}, // becomes empty after the .git strip

		// -- the .wiki rule is case-sensitive and suffix-only
		{"https://github.com/acme/gizmo.wiki", ""},
		{"https://github.com/acme/gizmo.wiki.git", ""},
		{"https://github.com/acme/GIZMO.WIKI", "acme/GIZMO.WIKI"},
		{"https://github.com/acme/wiki", "acme/wiki"},

		// -- trailing forms and path depth
		{"https://github.com/acme/gizmo/", "acme/gizmo"},
		{"https://github.com/acme/gizmo.git/", "acme/gizmo"},
		{"https://github.com/acme/sub/gizmo.git", ""}, // three segments
		{"https://github.com/acme", ""},               // one segment
	}
	for _, tc := range cases {
		if got := parseRepoSlug(tc.url); got != tc.want {
			t.Errorf("parseRepoSlug(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// The empty-segment guards are reachable through a doubled slash
// ("https://github.com//gizmo"), where Split yields an empty owner. Asserted on
// the helpers directly rather than through a URL: that URL shape was not part of
// the reference measurement above, so inventing an expected slug for it would be
// guessing. Both segments are empty-rejecting in the old and new code, so the
// slug is "" either way — only the guard itself is pinned here.
func TestSlugSegmentGuardsRejectEmpty(t *testing.T) {
	if validSlugOwner("") {
		t.Error("validSlugOwner(\"\") = true, want false")
	}
	if validSlugRepo("") {
		t.Error("validSlugRepo(\"\") = true, want false")
	}
	if got := parseRepoSlug("https://github.com//gizmo.git"); got != "" {
		t.Errorf("parseRepoSlug(doubled slash) = %q, want \"\"", got)
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
