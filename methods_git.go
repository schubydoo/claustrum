package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *server) handleGit(req *request) response {
	var fn func(*request) response
	switch req.Method {
	case "git.info":
		fn = gitInfo
	case "git.status":
		fn = gitStatus
	case "git.list_branches":
		fn = gitListBranches
	case "git.worktree_create":
		fn = gitWorktreeCreate
	case "git.worktree_remove":
		fn = gitWorktreeRemove
	default:
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	return fn(req)
}

type gitParams struct {
	Path         string `json:"path"`
	BaseRepo     string `json:"baseRepo"`
	BranchName   string `json:"branchName"`
	WorktreePath string `json:"worktreePath"`
	SourceBranch string `json:"sourceBranch"`
}

// repoDir is the repo a worktree op runs against: baseRepo, or the daemon's cwd
// (".") when absent. (worktree_* use baseRepo, NOT path — probe-verified.)
func (p *gitParams) repoDir() string {
	if p.BaseRepo != "" {
		return p.BaseRepo
	}
	return "."
}

// gitTimeout bounds every git invocation. The reference daemon runs git with no
// deadline, so a wedged git — an index/config lock, a credential prompt, a stalled
// network or filesystem, a hung checkout hook — hangs the request goroutine
// forever. We cap it; a timed-out git is reported as failure (ok=false), the same
// as any other non-zero git exit, so callers' result shapes are unchanged. Normal
// git ops finish in well under this bound, so happy-path frames stay byte-identical
// — an attack/pathological-path-only divergence from the reference. (var, not
// const, so tests can shrink it.)
var gitTimeout = 60 * time.Second

// git runs git -C <dir> <args...> under gitTimeout and returns combined output + ok.
func git(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitContext(ctx, dir, args...)
}

// gitContext is git() with an explicit context, so the timeout/cancel path is
// testable without waiting on a real wedged git.
func gitContext(ctx context.Context, dir string, args ...string) (string, bool) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

func isRepo(dir string) bool {
	out, ok := git(dir, "rev-parse", "--is-inside-work-tree")
	return ok && out == "true"
}

func gitInfo(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if !isRepo(p.Path) {
		return okResult(req.ID, notRepoResult{})
	}
	top, _ := git(p.Path, "rev-parse", "--show-toplevel")
	// Resolve the branch the way the reference does: symbolic-ref reports the
	// branch for both normal and unborn (no-commit) HEADs, where
	// `rev-parse --abbrev-ref HEAD` would fail/return "HEAD". A detached HEAD
	// (symbolic-ref fails) is reported as "detached:<short-sha>".
	branch, ok := git(p.Path, "symbolic-ref", "--short", "HEAD")
	if !ok {
		sha, _ := git(p.Path, "rev-parse", "--short", "HEAD")
		branch = "detached:" + sha
	}
	return okResult(req.ID, gitInfoResult{
		IsRepo:        true,
		Repo:          filepath.Base(top),
		Branch:        branch,
		Root:          top,
		RepoSlug:      gitRepoSlug(p.Path),
		DefaultBranch: gitDefaultBranch(p.Path),
	})
}

// gitRepoSlug returns the "owner/repo" slug parsed from remote.origin.url, or ""
// when there is no origin or the URL doesn't reduce to exactly two path segments.
// Added by the reference daemon in 7c2f88d.
func gitRepoSlug(dir string) string {
	url, ok := git(dir, "config", "--get", "remote.origin.url")
	if !ok || url == "" {
		return ""
	}
	return parseRepoSlug(url)
}

// parseRepoSlug reduces a git remote URL to its "owner/repo" slug. It accepts the
// scp-like (git@host:owner/repo.git), scheme (https:// or ssh://), userinfo, and
// trailing-slash forms, strips a single trailing ".git", and returns the slug
// ONLY when the path after the host is exactly two non-empty segments — a probe-
// verified quirk of the reference: a GitLab subgroup URL (host/group/sub/proj)
// has three segments and yields "". Owner/repo characters are preserved verbatim
// (case, '-', '_', '.' all kept).
func parseRepoSlug(remoteURL string) string {
	u := strings.TrimSpace(remoteURL)
	if i := strings.Index(u, "://"); i >= 0 { // strip scheme
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 { // strip userinfo
		u = u[i+1:]
	}
	// The host runs up to the first ':' (scp form) or '/' (URL form); the path is
	// whatever follows that separator.
	sep := strings.IndexAny(u, ":/")
	if sep < 0 {
		return ""
	}
	path := strings.TrimRight(u[sep+1:], "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// gitDefaultBranch returns the branch refs/remotes/origin/HEAD points to (e.g.
// "main"), or "" when origin/HEAD is unset. Added by the reference daemon in
// 7c2f88d.
func gitDefaultBranch(dir string) string {
	ref, ok := git(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return ""
	}
	return strings.TrimPrefix(ref, "refs/remotes/origin/")
}

func gitStatus(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if !isRepo(p.Path) {
		// The reference returns the full status shape (clean:false), not the
		// bare notRepoResult that git.info uses.
		return okResult(req.ID, gitStatusResult{})
	}
	out, _ := git(p.Path, "status", "--porcelain")
	if out == "" {
		return okResult(req.ID, gitStatusResult{IsRepo: true, Clean: true})
	}
	var changes []string
	for _, line := range strings.Split(out, "\n") {
		// Pass porcelain lines through verbatim apart from the line ending: the
		// XY status column is positional, so the leading space of an
		// unstaged-only change (" M f") is data, not padding. Trimming it made
		// staged ("M  f") and unstaged (" M f") differ only in space count.
		//
		// Kept as a single guarded append rather than an `if ... { continue }`:
		// git() already strips the trailing newline and an empty `out` returns
		// earlier, so no blank line reaches this loop in practice, and a bare
		// `continue` is then a statement coverage can never reach. The blank
		// skip itself stays — git() uses CombinedOutput, so stderr shares this
		// stream.
		if t := strings.TrimRight(line, "\r\n"); strings.TrimSpace(t) != "" {
			changes = append(changes, t)
		}
	}
	return okResult(req.ID, gitStatusResult{IsRepo: true, Clean: false, Changes: changes})
}

func gitListBranches(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if !isRepo(p.Path) {
		// The reference returns the full branches shape (branches:[]), not the
		// bare notRepoResult that git.info uses.
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	out, _ := git(p.Path, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	branches := []string{}
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			branches = append(branches, t)
		}
	}
	sort.Strings(branches)
	return okResult(req.ID, branchesResult{IsRepo: true, Branches: branches})
}

func gitWorktreeCreate(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.BranchName == "" {
		return errResult(req.ID, codeInvalidParam, "branchName is required")
	}
	repo := p.repoDir()
	// The reference checks the target is a repo BEFORE attempting the worktree
	// add, returning a clean not_a_repo error rather than leaking git's raw
	// "fatal: not a git repository …" output as a worktree_add_failed.
	if !isRepo(repo) {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "not a git repository",
			ErrorCode: "not_a_repo",
		})
	}
	// Default the source to the repo's current branch. On an unborn HEAD
	// (no-commit repo) abbrev-ref fails — leave source empty rather than capturing
	// git's error text, and let `git worktree add` infer an orphan branch (it
	// succeeds, and the reference omits sourceBranch from the result).
	source := p.SourceBranch
	if source == "" {
		if s, ok := git(repo, "rev-parse", "--abbrev-ref", "HEAD"); ok {
			source = s
		}
	}
	addArgs := []string{"worktree", "add", "-b", p.BranchName, p.WorktreePath}
	if source != "" {
		addArgs = append(addArgs, source)
	}
	out, ok := git(repo, addArgs...)
	if !ok {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "git worktree add failed: " + out,
			ErrorCode: "worktree_add_failed",
		})
	}
	return okResult(req.ID, worktreeResult{Success: true, Path: p.WorktreePath, SourceBranch: source})
}

func gitWorktreeRemove(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	git(p.repoDir(), "worktree", "remove", "--force", p.WorktreePath)
	return okResult(req.ID, successResult{Success: true})
}
