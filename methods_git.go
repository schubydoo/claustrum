package main

import (
	"context"
	"fmt"
	"os"
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
// forever. We cap it. Normal git ops finish in well under this bound, so
// happy-path frames stay byte-identical — an attack/pathological-path-only
// divergence from the reference. (var, not const, so tests can shrink it.)
//
// ⚠️ A TIMEOUT IS NOT "the same as any other git failure". That is what this
// comment used to say, and it was true only while failure meant NOTHING HAPPENED.
// gitWorktreeRemove now treats a failed git as permission to delete the worktree
// itself, so a caller that ACTS on failure must distinguish our deadline from
// git's verdict — otherwise our own safety cap authorises a destructive act the
// reference cannot perform, since it has no deadline and simply blocks.
//
//	read-only callers (isRepo, gitInfo, gitStatus, …)  ok=false is enough
//	callers with a side effect (gitWorktreeRemove)     MUST use gitDeadline
//
// The reply shape is NOT unchanged either: the timeout branch answers
// {"success":false,"error":"git worktree remove timed out after …"}, a frame the
// reference never emits. That is confined to this pathological path; every
// reference-reachable frame is still byte-identical.
var gitTimeout = 60 * time.Second

// git runs git -C <dir> <args...> under gitTimeout and returns combined output + ok.
//
// Combined, deliberately — do NOT "fix" this to Output() to match gitStatusErr
// below. git.worktree_create puts this string ON THE WIRE on failure
// ("git worktree add failed: " + out), and `git worktree add` writes both its
// progress and its fatal to stderr while leaving stdout empty. Measured against
// 5db5e4a with a branch name that already exists:
//
//	reference : "git worktree add failed: Preparing worktree (new branch 'dup')\nfatal: a branch named 'dup' already exists"
//	stdout-only: "git worktree add failed: "
//
// The stderr-in-porcelain bug that gitStatusErr fixes does not apply here: no
// caller of git() parses this string as a line-oriented list, and the two
// callers that compare it (isRepo, isRepoGitDir) test the exit status or an
// exact "true", both of which a warning-prefixed string already fails safely.
func git(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitContext(ctx, dir, args...)
}

// gitContext is git() with an explicit context, so the timeout/cancel path is
// testable without waiting on a real wedged git. Combined output — see git().
func gitContext(ctx context.Context, dir string, args ...string) (string, bool) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// gitStatusErr runs git and returns the exec error itself, not just an ok flag.
// The reference reports a failed `git status --porcelain` as the bare Go error
// string ("exit status 128"), NOT git's stderr — measured against 5db5e4a.
//
// Output, NOT CombinedOutput: git writes warnings to stderr while still
// succeeding on stdout, and folding the two streams together turned those
// warnings into porcelain entries. Measured against 5db5e4a with a repo whose
// core.excludesFile is unreadable — the reference answers {"isRepo":true,
// "clean":true} while claustrum reported the warning text as a change. The error
// path is unaffected: the caller reports err.Error(), which for an ExitError is
// "exit status N" and never includes stderr.
func gitStatusErr(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

// gitDeadline is git() plus one extra bit: whether OUR deadline killed the
// process, as opposed to git exiting non-zero on its own.
//
// It exists for exactly one caller. gitWorktreeRemove treats a failed git as
// permission to delete worktreePath itself, and gitTimeout is a CLAUSTRUM-ONLY
// divergence — the reference runs git with no deadline and simply blocks. So
// without this distinction a wedged git turns a claustrum safety measure into a
// recursive delete the reference would never perform. Measured before the fix,
// with a stub git that sleeps and gitTimeout shrunk: the directory was deleted
// and the reply was {"success":true}.
func gitDeadline(dir string, args ...string) (out string, ok bool, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, ok = gitContext(ctx, dir, args...)
	return out, ok, ctx.Err() != nil
}

// isRepoGitDir is the repo test used by git.status and git.list_branches. The
// reference gates those two on `rev-parse --git-dir`, which succeeds for a BARE
// repo and from inside a `.git` directory, where `--is-inside-work-tree` (still
// used by git.info) reports false.
//
// Measured against the reference at 5db5e4a — only these two methods diverged,
// so this deliberately does not replace isRepo everywhere:
//
//	                    bare repo                inside .git
//	git.info            isRepo:false (agrees)    isRepo:false (agrees)
//	git.status          -32603 exit status 128   -32603 exit status 128
//	git.list_branches   isRepo:true, []          isRepo:true, [master …]
func isRepoGitDir(dir string) bool {
	_, ok := git(dir, "rev-parse", "--git-dir")
	return ok
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
	// `remote get-url`, NOT `config --get remote.origin.url`: the former applies
	// url.<base>.insteadOf rewrites, the latter returns the raw stored value.
	// Measured at 5db5e4a with origin "gl-base/acme/gizmo.git" and
	// url."https://github.com/".insteadOf "gl-base/" — the reference answers
	// "acme/gizmo", which is only reachable from the rewritten URL.
	//
	// This became load-bearing with the host gate below: the raw value has no
	// github.com host, so reading it would drop the slug for every developer who
	// uses an insteadOf rewrite. Without the gate the old call happened to give
	// the right answer, which is why this looked like a no-op before.
	url, ok := git(dir, "remote", "get-url", "origin")
	if !ok || url == "" {
		return ""
	}
	return parseRepoSlug(url)
}

// slugHost is the only remote host the reference emits a repoSlug for. Every
// other host — GitLab, Bitbucket, a self-hosted GHE, even "www.github.com" or a
// trailing-dot "github.com." — yields "".
const slugHost = "github.com"

// parseRepoSlug reduces a git remote URL to its "owner/repo" slug, reproducing
// the reference's rule exactly. Derived by driving 42 remote-URL shapes through
// both daemons at 5db5e4a and diffing the git.info frames:
//
//   - Scheme must be https, http, ssh, or git, or absent (the scp-like
//     [user@]host:owner/repo form). "git+ssh://" is REJECTED — the scheme is
//     matched whole, not by suffix — and so is "file://".
//   - Host must equal "github.com", case-insensitively ("GITHUB.COM" is fine).
//     A port makes it a different host, so "github.com:443/..." yields "" — and
//     in the scp-like form the port lands in the path and gives three segments,
//     which fails for the same reason. Userinfo ("git@", "user:pw@") is stripped.
//   - The path, after one optional trailing "/" and one optional trailing
//     ".git", must be exactly two non-empty segments. Three ("acme/sub/gizmo")
//     or one ("acme") yield "".
//   - Owner: alphanumerics with interior hyphens only. "ac-me" and "ac--me" pass;
//     "-acme", "acme-", "acme_corp" and "acme.co" do not. Note the owner charset
//     is STRICTER than the repo charset — '_' and '.' are legal in a repo name
//     and illegal in an owner.
//   - Repo: alphanumerics plus '.', '_' and '-', not starting with '-', not "."
//     or "..", and not ending in a lowercase ".wiki". The wiki check is
//     case-sensitive and suffix-only, so "GIZMO.WIKI" and a repo simply named
//     "wiki" are both accepted.
func parseRepoSlug(remoteURL string) string {
	u := strings.TrimSpace(remoteURL)
	if i := strings.Index(u, "://"); i >= 0 {
		switch strings.ToLower(u[:i]) {
		case "https", "http", "ssh", "git":
		default:
			return ""
		}
		u = u[i+3:]
	}
	// Strip userinfo, but only when the '@' precedes the path — otherwise an '@'
	// inside a repo name would be mistaken for a userinfo delimiter.
	hostEnd := len(u)
	if i := strings.Index(u, "/"); i >= 0 {
		hostEnd = i
	}
	if a := strings.Index(u[:hostEnd], "@"); a >= 0 {
		u = u[a+1:]
	}
	// The host runs up to the first ':' (scp form) or '/' (URL form); the path is
	// whatever follows that separator.
	sep := strings.IndexAny(u, ":/")
	if sep < 0 {
		return ""
	}
	if !strings.EqualFold(u[:sep], slugHost) {
		return ""
	}
	path := strings.TrimRight(u[sep+1:], "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !validSlugOwner(parts[0]) || !validSlugRepo(parts[1]) {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// validSlugOwner reports whether s is alphanumerics with interior hyphens only.
func validSlugOwner(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(s)-1 { // no leading or trailing hyphen
				return false
			}
		default:
			return false
		}
	}
	return true
}

// validSlugRepo reports whether s is a repo name the reference accepts.
func validSlugRepo(s string) bool {
	if s == "" || s == "." || s == ".." || s[0] == '-' {
		return false
	}
	if strings.HasSuffix(s, ".wiki") { // case-sensitive: "GIZMO.WIKI" is fine
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
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
	if !isRepoGitDir(p.Path) {
		// The reference returns the full status shape (clean:false), not the
		// bare notRepoResult that git.info uses.
		return okResult(req.ID, gitStatusResult{})
	}
	// A bare repo and a `.git` directory pass the gate above but have no work
	// tree, so `git status` exits 128. The reference propagates that as -32603
	// carrying the Go error string, not git's "fatal: …" output.
	out, err := gitStatusErr(p.Path, "status", "--porcelain")
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if out == "" {
		return okResult(req.ID, gitStatusResult{IsRepo: true, Clean: true})
	}
	var changes []string
	// The reference trims the WHOLE porcelain blob once and only then splits it,
	// so exactly ONE line can lose its leading space: the first. Probe-measured
	// at 5db5e4a — a repo whose first entry is " M a1.txt" and whose second is
	// " M a2.txt" comes back as ["M a1.txt"," M a2.txt"]. Trimming every line
	// (what claustrum did before) and trimming none (what it did after) are both
	// wrong; the blob-level trim is what reproduces it.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// Past the first line, pass porcelain through verbatim apart from the
		// line ending: the XY status column is positional, so the leading space
		// of an unstaged-only change (" M f") is data, not padding. Trimming it
		// made staged ("M  f") and unstaged (" M f") differ only in space count.
		//
		// Kept as a single guarded append rather than an `if ... { continue }`:
		// gitStatusErr already strips the trailing newline and an empty `out`
		// returns earlier, so no blank line reaches this loop in practice, and a
		// bare `continue` is then a statement coverage can never reach. The blank
		// skip itself stays as cheap insurance against a porcelain blob that ends
		// in a stray separator.
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
	if !isRepoGitDir(p.Path) {
		// The reference returns the full branches shape (branches:[]), not the
		// bare notRepoResult that git.info uses. Gated on --git-dir, so a bare
		// repo and a `.git` directory both list their branches (see
		// isRepoGitDir).
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
	// The reference accepts a sourceBranch ONLY when it names an existing local
	// branch, and silently ignores anything else — succeeding off HEAD rather
	// than failing the request. claustrum forwarded the value straight to
	// `git worktree add`, which failed with "invalid reference".
	//
	// Measured against the reference at 5db5e4a; the accepted set is narrower
	// than "a resolvable rev":
	//
	//	feature, diverged           local branches  -> used as-is
	//	HEAD                                        -> ignored, falls back
	//	refs/heads/feature          full ref name   -> ignored, falls back
	//	v1                          tag             -> ignored, falls back
	//	cb77b0c                     commit sha      -> ignored, falls back
	//	origin/rbranch              remote-tracking -> ignored, falls back
	//	no-such-branch                              -> ignored, falls back
	//
	// So the test is the existence of refs/heads/<source>, not `rev-parse
	// --verify`, which would accept HEAD, the tag and the sha. Note "diverged"
	// is accepted although it is NOT an ancestor of HEAD, which rules out an
	// ancestry check here.
	source := p.SourceBranch
	if source != "" {
		if _, ok := git(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+source); !ok {
			source = "" // fall through to HEAD below
		}
	}
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
	// `git worktree add` checks out tracked files only, so the reference seeds
	// the new worktree with .claude/ and the .worktreeinclude matches — without
	// this the worktree comes up bare, with no agent configuration. Best-effort:
	// the worktree exists and the reference reports success regardless.
	populateWorktree(repo, p.WorktreePath)
	return okResult(req.ID, worktreeResult{Success: true, Path: p.WorktreePath, SourceBranch: source})
}

func gitWorktreeRemove(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	repo := p.repoDir()
	// When `git worktree remove --force` fails for ANY reason, the reference
	// removes worktreePath itself and still answers {"success":true}; it reports
	// failure only when that manual cleanup ALSO fails.
	//
	// ⚠️ "any reason" is literal, and it is measured — not inferred from the one
	// fixture that motivated this. Reviewed on PR #204, which pointed out that the
	// original measurement covered only a locked worktree while the code deletes on
	// every non-zero exit. Re-probed at 5db5e4a, checking the DIRECTORY afterwards
	// rather than the reply, and claustrum matches on all three:
	//
	//	fixture      git fails because            reference: dir after
	//	locked       the worktree is locked       DELETED
	//	plain-dir    the path was never a worktree  DELETED
	//	bogus-repo   baseRepo is not a repo at all  DELETED
	//
	// So `git.worktree_remove` is a recursive delete of the caller-supplied
	// worktreePath whenever git is unhappy, and that is parity, not a claustrum
	// invention. Documented in PROTOCOL.md because it will otherwise read as a bug.
	// The caller did name the path and ask for it to be removed, which is why this
	// is not treated like the -cli-version escape in #196 — there the deletion
	// reached a path the caller never named.
	//
	// The registration is NOT cleaned up either: after a locked removal the repo
	// still lists the worktree (measured: 2 entries, on both binaries).
	//
	// git() not a bespoke helper — it is already CombinedOutput, and the only
	// differences were error-vs-ok and a trailing-newline trim that the
	// strings.TrimSpace below absorbs.
	out, ok, timedOut := gitDeadline(repo, "worktree", "remove", "--force", p.WorktreePath)
	// NESTED under !ok deliberately: timedOut alone is not enough. If git exits 0
	// at the instant the deadline fires, exec's Wait returns a nil error (ok=true)
	// while ctx.Err() is already DeadlineExceeded — so a removal that SUCCEEDED
	// would be reported as a wedged one, and the branchName delete below skipped.
	// The window is nanoseconds and not reproducible in a test, so the guarantee
	// is structural: a successful git cannot reach the timeout report at all.
	// Raised on review.
	if !ok {
		if timedOut {
			// OUR deadline, not git's verdict. Deleting here would be a destructive
			// act on a path the reference never reaches, so report instead — a bare
			// {"success":true} would be a lie about a wedged removal.
			//
			// Worded as what the daemon KNOWS: its deadline fired and it ran no
			// cleanup of its own. It does NOT know the directory state, because the
			// git it SIGKILLed unlinks as it goes and the slow-filesystem case this
			// timeout exists for is exactly when it will have got part-way.
			return okResult(req.ID, worktreeRemoveResult{
				Success: false,
				Error: fmt.Sprintf(
					"git worktree remove timed out after %s; no cleanup was attempted, "+
						"and git may have partially removed the worktree", gitTimeout),
			})
		}
		if rmErr := os.RemoveAll(p.WorktreePath); rmErr != nil {
			return okResult(req.ID, worktreeRemoveResult{
				Success: false,
				Error: fmt.Sprintf("failed to remove worktree: %s; manual cleanup also failed: %v",
					strings.TrimSpace(out), rmErr),
			})
		}
	}
	// The reference also deletes the branch when branchName is given, and does
	// so FORCEFULLY — an unmerged branch goes too (probe-measured at 5db5e4a:
	// both a merged and an unmerged branch are gone afterwards, where claustrum
	// left both behind). Best-effort: naming a branch that does not exist still
	// answers {"success":true}, so a failed delete is not surfaced.
	if p.BranchName != "" {
		git(repo, "branch", "-D", p.BranchName)
	}
	return okResult(req.ID, worktreeRemoveResult{Success: true})
}
