package main

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// git runs git -C <dir> <args...> and returns combined output + ok.
func git(dir string, args ...string) (string, bool) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
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
	branch, _ := git(p.Path, "rev-parse", "--abbrev-ref", "HEAD")
	return okResult(req.ID, gitInfoResult{IsRepo: true, Repo: filepath.Base(top), Branch: branch})
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
		if t := strings.TrimSpace(line); t != "" {
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
	source := p.SourceBranch
	if source == "" {
		source, _ = git(repo, "rev-parse", "--abbrev-ref", "HEAD")
	}
	out, ok := git(repo, "worktree", "add", "-b", p.BranchName, p.WorktreePath, source)
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
