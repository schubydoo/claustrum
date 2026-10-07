package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// ExistingBranch attaches the new worktree to an already-existing local branch
	// instead of creating one, added by the reference in 19f30c46 and advertised as
	// the git.worktree_create.existingBranch feature. When it names a branch that
	// show-ref --verify resolves, the add uses that branch as the commit-ish (no -b);
	// when it is absent or names no branch, the -b <branchName> new-branch path runs.
	// Caller-activated (like timeoutMs), not an operator divergence. Bound
	// namespace-wide like every gitParams field (D9); only git.worktree_create reads it.
	ExistingBranch string `json:"existingBranch,omitempty"`
	WorktreeRoot   string `json:"worktreeRoot,omitempty"`
	// TimeoutMs is a caller-supplied per-request deadline (milliseconds) on the
	// git.worktree_create add + checkout, added by the reference in 4534d86 and
	// advertised as the git.worktree_create.timeoutMs feature. Absent or 0 arms no
	// deadline, so the default path is byte-identical. This is caller-activated
	// (like CT-1's wantPid), not an operator divergence. Bound namespace-wide like
	// every gitParams field (D9); only git.worktree_create reads it.
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// repoDir is the repo a worktree op runs against: baseRepo, or the daemon's cwd
// (".") when absent. (worktree_* use baseRepo, NOT path — probe-verified.)
func (p *gitParams) repoDir() string {
	if p.BaseRepo != "" {
		return p.BaseRepo
	}
	return "."
}

// gitTimeout optionally bounds every git invocation. **It is OFF by default (0),
// which is the parity position**: the reference daemon showed no deadline at or
// below the 75 s probed (outside the branch step of 89cb6289, which has its own
// bounds, worktreebranch.go), so a wedged git — an index/config lock, a credential
// prompt, a stalled network or filesystem, a hung checkout hook — leaves the
// request goroutine waiting without bound there, and now here too.
//
// ⚠️ Everything below describes what an operator opts INTO, measured against the
// retracted 60 s default. It shipped always-on and that failed rule 3: a
// wall-clock deadline cannot separate a hostile git from an honestly slow one, so
// a large repo on a loaded host or a cold network filesystem trips it too. The
// fallback here IS observable. On git.status and
// git.list_branches the killed process surfaces as -32603 carrying
// "signal: killed" (docs/PROTOCOL.md -> git.list_branches; docs/DIVERGENCES.md D5).
// Normal git ops finish well under any sane bound, but "well under" is a statement
// about typical hosts, not a property of the predicate, and an honest 61 s git has
// never been measured on either binary. This is D5.
//
// ⚠️ A TIMEOUT IS NOT "the same as any other git failure" for a caller that acts on
// the failure. git.worktree_remove runs no `git worktree remove`. On that method no
// D5 kill leads to a delete. A kill only refuses or skips a step.
// git.worktree_create is the exception: its rollback deletes after a failed or killed
// read-tree checkout, and it removes an empty leaf after a failed add. Its checkout
// tests run without the answer of a killed show-toplevel or `worktree list`. The
// root-chain step still refuses a root that has, or lies below, a .git entry. A caller
// that deletes on a git
// failure must tell our deadline from git's verdict first.
//
// ⚠️ This used to add "no OTHER frame moves because of the deadline". That is
// false: the deadline is the shared gitTimeout, applied independently at the
// helpers (git, gitStdoutErr, the hardened helpers), so a kill can surface through
// ANY call site. gitStdoutErr turns it into -32603 "signal: killed" on
// git.status and git.list_branches, and the repo-detection calls can answer
// isRepo:false instead. More than one arm moves; the full set has not been
// enumerated against the code, so do not restate a count here.
//
// CORRECTION, 2026-08-02: that last clause used to read "every reference-reachable
// frame is still byte-identical", which is a claim about the whole wire and was
// false when written — git.list_branches was folding git's stderr into branches[]
// at the time. Scope a claim to the thing it was measured on.
//
// ⚠️ THE BOUND IS SOFTER THAN IT LOOKS ON THE D5 GIT SITES. CombinedOutput waits
// for the output pipe to close, not merely for git to exit, so a git that spawns a
// child which OUTLIVES it keeps the call blocked past the deadline — the orphan
// still holds the pipe. Measured: a stub `sleep 30` under `sh` took the full 30s
// against a 300ms gitTimeout, while the same stub as `exec sleep 30` returned
// promptly. The general git exec sites (git.status, git.list_branches, the repo
// probes) do NOT cap that drain: closing it means a process-group teardown that is
// unmeasured on those paths, so it is recorded rather than fixed.
//
// The ONE measured exception is git.worktree_create's read-tree checkout, which
// CAN leave a smudge/hook descendant holding the pipe. Only that path caps the
// drain — see hardenedGitCheckout / worktreeCreateDrainCap in githarden.go —
// reproducing 4534d86's fixed ~5s post-exit cap + descendant reap and the
// timeoutMs-gated success-vs-timeout verdict. That is wire-visible (a timeout +
// rollback where an unbounded drain would report success), so it is scoped to the
// one path where it was measured, not applied blanket to every git call.
//
// D5 FLIP: the default is now 0 = NO DEADLINE, matching the reference. A non-zero
// value is opt-in via -git-timeout or the git-timeout key in claustrum.conf (the
// config key is the reachable one — Claude Desktop owns the argv). At 0 the
// deadline is not merely large: gitCtx below bypasses context.WithTimeout
// entirely, so no cancel path is armed and exec.CommandContext cannot kill git.
// Do NOT "simplify" that into a huge duration — the bypass is what makes "off"
// mean off, exactly as D3 and D10 bypass their io.LimitReaders.
// (var, not const, so tests can shrink it and -serve can set it.)
var gitTimeout time.Duration

// gitCtx returns the context every git invocation runs under: bounded when
// gitTimeout is positive, unbounded when it is 0 (the default).
//
// The zero case returns context.Background() rather than a WithTimeout carrying a
// huge value, so exec.CommandContext has nothing to fire and ctx.Err() is nil by
// construction — a wedged git then blocks, as the reference
// did at every duration probed (no deadline at or below 75 s, measured on
// git.worktree_remove only, outside the branch step; above that, unmeasured on both
// binaries).
func gitCtx() (context.Context, context.CancelFunc) {
	if gitTimeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), gitTimeout)
}

// git runs git -C <dir> <args...> under gitTimeout and returns combined output + ok.
//
// Combined, deliberately — do NOT "fix" this to Output() to match gitStdoutErr
// below. It once carried the failure text of git.worktree_create, whose `git
// worktree add` writes both its progress and its fatal to stderr while leaving
// stdout empty. git.worktree_create now quotes stderr only, through
// hardenedGitStderr and worktreeGitText (measured against f6010b97 and 90fca6e6:
// stdout never shows in those frames).
//
// This helper's remaining callers fall into two groups, and only the first
// is safe by argument:
//
//	compare or discard   isRepo, isRepoGitDir — exit status or an exact "true",
//	                     both of which a warning-prefixed string fails safely
//	                     (show-ref --verify now runs through hardenedGit, and the
//	                     branch step through its own calls in worktreebranch.go)
//	echoes it verbatim   --show-toplevel → root/repo on Windows only, and only when
//	                     it names the walk root (gitInfoRoot). On Linux and macOS
//	                     git.info does not run it. branch --show-current →
//	                     branch (rev-parse --short HEAD supplies only the
//	                     detached-HEAD sha fallback), remote get-url → repoSlug,
//	                     symbolic-ref → defaultBranch, rev-parse --abbrev-ref
//	                     HEAD → sourceBranch
//
// The second group puts combined output on the wire unsplit. No fixture has been
// found that makes those commands write to stderr while exiting 0, so the
// exposure is theoretical and this change does not touch it — but it is NOT
// covered by the argument above, and saying otherwise would repeat the mistake
// this block already records.
//
// CORRECTION, 2026-08-02: this used to say "no caller of git() parses this string
// as a line-oriented list". Two did — gitListBranches and copyWorktreeIncludes —
// and the first put the result on the wire. They now use gitStdoutErr. Keep the
// split: choosing between the helpers is a per-caller decision about whether
// stderr is data or noise, not an inconsistency to tidy away.
func git(dir string, args ...string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	return gitContext(ctx, dir, args...)
}

// gitContext is git() with an explicit context, so the timeout/cancel path is
// testable without waiting on a real wedged git. Combined output — see git().
func gitContext(ctx context.Context, dir string, args ...string) (string, bool) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// The GIT_COMMON_DIR pin of the git-directory trust check, when dir's git directory
	// is trusted (see gitdirtrust.go).
	if pin := commonDirPinEnv(dir); pin != nil {
		cmd.Env = append(os.Environ(), pin...)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// gitStdoutErr runs git and returns the exec error itself, not just an ok flag.
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
func gitStdoutErr(dir string, args ...string) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

// worktreeCreateCtx builds the two contexts of git.worktree_create.
//
// d5 is the D5 global git deadline from gitCtx. It has no deadline when D5 is off.
// The add runs under d5 only, so the caller's timeoutMs never kills the add.
//
// caller is d5 plus the caller's timeoutMs (4534d86 parity). The read-tree checkout
// runs under caller, so the caller's deadline kills the checkout. The daemon also
// tests caller after the add and after the copy step. When timeoutMs is 0 or less,
// caller is d5 itself, and with D5 also off nothing is bounded.
//
// The add and the checkout share the one D5 deadline. With D5 opted in and
// timeoutMs 0, the checkout thus runs under the D5 time that the add left. That is a
// change only to the claustrum-only, default-off D5 path. A D5 kill of the checkout
// is a failed checkout: the request answers worktree_add_failed "(checkout)" and
// rolls back.
//
// errCallerTimeoutMs is the cause stamped on the caller's deadline. It tells a fired
// caller deadline apart from a fired D5 deadline. When D5 fires first,
// context.Cause reports the D5 DeadlineExceeded instead. See callerTimeoutFired.
var errCallerTimeoutMs = errors.New("git.worktree_create caller timeoutMs deadline")

func worktreeCreateCtx(timeoutMs int) (d5, caller context.Context, cancel context.CancelFunc) {
	base, baseCancel := gitCtx()
	if timeoutMs <= 0 {
		return base, base, baseCancel
	}
	ctx, c := context.WithTimeoutCause(base, time.Duration(timeoutMs)*time.Millisecond, errCallerTimeoutMs)
	return base, ctx, func() { c(); baseCancel() }
}

// callerTimeoutFired reports whether ctx was canceled by the caller's own timeoutMs
// deadline, as opposed to the D5 global deadline (or any other parent cancellation).
// Only the caller's deadline earns the 4534d86 errorCode "timeout"; a D5 kill falls
// through to worktree_add_failed, the way a D5-only kill (no timeoutMs) already does.
func callerTimeoutFired(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errCallerTimeoutMs)
}

// isRepo runs right after hostileConfigRefusal on dir, so it runs no precursor
// (hardenedGitFirst) and pins the hooks of that check's listing l.
func isRepo(dir string, l configListing) bool {
	out, ok := hardenedGitFirst(dir, false, l, "rev-parse", "--is-inside-work-tree")
	return ok && out == "true"
}

func gitInfo(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// The excludesFile the reference reads at git.info is now resolved once (cached)
	// and applied to every git op via hardenedProfileArgs/userExcludesFile, rather than
	// probed here and discarded. The read comes first, so it also runs before a trust
	// refusal, as on f6010b97 and 89cb6289 (every trust refusal row).
	userExcludesFile()
	// The git-directory trust check runs on path (gitdirtrust.go). The check of the
	// daemon's own GIT_CONFIG_COUNT comes after a trust refusal and before the "no
	// repository" answer (docs/PROTOCOL.md).
	t := requestGitDirTrust(p.Path, true)
	if t.verdict == gitDirRefused {
		return errResult(req.ID, codeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	if t.verdict == gitDirNoRepo {
		// A listing that fails here refuses (rows A-N1 and A-X2 on Linux and macOS VMs,
		// cells P3-infoN and P3-infoX on a Windows VM).
		if msg := noRepoListingRefusal(t, p.Path, false); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return okResult(req.ID, notRepoResult{})
	}
	// If the repo's config cannot be enumerated (e.g. a corrupt .git/config), the
	// reference cannot pin its config-defined hooks off and refuses with -32603
	// rather than running git. Measured against 7d193f89 on an ephemeral VM.
	c := hostileConfigRefusal(p.Path, false)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	}
	if c.noRepo {
		return okResult(req.ID, notRepoResult{})
	}
	if _, ok := hardenedGitFirst(p.Path, false, c.listing, "rev-parse", "--git-dir"); !ok {
		return okResult(req.ID, notRepoResult{})
	}
	// The root is the folder where the walk of the trust check found the repository
	// (gitWalkRoot). 89cb6289 runs no `rev-parse --show-toplevel` here on Linux and
	// macOS VMs. A walk that found a git directory with no work tree (a bare repository,
	// a path inside .git), or nothing, answers the bare shape (rows I04a, I04b, I05a,
	// I05b and T11a). On Windows git is asked to confirm the root (gitInfoRoot).
	walkRoot, walkGitDir := gitWalkRoot(p.Path)
	if walkRoot == "" {
		return okResult(req.ID, notRepoResult{})
	}
	top := gitInfoRoot(walkRoot, walkGitDir, commonDirPinEnv(p.Path))
	// slug and defaultBranch come before the branch, matching the reference order.
	slug := gitRepoSlug(p.Path)
	defBranch := gitDefaultBranch(p.Path)
	// The branch is `branch --show-current` (works on a normal and an unborn HEAD),
	// empty on a detached HEAD → reported as "detached:<short-sha>".
	//
	// When `branch --show-current` fails, the result has no `branch` member at all,
	// never git's error text. Measured side by side against 90fca6e6 and f6010b97 on a
	// Linux VM: a HEAD git fails to resolve ("ref: refs/heads/main" or 40 hex digits,
	// each followed by junk, H15 H18 NH21 NH22) answers without `branch` on both.
	branch, ok := hardenedGit(p.Path, false, "branch", "--show-current")
	switch {
	case !ok:
		branch = ""
	case branch == "":
		sha, _ := hardenedGit(p.Path, false, "rev-parse", "--short", "HEAD")
		branch = "detached:" + sha
	}
	return okResult(req.ID, gitInfoResult{
		IsRepo:        true,
		Repo:          infoRepoName(top),
		Branch:        branch,
		Root:          top,
		RepoSlug:      slug,
		DefaultBranch: defBranch,
	})
}

// infoRepoName is the `repo` member of git.info: the base name of the root. A name that
// starts with "-" or "+" leaves the member out. f6010b97 and 89cb6289 do so on Linux
// and macOS VMs (rows I11a and I11b). Every other measured name kept it. The general
// rule behind those two rows is not measured.
func infoRepoName(root string) string {
	name := filepath.Base(root)
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "+") {
		return ""
	}
	return name
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
	url, ok := hardenedGit(dir, false, "remote", "get-url", "origin")
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
//
// The name is verified. A name that starts with "-" gives "" with no further call.
// Else the light `rev-parse --verify --quiet refs/remotes/origin/<name>^{commit}` runs
// in dir, with its listing. Exit 0 keeps the name, and any failure gives "".
// f6010b97 and 89cb6289 do so on Linux and macOS VMs (rows I10a to I10c: a dangling
// origin/HEAD, one that names a blob, and origin/-x). Not measured: other invalid
// names, and a symbolic-ref answer that does not start with refs/remotes/origin/.
// claustrum verifies that answer the same way.
func gitDefaultBranch(dir string) string {
	ref, ok := hardenedGit(dir, false, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return ""
	}
	name := strings.TrimPrefix(ref, "refs/remotes/origin/")
	if strings.HasPrefix(name, "-") {
		return ""
	}
	if _, ok := hardenedGit(dir, false, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name+"^{commit}"); !ok {
		return ""
	}
	return name
}

func gitStatus(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// 7d193f89 rebuilt git.status around session worktrees. baseRepo is now
	// required, and status is reported ONLY when path is a linked worktree that
	// belongs to baseRepo. A plain path, a plain subdir, a nested repo, the repo
	// root itself, a worktree of another repo, or the right worktree named against
	// the wrong baseRepo all answer the bare isRepo:false shape — confirmed
	// byte-for-byte against the reference. The check comes before status runs.
	if p.BaseRepo == "" {
		return errResult(req.ID, codeInvalidParam, "baseRepo is required")
	}
	// The gate of 89cb6289 (gitstatus.go, docs/PROTOCOL.md). Every failure of it
	// answers the full status shape with isRepo:false, not the bare notRepoResult of
	// git.info.
	notRepo := okResult(req.ID, gitStatusResult{})
	// A baseRepo that does not resolve, or that lies in a managed worktrees tree,
	// answers before any git call and before the check of the daemon's own
	// GIT_CONFIG_COUNT (rows n10, n11 and o23).
	if unresolvable(p.BaseRepo) || statusBaseInManagedTree(p.BaseRepo) {
		return notRepo
	}
	// The read of the user's excludes comes next, so it also runs before a trust
	// refusal (rows o20 and o21, Linux VM).
	userExcludesFile()
	// The git-directory trust check runs on baseRepo, not on path (gitdirtrust.go).
	// The count check comes after a trust refusal and before the "no repository"
	// answer.
	t := requestGitDirTrust(p.BaseRepo, false)
	if t.verdict == gitDirRefused {
		return errResult(req.ID, codeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	if t.verdict == gitDirNoRepo {
		if msg := statusNoRepoCalls(p.BaseRepo, t.noGit); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return notRepo
	}
	// A baseRepo that resolves and cannot be opened answers here, with no further git
	// call. On a Windows VM that is a dangling junction (rows n11-j and x11).
	if _, err := os.Stat(p.BaseRepo); err != nil {
		return notRepo
	}
	// A repo whose config cannot be enumerated is refused with -32603 (row o22b). The
	// listing runs in baseRepo as sent and carries the heavy profile (K1 call 2).
	c := hostileConfigRefusal(p.BaseRepo, true)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	} else if c.noRepo {
		return notRepo
	}
	// K1 call 3. Its answer is the common directory, in the spelling that git gives
	// it. A failure answers isRepo:false (row n17b).
	out, err := hardenedGitRunPre(p.BaseRepo, true, &c.listing, nil, "rev-parse", "--absolute-git-dir")
	if err != nil || out == "" {
		return notRepo
	}
	common := filepath.Clean(strings.TrimRight(out, "\r"))
	// No `worktrees` folder in the common directory: no further git call (rows n18
	// and n18c).
	if fi, err := os.Stat(filepath.Join(common, "worktrees")); err != nil || !fi.IsDir() {
		return notRepo
	}
	sp, ok := statusPathOf(p.Path, p.BaseRepo)
	if !ok {
		return notRepo
	}
	if ok, err := statusWorkTreeProbe(common, sp.probeTree, statusCommonPin(common)); err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	} else if !ok {
		return notRepo
	}
	entry, ok := statusEntryOf(common, sp)
	if !ok {
		return notRepo
	}
	defer func() { _ = entry.root.Close() }()
	// The exec error of a failed command goes on the wire as it is, for example
	// "exit status 128". The text of git does not show for status, ls-files and
	// diff-index. A relative path passes the gate and
	// then fails here, because git gets it as sent (row n25c).
	changes, err := statusChanges(p.Path, common, entry)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	return okResult(req.ID, gitStatusResult{IsRepo: true, Clean: len(changes) == 0, Changes: changes})
}

// unresolvable reports whether filepath.EvalSymlinks fails on p, for any reason.
// git.status treats such a baseRepo as missing, and git.list_branches such a path
// (docs/PROTOCOL.md).
func unresolvable(p string) bool {
	_, err := filepath.EvalSymlinks(p)
	return err != nil
}

// canonicalPath resolves p to the spelling git reports — symlinks resolved and
// (on Windows) 8.3 short names expanded. Defined per-OS in pathcanon_{unix,windows}.go.

// samePath compares two paths after lexical cleaning. Callers that compare against
// git's output canonicalize their operands first.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func gitListBranches(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// A path inside a managed worktrees tree, and a non-empty path that does not
	// resolve, answer the bare isRepo:false shape (branches:[]) before any git call
	// and before the check of the daemon's own GIT_CONFIG_COUNT (docs/PROTOCOL.md).
	// The managed-worktrees test also runs on baseRepo.
	if baseRepoUnderManagedWorktrees(p.Path) || baseRepoUnderManagedWorktrees(p.repoDir()) ||
		(p.Path != "" && unresolvable(p.Path)) {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// The git-directory trust check runs on path (gitdirtrust.go). The check of the
	// daemon's own GIT_CONFIG_COUNT comes after a trust refusal and before the "no
	// repository" answer (docs/PROTOCOL.md).
	t := requestGitDirTrust(p.Path, true)
	if t.verdict == gitDirRefused {
		return errResult(req.ID, codeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	if t.verdict == gitDirNoRepo {
		// A listing that fails here refuses (row A-N2 on Linux and macOS VMs, cell
		// P3-lbN on a Windows VM).
		if msg := noRepoListingRefusal(t, p.Path, false); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// A repo whose config cannot be enumerated is refused with -32603 (7d193f89),
	// measured on an ephemeral VM.
	c := hostileConfigRefusal(p.Path, false)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	}
	if c.noRepo {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// 7d193f89 runs this under the light hardening profile with the config
	// precursor; the repo gate is `rev-parse --git-dir` (true for a bare repo and
	// from inside a `.git` directory, unlike git.info's --is-inside-work-tree). The
	// reference returns the full branches shape (branches:[]) on a non-repo, not
	// git.info's bare notRepoResult. The refusal check above was its precursor.
	if _, ok := hardenedGitFirst(p.Path, false, c.listing, "rev-parse", "--git-dir"); !ok {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// stdout only, AND propagate a failure — the same two rules git.status
	// follows, for the same reason. Two distinct frames were wrong here:
	//
	//	broken ref, for-each-ref exits 0   ref ["main","real"]
	//	                                   was ["main","real","warning: ignoring broken ref …"]
	//	corrupt packed-refs, exits 128     ref -32603 "exit status 128"
	//	                                   was ["fatal: unexpected line in .git/packed-refs: …"]
	//
	// Both measured 2026-08-02 against 5db5e4a, with a clean repo as the control.
	// Reading stdout alone fixes only the first: it turns the second into an empty
	// branches[], which is a different wrong answer. Discarding the error was the
	// other half of the bug.
	//
	// The reference sorts refs in git itself (`--sort=refname` over `refs/heads/`),
	// so the branch order matches without a Go sort.
	out, err := hardenedGitStdout(p.Path, false, "for-each-ref",
		"--format=%(refname:short)", "--sort=refname", "refs/heads/")
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	branches := []string{}
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			branches = append(branches, t)
		}
	}
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
	return withWorktreeRepoLock(repo, func() response {
		return gitWorktreeCreateLocked(req, &p, repo)
	})
}

func gitWorktreeCreateLocked(req *request, p *gitParams, repo string) response {
	// 7d193f89 refuses a baseRepo that sits inside a managed worktrees tree as an
	// invalid trust root, before the repo check. Measured against 7d193f89 on an
	// ephemeral VM. A session worktree must be created from a real top-level repo,
	// never from inside another session's worktree tree. A baseRepo that fails
	// claustrum's own trust-root test gets the same refusal (baseRepoWalkFails). It
	// comes before the count check (f6010b97, round 1 row C1 P1 on the Windows VM).
	if baseRepoUnderManagedWorktrees(repo) || baseRepoWalkFails(repo) {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     managedWorktreesRefusal,
			ErrorCode: "nested_base_repo",
		})
	}
	// The git-directory trust check runs on baseRepo before anything is created
	// (gitdirtrust.go). A refusal creates no worktree directory, no entry and no
	// branch. When the daemon's own environment carries GIT_COMMON_DIR, the refusal
	// text is wrapped as a failed add. The check of the daemon's own GIT_CONFIG_COUNT
	// comes after a trust refusal and before the "no repository" answer
	// (docs/PROTOCOL.md).
	t := requestGitDirTrust(repo, false)
	if t.verdict == gitDirRefused {
		msg := t.refusal
		if daemonCommonDirSet() {
			msg = gitDirAddWrap + msg
		}
		return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	if msg, bad := daemonCountRefusal(); bad {
		return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	if t.verdict == gitDirNoRepo {
		// A listing that fails here refuses, with and without worktreeRoot, and nothing
		// is created (rows A-N3, A-N6 and A-N6b on Linux and macOS VMs, cells P3-crN
		// and Wf-P3-crNroot on a Windows VM).
		if msg := noRepoListingRefusal(t, repo, false); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
		}
		return okResult(req.ID, worktreeResult{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
	}
	// A repo whose config cannot be enumerated is refused before git runs — the
	// reference surfaces the same "config-defined hooks could not be pinned off"
	// detail under errorCode worktree_add_failed. Measured on an ephemeral VM.
	c := hostileConfigRefusal(repo, false)
	if c.refusal != "" {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     c.refusal,
			ErrorCode: "worktree_add_failed",
		})
	}
	// The reference checks the target is a repo BEFORE attempting the worktree
	// add, returning a clean not_a_repo error rather than leaking git's raw
	// "fatal: not a git repository …" output as a worktree_add_failed.
	if c.noRepo || !isRepo(repo, c.listing) {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "not a git repository",
			ErrorCode: "not_a_repo",
		})
	}
	// With a worktreeRoot the session folder is created OUTSIDE the repo, so the
	// in-repo containment is replaced by the external-location checks: absolute /
	// no-".." spelling of the root and path, 2-level containment, ownership /
	// writability of the root, and the <directory> must start out empty (or already
	// be a managed worktree directory). An empty worktreePath is judged here as a
	// relative path, not the in-repo mkdir failure.
	var externalDir string
	if p.WorktreeRoot != "" {
		root := filepath.Clean(p.WorktreeRoot)
		if msg := externalWorktreeUnsupportedRefusal(p.WorktreeRoot, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalSpellingRefusal(p.WorktreeRoot, p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// f6010b97 refuses a relative baseRepo, an absent one included, or one with a
		// ".." component, with the texts of the worktreePath check. The text names
		// baseRepo as sent. It comes after the repo check, and nothing is created (rows
		// C7, C8 and C9 on Linux and macOS VMs). Its order against the other checks of
		// this branch is not measured. It sits where git.worktree_remove has it.
		if msg := sessionFolderSpellingRefusal(p.BaseRepo, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalShapeRefusal(p.WorktreeRoot, p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A root in the repository is refused before the root-chain step. The first
		// test compares the cleaned paths and resolves no symlink (rows K1 to K3 and
		// MX14). Then claustrum makes the calls that f6010b97 and 89cb6289 make before
		// their next refusal. They are a light `rev-parse --show-toplevel`, a heavy
		// `rev-parse --absolute-git-dir` and a light `worktree list --porcelain -z`.
		// Each runs in baseRepo with its listing. With the repo check that is 9 calls
		// (Linux and macOS VMs, rows E2, W3, W4 and Y11a to Y11c). claustrum does not
		// use the answer of the absolute-git-dir call. After the ancestor test,
		// worktreeRootCheckoutRefusal refuses a root that leads into a checkout of the
		// repository.
		if msg := worktreeRootInRepoRefusal(p.WorktreeRoot, p.BaseRepo, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A D5 kill of show-toplevel or `worktree list` does not refuse here. The
		// checkout tests then run without that answer. The root-chain step below still
		// refuses a root that has, or lies below, a .git entry. A root in a listed
		// worktree whose .git entry is gone then goes on. That is claustrum's choice
		// (not measured).
		topLevel, _ := repoTopLevel(repo, nil)
		_ = repositoryCheckError(repo, nil)
		listed, _ := worktreeList(repo)
		// A worktreeRoot is refused below a directory owned by a user other than you
		// or uid 0. It is also refused below a directory writable by a shared group or
		// by every user, without the sticky bit. The refusal comes after the 9 calls,
		// with no further git call (rows K1, G2 to G18, Linux and macOS VMs). It comes
		// before the checkout tests. In rows Y11d and T7 both references send it where
		// the checkout text also applies. It comes before the root-chain step (rows
		// G18, G36a, G40a and G40b) and the tests of the root itself (rows G16 and
		// G17).
		if msg := worktreeRootAncestorRefusal(p.WorktreeRoot); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeRootCheckoutRefusal(p.WorktreeRoot, p.BaseRepo, topLevel, listed, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// The root-chain step comes before the owner and write refusals of the root.
		// The <directory> symlink refusal comes after them, then the <directory> step.
		// docs/PROTOCOL.md gives the measured order.
		dir, msg, code := externalChainCheck(p.WorktreeRoot, p.WorktreePath)
		if msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: code})
		}
		externalDir = dir
		if msg := worktreeRootShareRefusal(root); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg, code := externalDirLevelCheck(externalDir); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: code})
		}
		if msg := externalWorktreeDirNotEmptyRefusal(p.WorktreePath); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
	} else {
		// worktree-location containment, added by the reference in 7d193f89: the
		// session folder must be a fresh directory strictly inside the repo. Empty is
		// not a relative-path refusal — the reference fails it as a non-directory at
		// the parent-creation step, with its own worktreePath echoed (measured).
		if p.WorktreePath == "" {
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     fmt.Sprintf("failed to create parent directory: %q does not name a directory", p.WorktreePath),
				ErrorCode: "mkdir_failed",
			})
		}
		// The text names baseRepo as sent, so an absent one reads "" (f6010b97, row C6
		// on Linux and macOS VMs). filepath.Rel reads "" as ".", so the test is the
		// same as on repo.
		if msg := worktreePathRefusal(p.BaseRepo, p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeSymlinkRefusal(repo, p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "symlinked_component"})
		}
	}
	if _, err := os.Lstat(p.WorktreePath); err == nil {
		// With a worktreeRoot the refusal names the cleaned path: f6010b97 and
		// 90fca6e6 quote "R/cp/w1" for a worktreePath sent as "R/cp/w1/" (measured
		// on Linux and macOS VMs). Without a worktreeRoot it names the path as sent.
		// No probe sent an in-repo path with a slash to this refusal. On Windows the
		// in-repo path is spelled with the on-disk letter case of each component that
		// exists (existingPathSpelling, row W15).
		existing := filepath.Clean(p.WorktreePath)
		if p.WorktreeRoot == "" {
			existing = existingPathSpelling(p.WorktreePath)
		}
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("refusing to create worktree: %s already exists, and a new worktree is only ever created in a fresh directory", existing),
			ErrorCode: "unsafe_path",
		})
	}
	// On Windows a junction between the repo and the leaf fails the parent step, and
	// nothing is created (rows JCR1 and JCR2, Windows VM). It runs before the stale
	// registration drop below, so nothing is touched either.
	if p.WorktreeRoot == "" {
		if msg := junctionParentRefusal(repo, p.WorktreePath); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "mkdir_failed"})
		}
	}
	// The target is confirmed missing above, so any worktree registration still
	// naming it is stale (its session folder was deleted out from under git). Drop
	// just that registration so the add below recreates cleanly, the way 7d193f89
	// does — where claustrum otherwise failed "missing but already registered".
	// staleKept holds each stale entry that is still there (Linux and macOS): it
	// is locked, or the remove failed. It is empty on Windows.
	staleKept := dropStaleWorktreeRegistration(repo, p.WorktreePath)
	// `git worktree add` does not create leading directories, so the reference
	// makes the parent before adding — this is what lets a nested session path
	// such as <repo>/.claude/worktrees/<id> succeed on a fresh repo. The parent comes
	// from the cleaned path. With a trailing slash, filepath.Dir of the raw path is
	// the leaf itself, and the leaf mkdir below then failed with "file exists". The
	// references create that worktree and echo the path as sent. Measured on Linux
	// and macOS VMs. Every frame below therefore keeps the raw p.WorktreePath.
	// The parent makers set the modes and the error texts per OS.
	leafParent := filepath.Dir(filepath.Clean(p.WorktreePath))
	var parentErr error
	if p.WorktreeRoot != "" {
		parentErr = mkdirExternalWorktreeParents(externalDir)
	} else {
		parentErr = mkdirRepoWorktreeParents(repo, leafParent)
	}
	// For an external worktreeRoot, tag the <directory> level as holding managed
	// session worktrees before git runs. This is the same marker
	// baseRepoUnderManagedWorktrees looks for, so a later create whose baseRepo sits
	// under here is refused as a nested repo. A marker that cannot be made stops
	// the create before the leaf is made.
	if parentErr == nil && p.WorktreeRoot != "" {
		parentErr = ensureManagedWorktreesMarker(externalDir)
	}
	if parentErr != nil {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("failed to create parent directory: %v", parentErr),
			ErrorCode: "mkdir_failed",
		})
	}
	// 7d193f89 also creates the worktree directory ITSELF before `git worktree add`
	// (git adds into the pre-made empty dir). An unwritable/foreign-owned parent fails HERE as
	// `failed to create worktree directory: mkdirat <leaf>: <errno>` with errorCode
	// mkdir_failed — where claustrum used to reach git and return worktree_add_failed.
	// mkdirWorktreeLeaf reproduces the `mkdirat <leaf>` wording byte-for-byte (unix;
	// os.MkdirAll on Windows). Measured against 7d193f89 on an ephemeral VM.
	if err := mkdirWorktreeLeaf(p.WorktreePath); err != nil {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("failed to create worktree directory: %v", err),
			ErrorCode: "mkdir_failed",
		})
	}
	// Record the leaf's identity now, to confirm below that `git worktree add`
	// populated this same directory and nothing swapped it during the add. The
	// checkpoint holds the leaf and its parent open until the create answers.
	checkpoint := checkpointCreatedWorktree(p.WorktreePath)
	defer checkpoint.release()
	// A non-empty sourceBranch picks the start commit from the local branch and its
	// origin remote-tracking ref (pickSourceCommit, worktreesource.go). When a
	// candidate resolves, the add and the checkout get that commit's full id, so the
	// new branch's reflog reads "branch: Created from <sha>", and sourceBranch is
	// echoed exactly as sent. When none resolves, or sourceBranch is omitted or "",
	// the add gets no start point (HEAD) and the current branch is echoed instead.
	// Measured side by side against f6010b97 on Linux, Windows and macOS.
	source := p.SourceBranch
	startCommit := ""
	if source != "" {
		startCommit = pickSourceCommit(repo, source)
	}
	// existingBranch (19f30c46): attach the worktree to an already-existing local
	// branch instead of creating one. It is resolved after the sourceBranch
	// candidates and before the HEAD fallback, in the order measured against
	// f6010b97. show-ref --verify decides: a resolvable refs/heads/<existingBranch>
	// switches to attach mode, and the chosen start commit goes unused. An empty
	// value or a miss falls through to the -b <branchName> new-branch path
	// (measured against 19f30c46, which silently creates branchName when
	// existingBranch names no branch). branchName stays required in both modes.
	attachBranch := ""
	if p.ExistingBranch != "" {
		if _, ok := hardenedGit(repo, false, "show-ref", "--verify", "--quiet", "refs/heads/"+p.ExistingBranch); ok {
			attachBranch = p.ExistingBranch
		}
	}
	if startCommit == "" {
		// The fallback echoes the current branch. On a detached HEAD abbrev-ref
		// prints "HEAD", and the member is omitted (measured against f6010b97 and
		// 90fca6e6). On an unborn HEAD abbrev-ref fails, the member is omitted, and
		// the add below fails (see TestGitWorktreeCreateEmptyRepo).
		source = ""
		if s, ok := hardenedGit(repo, false, "rev-parse", "--abbrev-ref", "HEAD"); ok && s != "HEAD" {
			source = s
		}
	}
	// 7d193f89 creates the branch WITHOUT checking out (--no-track --no-checkout),
	// then populates the working tree with a separate read-tree --reset. A plain
	// `worktree add` that checks out leaves an ORIG_HEAD in the worktree's admin dir
	// that the two-step does not. That is wire-visible via files.read. An explicit
	// start commit is passed only when a sourceBranch candidate resolved. Otherwise
	// -b defaults to HEAD.
	// worktreeBranch is the branch the worktree ends up on: the attached existingBranch,
	// or the -b branchName it creates. It drives the add commit-ish, the read-tree ref,
	// and the result's new `branch` field. createdBranch is only the branch this add
	// CREATES, so the rollback below never deletes a pre-existing attached branch.
	worktreeBranch := p.BranchName
	createdBranch := p.BranchName
	var addArgs []string
	if attachBranch != "" {
		// Attach mode: `worktree add --no-checkout <path> <existingBranch>` — no -b and
		// no --no-track, since the branch already exists (measured, 19f30c46).
		worktreeBranch = attachBranch
		createdBranch = ""
		addArgs = []string{"worktree", "add", "--no-checkout", p.WorktreePath, attachBranch}
	} else {
		addArgs = []string{"worktree", "add", "--no-track", "--no-checkout", "-b", p.BranchName, p.WorktreePath}
		if startCommit != "" {
			addArgs = append(addArgs, startCommit)
		}
	}
	// The checkout reads the start commit's id when the add got one, and the
	// worktree's branch otherwise (the attached branch, or the -b branch made off
	// HEAD). Measured against f6010b97.
	checkoutRev := "refs/heads/" + worktreeBranch
	if attachBranch == "" && startCommit != "" {
		checkoutRev = startCommit
	}
	// The read-tree checkout names the git dir of baseRepo with --git-dir, as
	// measured against f6010b97 and 90fca6e6 on a Windows VM. For a linked-worktree
	// baseRepo that is its own admin dir. The references ask git for it with
	// rev-parse --absolute-git-dir, on the heavy profile, before the add.
	gitDir, gitAnswered := worktreeBaseGitDir(repo)
	// A caller-supplied timeoutMs (4534d86) bounds the add, the checkout and the copy
	// step. At timeoutMs 0 with D5 off, both contexts are the unbounded context that
	// hardenedGit uses, so the default path is byte-identical.
	d5Ctx, callerCtx, cancelCtx := worktreeCreateCtx(p.TimeoutMs)
	defer cancelCtx()
	// The caller's deadline does not kill the add. The daemon waits for git to exit.
	// Measured against f6010b97 and 90fca6e6 on a macOS VM. Each failure frame quotes
	// git's stderr through worktreeGitText.
	addStderr, addErr := hardenedGitStderr(d5Ctx, repo, false, addArgs...)
	if addErr != nil && attachBranch != "" {
		// A refused attach falls back to a new branch, unless the failed attach add
		// deleted the leaf or put a new directory in its place. Then the request
		// answers the attach add's own failure. Measured against f6010b97 and 90fca6e6
		// on a macOS VM, with the leaf deleted and with it replaced by a new empty
		// directory. A leaf left as it was gets the fallback.
		if verifyCreatedWorktree(p.WorktreePath, checkpoint) != "" {
			undoFailedAdd(p.WorktreePath, checkpoint)
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     "git worktree add failed: " + worktreeGitText(addStderr, addErr),
				ErrorCode: "worktree_add_failed",
			})
		}
		// The fallback add names branchName and gets the start point that
		// sourceBranch resolved to, if any, as the new-branch path does. The worktree
		// then ends up on that new branch, and the rollbacks below run the branch step
		// on it. If the
		// fallback also fails, the frame quotes both texts, each one made on its own.
		// Measured against f6010b97 and 90fca6e6 on a macOS VM.
		attachStderr, attachErr := addStderr, addErr
		fallbackArgs := []string{"worktree", "add", "--no-track", "--no-checkout", "-b", p.BranchName, p.WorktreePath}
		if startCommit != "" {
			fallbackArgs = append(fallbackArgs, startCommit)
		}
		addStderr, addErr = hardenedGitStderr(d5Ctx, repo, false, fallbackArgs...)
		if addErr != nil {
			undoFailedAdd(p.WorktreePath, checkpoint)
			return okResult(req.ID, worktreeResult{
				Success: false,
				Error: fmt.Sprintf("git worktree add failed: %s (attaching to the existing branch %s was refused first: %s)",
					worktreeGitText(addStderr, addErr), attachBranch, worktreeGitText(attachStderr, attachErr)),
				ErrorCode: "worktree_add_failed",
			})
		}
		worktreeBranch = p.BranchName
		createdBranch = p.BranchName
		checkoutRev = "refs/heads/" + p.BranchName
		if startCommit != "" {
			checkoutRev = startCommit
		}
	}
	if addErr != nil {
		// A failed add answers this frame even when the caller's deadline already
		// expired. The rollback runs no git call and removes the leaf only if it is
		// empty (undoFailedAdd). A branch that existed before the call is kept.
		// Measured against f6010b97 and 90fca6e6 on a macOS VM.
		undoFailedAdd(p.WorktreePath, checkpoint)
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "git worktree add failed: " + worktreeGitText(addStderr, addErr),
			ErrorCode: "worktree_add_failed",
		})
	}
	// The add succeeded. The .git file of the new worktree must name a registration
	// of this repository, and the commondir file and the gitdir record of that
	// registration must be files that can be read (createdRegistrationRefusal, cells
	// P-g to P-j). If not, the create is
	// refused, with no checkout and no rollback: the leaf, the registration and the
	// branch stay. The test comes before the deadline test: with a timeoutMs that
	// expired during the add, 89cb6289 answers this refusal (row D-9, macOS VM).
	//
	// The .git file is read once for the tests and the index folder, here. The
	// commondir file of gitDir is read once for them, in the tests. The tests answer
	// the index folder, so it is the entry that they saw. A .git file or a commondir
	// file that changes later cannot move the index to a folder that the tests did
	// not see. On Linux and macOS a rollback removes that same entry (tested), and
	// it reads neither file again.
	//
	// The tests need the git directory that git answered. If rev-parse gave no
	// answer, gitDir is a guess (repoGitDir), and for a baseRepo that is a subfolder
	// of a repository that folder does not exist. The tests then do not run, and the
	// index folder is the one that the .git file names. A rollback then reads both
	// files again for its own check (createdWorktreeAdminDir). That is claustrum's
	// choice (not measured).
	adminDir := worktreeAdminDir(p.WorktreePath)
	indexDir := ""
	if adminDir != "" {
		indexDir = absoluteAdminDir(p.WorktreePath, adminDir)
	}
	var tested testedRegistration
	if gitAnswered {
		var text string
		if indexDir, text = createdRegistrationRefusal(gitDir, p.WorktreePath, adminDir); text != "" {
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     text,
				ErrorCode: "unsafe_path",
			})
		}
		tested = acceptRegistration(indexDir)
		defer tested.release()
		// A stale entry that the step before the add left is not the registration of
		// this create. 89cb6289 answers the refusal there after 9 git calls, and the
		// pair of the next test is not among them (cells A9 g, A9b g and A10 g,
		// Linux VM).
		if text := staleRegistrationRefusal(p.WorktreePath, indexDir, staleKept); text != "" {
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     text,
				ErrorCode: "unsafe_path",
			})
		}
	}
	// indexDir is the registration that gets the index (createdRegistrationRefusal, or
	// absoluteAdminDir if rev-parse gave no answer).
	// Its gitdir record names worktreePath after symlink resolution.
	// If it was read and names anything else, the create is refused, with no checkout
	// and no rollback (cells P-c and P-k to P-m). Before
	// the answer, 89cb6289 runs the pair of gitDirWorkTreeToplevel with baseRepo as sent
	// as the work tree (row I07a, macOS VM, and cell P-c, Linux and macOS VMs). claustrum makes the calls and does not use their
	// answer: what 89cb6289 takes from them is not measured. This test comes before
	// the deadline test too: with a timeoutMs that expired during the add, 89cb6289
	// answers this refusal and keeps the leaf and the branch (cell P-f, Linux and macOS VMs).
	if adminRecordMismatch(indexDir, p.WorktreePath) {
		_, _ = gitDirWorkTreeToplevel(gitDir, repo, commonDirPinEnv(repo))
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     adminRecordRefusal(p.WorktreePath),
			ErrorCode: "unsafe_path",
		})
	}
	// If the caller's deadline expired during the add, the request
	// answers timeout "before the checkout started" and rolls back. errorCode
	// "timeout" is reserved for the caller's own deadline. The reference has no D5.
	// Every rollback below appends an undo text when one of its steps fails.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired before the checkout started)", p.TimeoutMs)
		undo, kept := undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      msg + undo,
			ErrorCode:  "timeout",
			BranchKept: kept,
		})
	}
	// Second half of the two-step: the read-tree fills the working tree from the new
	// branch. It runs in the leaf with --git-dir set to the git dir of baseRepo and its
	// index in a new temporary directory (GIT_INDEX_FILE). Its --work-tree is the leaf
	// with its symlinks resolved on Linux and macOS (checkoutWorkTree). After git exits 0, that
	// index becomes the new worktree's own index. Measured against f6010b97 and
	// 90fca6e6 on a Windows VM. A checkout that fails also fails the request. See the
	// last arm of the switch.
	if indexDir != "" {
		rtStderr, rtDrained, rtErr, installErr := runWorktreeCheckout(callerCtx, p.WorktreePath,
			checkoutWorkTree(p.WorktreePath, checkpoint.resolved), gitDir,
			indexDir, tested, checkoutRev, commonDirPinEnv(repo))
		switch {
		case installErr != nil:
			// git exited 0, and the index did not reach the registration. On Linux and
			// macOS VMs 89cb6289 answers that as a failed checkout and rolls back (rows
			// A14, A14f and A14b, and cell X1). The text is indexInstallText. A placement that
			// fails after a drain overrun takes this arm too. That is claustrum's
			// choice (not measured).
			msg := "git worktree add failed (checkout): " + indexInstallText(rtStderr, installErr)
			undo, kept := undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "worktree_add_failed",
				BranchKept: kept,
			})
		case rtDrained:
			// git exited 0, but a checkout descendant held the daemon's output pipe
			// past the ~5s drain cap. hardenedGitCheckout already killed and reaped
			// the process group. The checkout counts as finished, so the request goes
			// on to the copy step. The deadline test after the copy step then answers
			// timeout "after the checkout finished" and rolls back. If the deadline has
			// not expired, the request succeeds. Measured against f6010b97 and
			// 90fca6e6 on a macOS VM.
		case rtErr != nil && p.TimeoutMs > 0 && callerTimeoutFired(callerCtx):
			// The caller's deadline killed the checkout. The text after "during the
			// checkout): " is the killed git's stderr made by worktreeGitText, or the
			// exec error, for example "signal: killed", when that text is empty.
			// Measured against f6010b97 and 90fca6e6 on a macOS VM.
			msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired during the checkout): %s",
				p.TimeoutMs, worktreeGitText(rtStderr, rtErr))
			undo, kept := undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "timeout",
				BranchKept: kept,
			})
		case rtErr != nil:
			// The checkout failed on its own (for example a tree object it cannot
			// read). The request fails with git's text, and the rollback removes the
			// new directory and its registration. The branch step then deletes the
			// branch this call created only when another ref reaches its tip. The
			// frame matches 90fca6e6 byte for byte, apart from git's graft-file
			// deprecation hint: lines. f6010b97 and claustrum both set GIT_GRAFT_FILE,
			// so where git prints the hint, their text starts with it. In attach mode
			// createdBranch is "", so the attached branch is kept, as measured against
			// f6010b97.
			msg := "git worktree add failed (checkout): " + worktreeGitText(rtStderr, rtErr)
			undo, kept := undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "worktree_add_failed",
				BranchKept: kept,
			})
		}
	}
	// Post-condition: the add must have populated the directory claustrum created, not
	// one swapped in during the add. claustrum's own guard; empty on an unraced create.
	if msg := verifyCreatedWorktree(p.WorktreePath, checkpoint); msg != "" {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     msg,
			ErrorCode: "worktree_add_failed",
		})
	}
	// `git worktree add` checks out tracked files only, so the reference seeds the new
	// worktree in two passes: the untracked files that .worktreeinclude names AND git
	// also ignores (copyWorktreeIncludes), and the git-ignored files under .claude/,
	// which need no manifest entry at all (copyClaudeDir in worktreeclaude.go).
	//
	// An earlier version of this comment said 7d193f89 had dropped the .claude/ pass.
	// It had not. Measured against 19f30c46 and 90fca6e6 on a linux VM; the old reading
	// came from a probe repo whose .claude/ was untracked rather than git-ignored, for
	// which the pass lists nothing.
	//
	// Best-effort: the worktree exists, so a copy failure does not fail the request.
	populateWorktree(repo, p.WorktreePath)
	// The caller's deadline does not kill the copy step. The daemon lets the step
	// finish and then tests the deadline. If the deadline expired, the request answers
	// timeout "after the checkout finished" and rolls back. Measured against f6010b97
	// and 90fca6e6 on a macOS VM.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired after the checkout finished)", p.TimeoutMs)
		undo, kept := undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      msg + undo,
			ErrorCode:  "timeout",
			BranchKept: kept,
		})
	}
	return okResult(req.ID, worktreeResult{Success: true, Path: p.WorktreePath, SourceBranch: source, Branch: worktreeBranch})
}

func gitWorktreeRemove(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	repo := p.repoDir()
	return withWorktreeRepoLock(repo, func() response {
		return gitWorktreeRemoveLocked(req, &p, repo)
	})
}

func gitWorktreeRemoveLocked(req *request, p *gitParams, repo string) response {
	refuse := func(msg string) response {
		return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
	}
	// 7d193f89 refuses a baseRepo inside a managed worktrees tree as an invalid trust
	// root (no errorCode on remove). A baseRepo that does not exist skips the check.
	// The reference was measured on a macOS VM (f6010b97, rows M01 and M02).
	if _, err := os.Stat(repo); !errors.Is(err, fs.ErrNotExist) && baseRepoUnderManagedWorktrees(repo) {
		return refuse(managedWorktreesRefusal)
	}
	// A baseRepo that fails claustrum's own trust-root test gets the same refusal
	// (baseRepoWalkFails). It comes before the worktreePath checks, the Windows
	// worktreeRoot refusal and the count check. f6010b97 showed that order in round 1
	// rows O1 to O4, WR1 and A1 P1 (Windows VM).
	if baseRepoWalkFails(repo) {
		return refuse(managedWorktreesRefusal)
	}
	var target removeTarget
	var err error
	if p.WorktreeRoot != "" {
		// With a worktreeRoot the worktree lives outside the repo. The external checks
		// replace the in-repo containment. Measured against 7d193f89 and f6010b97.
		// An empty worktreePath is judged as a relative path here (f6010b97, row R10
		// on Linux and macOS VMs).
		if msg := externalWorktreeUnsupportedRefusal(p.WorktreeRoot, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeExternalSpellingRefusal(p.WorktreeRoot, p.WorktreePath, "remove"); msg != "" {
			return refuse(msg)
		}
		// f6010b97 then refuses a relative baseRepo, an absent one included, or one with
		// a ".." component, with the same texts. The text names baseRepo as sent. It
		// comes before the shape check and runs no git (rows R1 to R4, R9 and R11 on
		// Linux and macOS VMs). The ".." test is by whole component (rows R12 to R14 on
		// Linux and macOS VMs).
		if msg := sessionFolderSpellingRefusal(p.BaseRepo, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeExternalShapeRefusal(p.WorktreeRoot, p.WorktreePath, "remove"); msg != "" {
			return refuse(msg)
		}
		// A root that is baseRepo or lies beneath it is refused after the shape check,
		// with no git call. The leaf is not looked at. f6010b97 and 89cb6289 do the
		// same on Linux and macOS VMs. Rows Q2 to Q6 and MX10 get the shape text first.
		// Rows Q8 and MX11, with no leaf, and row Q16, with a symlinked <directory>,
		// get this refusal. The macOS rows are MX3, MX4, MX11, MX13 and MX15.
		if msg := worktreeRootInRepoRefusal(p.WorktreeRoot, p.BaseRepo, "remove"); msg != "" {
			return refuse(msg)
		}
		// The work-tree check comes before the dir-symlink check. A base that the check
		// refuses gets the work-tree refusal even when the directory level is a symlink.
		// Measured side by side against f6010b97 on a Linux VM (rows K10, K11, K14 and
		// K16 with a symlinked directory level).
		msg, topLevel := externalWorkTreeRefusal(repo)
		if msg != "" {
			return refuse(msg)
		}
		// A root that the daemon cannot open or search answers its own error later,
		// and not this refusal (cell U13b, Linux and macOS VMs).
		dirSymlinkChecked := !externalRootDenied(p.WorktreePath)
		if dirSymlinkChecked {
			if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "remove"); msg != "" {
				return refuse(msg)
			}
		}
		// 89cb6289 makes two more git calls here: a light `worktree list --porcelain -z`,
		// then a heavy `rev-parse --absolute-git-dir`. Each runs in baseRepo with its
		// listing. The second one does not run for a worktreeRoot below a folder of mode 000:
		// that request answers its lstat error after 7 calls (Linux VM, row T6).
		// claustrum runs the second call after it located the worktree folder.
		// f6010b97 made the same two calls (rows WR00,
		// WR07, WR09 and WR14). Right after the first call, the 7th git call,
		// f6010b97 and 89cb6289 refuse a root that leads into a checkout of the
		// repository. Then they refuse a root that does not exist or does not resolve.
		// Nothing is deleted (Linux and macOS VMs, rows Q6s, Q17 to Q19, E1, W1, W2, Y1,
		// Y5, Y6, Y9 and Y10). Rows T2b to T5 on both VMs, Linux row T1 and macOS row T1m
		// measured the same. If the D5 deadline stops the first `worktree list`, the removal
		// answers the work-tree refusal with the exec error. A D5 hit on `rev-parse
		// --show-toplevel` or on the repository check does the same. Both are claustrum's
		// choice (not measured). After any other failure of `worktree list -z`, the
		// daemon runs `worktree list --porcelain` with its listing. If that call fails
		// too, the removal is refused and nothing is deleted (89cb6289, row DG2s-g on a
		// Linux VM). See worktreeListForRemove.
		listed, msg := worktreeListForRemove(repo)
		if msg != "" {
			return refuse(msg)
		}
		if msg := worktreeRootCheckoutRefusal(p.WorktreeRoot, p.BaseRepo, topLevel, listed, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeRootMissingRefusal(p.WorktreeRoot); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeRemoveHomeRefusal(p.WorktreePath); msg != "" {
			return refuse(msg)
		}
		target, err = locateExternalWorktree(p.WorktreePath)
		// The skipped symlink refusal runs here, before any delete. No row measures it.
		if err == nil && !dirSymlinkChecked {
			if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "remove"); msg != "" {
				target.close()
				return refuse(msg)
			}
		}
	} else {
		// Empty is failed as a non-directory, not as a relative path (measured against
		// 7d193f89).
		if p.WorktreePath == "" {
			return refuse(fmt.Sprintf("failed to remove worktree: %q does not name a directory", p.WorktreePath))
		}
		// The containment of 7d193f89: only a path strictly inside the repo goes on, so
		// a "~"-expanded home path is refused here, with the reference's wording, before
		// the home guard is consulted. The text names baseRepo as sent, so an absent one
		// reads "" (f6010b97, rows A12 and A12b on Linux and macOS VMs, A12 on Windows).
		// filepath.Rel reads "" as ".", so the test is the same as on repo.
		if msg := worktreePathRefusal(p.BaseRepo, p.WorktreePath, "remove"); msg != "" {
			return refuse(msg)
		}
		// A symlinked component under the repo, the leaf excluded, is refused before
		// anything is deleted (matches 7d193f89. No errorCode on remove).
		if msg := worktreeSymlinkRefusal(repo, p.WorktreePath, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeRemoveHomeRefusal(p.WorktreePath); msg != "" {
			return refuse(msg)
		}
		// A baseRepo the daemon can open but not search (mode 0600), or cannot open at
		// all, answers with the error from its look at .claude, and that error is the
		// whole reply. Measured side by side against f6010b97 on Linux, and the
		// reference's answers also on macOS.
		//
		// On Linux and macOS the look is at the first component of worktreePath below
		// baseRepo. For a repository of mode 0600, 89cb6289 answers "statat wt" for
		// <repo>/wt (cell U19 on Linux and macOS VMs, cell N4 on a Linux VM), "statat a"
		// for <repo>/a/wt and <repo>/a/b/c/wt (cells N6a and N6b, Linux VM) and "statat
		// .claude" for a path under .claude (cell N7a, Linux VM). Mode 0400 answers the
		// same (cell N5, Linux VM). Windows is not measured, and keeps the look at
		// .claude.
		first := ".claude"
		if runtime.GOOS != "windows" {
			if rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath)); err == nil {
				first, _, _ = strings.Cut(rel, string(filepath.Separator))
			}
		}
		if err := statInsideDir(repo, first); err != nil {
			return refuse("failed to remove worktree: " + err.Error())
		}
		target, err = locateInRepoWorktree(repo, p.WorktreePath)
	}
	if err != nil {
		return refuse(worktreeRemoveText(err))
	}
	defer target.close()
	// answered is the answer of `rev-parse --absolute-git-dir` in baseRepo, or "".
	answered := ""
	if p.WorktreeRoot != "" {
		if out, err := hardenedGitStdout(repo, true, "rev-parse", "--absolute-git-dir"); err == nil {
			answered = strings.TrimRight(out, "\r\n")
		}
	}
	if target.parent == nil {
		return removeGoneWorktree(req, p, repo, target.path)
	}
	// A leaf that is a symbolic link or not a directory is refused, and nothing is
	// deleted. In the repo no git runs first. Before, a symbolic link to a sibling
	// worktree made the removal delete that sibling, its entry and its branch (rows
	// S01 to S05).
	fi, err := target.parent.Lstat(target.leaf)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return removeGoneWorktree(req, p, repo, target.path)
	case err != nil:
		return refuse(worktreeRemoveText(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return refuse(worktreeRemoveText(refuseWorktree("%s is a symbolic link, not a worktree directory", target.path)))
	case !fi.IsDir():
		return refuse(worktreeRemoveText(refuseWorktree("%s is not a directory", target.path)))
	}
	leafDir, err := target.parent.OpenRoot(target.leaf)
	if err != nil {
		return refuse(worktreeRemoveText(err))
	}
	if err := checkLeafIdentity(target.parent, target.leaf, leafDir, target.path); err != nil {
		_ = leafDir.Close()
		return refuse(worktreeRemoveText(err))
	}
	// The `.git` file is read before the git calls below. In the repo no git has run
	// yet. With worktreeRoot the external checks above already ran git. A leaf swapped
	// during the git calls below is not detected: the removal then deletes the new directory and drops the
	// entry that the old `.git` file named (row Z01).
	gitDir, dotGitErr := worktreeGitFileTarget(leafDir, target.path)
	_ = leafDir.Close()
	// namesGitDir is true when the `.git` file names a git dir.
	namesGitDir := dotGitErr == nil
	// dropEntry is false for a baseRepo that does not exist as sent. 89cb6289 then
	// deletes the worktree folder and keeps its entry, and its one git call is the read
	// of the user excludes. Probe row 7c sends <T>/missing/.. on Linux and macOS VMs.
	// Battery rows A1, A3 and G3 send <T>/missing/.., <F>/missing/../T and <T>/dl/.. on
	// a Linux VM. The lock check below still reads <T>/.git/worktrees for that
	// baseRepo, and a locked worktree is refused. 89cb6289 deletes it (row p6d on
	// Linux and macOS VMs). That refusal is divergence D22.
	dropEntry := true
	_, statErr := os.Stat(repo)
	repoMissing := errors.Is(statErr, fs.ErrNotExist)
	if p.WorktreeRoot == "" {
		// If the repo's config cannot be listed, or baseRepo holds no repository, or its
		// git directory fails the trust check (gitdirtrust.go), the registrations cannot
		// be examined. Nothing is deleted. Measured side by side against f6010b97 on
		// Linux, macOS and Windows VMs (rows G08, T03 and T04 on macOS). For a baseRepo
		// that holds no repository, 89cb6289 runs the listing and the rev-parse twice
		// before that answer (row A5 on Linux, macOS and Windows VMs).
		verdict := requestGitDirTrust(repo, false).verdict
		if verdict == gitDirRefused {
			// The one git call of 89cb6289 is the read of the user excludes (row DG2i-g
			// on a Linux VM).
			userExcludesFile()
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// A refused daemon GIT_CONFIG_COUNT gets the same answer. hostileConfigRefusal
		// below does not refuse a baseRepo that does not stat, such as <T>/missing/..,
		// so the count is checked here. f6010b97 answers so in row A1 P1 (Linux and
		// macOS VMs).
		if _, bad := daemonCountRefusal(); bad {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// The listing carries the heavy profile of the rev-parse that follows it, as on
		// f6010b97 (Linux, macOS and Windows VMs). When the check fails, f6010b97 runs it
		// once more before it answers: a second listing, and a second rev-parse when that
		// listing passes. That repeat was measured on Linux and macOS VMs (a corrupt
		// config, and a git directory that git cannot use). The answer is the same
		// whatever the second check finds.
		//
		// A listing that answers "no repository" (hostileConfigRefusal) gets the same
		// answer here. With no git on PATH, 89cb6289 answers so too (row L13xa on a Linux
		// VM). For a listing that says "not a git repository", 89cb6289 was measured with
		// the worktree folder absent only. Those rows are L14a, L14b, L14d, L14e and N01
		// to N04. With the folder present, claustrum answers the lock-check refusal. Not
		// measured.
		if repoMissing {
			// With no git on PATH the answer is the lock-check refusal, and nothing is
			// deleted. 89cb6289 answers so for <T>/missing/.. and for <T>/dl/.. with a
			// dangling link `dl` (rows p1 and p1b on Linux and macOS VMs). No git call
			// starts.
			if gitLookupError() != nil {
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			dropEntry = false
		} else {
			c := hostileConfigRefusal(repo, true)
			var checkErr error
			if !c.refused() {
				answered, checkErr = repositoryGitDir(repo, &c.listing)
			}
			if c.refused() || checkErr != nil {
				repeatRepositoryCheck(repo)
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			// The trust check found no repository, and git found one. Not measured.
			// claustrum answers as before.
			if verdict == gitDirNoRepo {
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
		}
	}
	// The registrations are those of the git directory that the rev-parse call
	// answered (registrationGitDir). With worktreeRoot, the directory is verifyGitDir,
	// as before.
	commonDir := verifyGitDir(repo)
	probe := newRegistrationProbe(answered, target.path)
	if p.WorktreeRoot == "" {
		commonDir = registrationGitDir(repo, answered)
		probe = newRegistrationProbe(answered, inRepoWorkTree(repo))
	}
	sp := newWorktreePathSet(target.path, p.WorktreePath)
	// A worktree that is locked in the `.git` folder of baseRepo is refused, also when
	// the entries are read from another git directory (D22, lockedInBaseRepo). That
	// holds with worktreeRoot too (rows q2 and q3 on a Linux VM).
	named := ""
	if namesGitDir {
		named = gitDir
	}
	if msg := baseRepoLockRefusal(repo, commonDir, named, target.path, p.WorktreePath, sp,
		lockedWorktreeRefusal(p.WorktreePath)); msg != "" {
		return refuse(msg)
	}
	entry := ""
	if dotGitErr == nil {
		// The answer of the pair gives one more spelling of the worktree: the top
		// level that git names for baseRepo, joined with the rest of worktreePath. With
		// worktreeRoot the pair runs and claustrum does not use its answer: no row
		// shows an entry that it verifies there.
		respell := func() string {
			top := probe.run()
			if top == "" || p.WorktreeRoot != "" {
				return ""
			}
			rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath))
			if err != nil || !filepath.IsLocal(rel) {
				return ""
			}
			return filepath.Join(filepath.FromSlash(top), rel)
		}
		inRepoAnswer := ""
		if p.WorktreeRoot == "" {
			inRepoAnswer = answered
		}
		entry, dotGitErr = verifiedWorktreeEntry(gitDir, commonDir, inRepoAnswer, target.path, sp, respell)
	}
	// A `.git` file that names a git dir which is not a verified entry: 89cb6289 runs
	// the listing and the rev-parse once more (repeatRepositoryCheck). A `.git` file
	// that names no git dir, or no `.git` file, gets no such calls (probe rows 11 and
	// K1). Not measured: a baseRepo that does not exist. claustrum makes no call there.
	if namesGitDir && entry == "" && !repoMissing {
		repeatRepositoryCheck(repo)
	}
	lockChecked := false
	// matches is the count of entries that name the worktree by path, or -1 when the
	// lock check did not count them.
	matches := -1
	if p.WorktreeRoot != "" && dotGitErr != nil {
		// Beneath a worktreeRoot, a worktree that cannot be verified is removed only
		// when it is an empty directory or a stale worktree of this repository. Else it
		// is refused and left in place. A locked registration by path comes first (row
		// E06). Rows E01 to E08, L03, X03 and X04.
		//
		// With a refused daemon GIT_CONFIG_COUNT, externalWorkTreeRefusal has already
		// refused a baseRepo that exists. For a missing absolute baseRepo without a
		// ".." component, the answer is a lock-check refusal that names the repository
		// (docs/PROTOCOL.md).
		if _, bad := daemonCountRefusal(); bad {
			return refuse(unreadableRepoLockCheckRefusal(p.WorktreePath, repo))
		}
		// With no git on PATH the answer is the same refusal. Only a baseRepo that does
		// not exist comes this far then: externalWorkTreeRefusal refuses every other
		// one. 89cb6289 and f6010b97 answer so on a Linux VM for a leaf that holds a
		// file (row L13wa) and for an empty leaf (row L13we). With git the empty leaf is
		// removed below (row L13we-g). A leaf whose `.git` file names an entry of the
		// missing repository gets this refusal too (row DG1). With git that leaf is
		// deleted below, as on both references (row DG1-g).
		if gitLookupError() != nil {
			return refuse(unreadableRepoLockCheckRefusal(p.WorktreePath, repo))
		}
		probe.pairIfWorktreesDir(commonDir)
		locked, readable, _ := worktreeLockedByPath(commonDir, sp)
		if !readable {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		if locked {
			return refuse(lockedWorktreeRefusal(p.WorktreePath))
		}
		lockChecked = true
		if target.parent.Remove(target.leaf) == nil {
			return removeGoneWorktree(req, p, repo, target.path)
		}
		if !entryAlreadyGone(gitDir, commonDir) {
			var r *worktreeRefusal
			if errors.As(dotGitErr, &r) {
				return refuse(fmt.Sprintf("refusing to remove worktree: %s is not a worktree of %s (%v), "+
					"so it is left in place; remove it by hand if it is a leftover", p.WorktreePath, repo, dotGitErr))
			}
			// One input is measured, with git on PATH (row DG1c-g on a Linux VM). It is a
			// missing baseRepo and a `.git` file that names an entry of another missing
			// repository. 89cb6289 sends this frame there. Its detail is the hooks refusal
			// with the chdir error of baseRepo, and its one git call is the read of the
			// user excludes. For a baseRepo that exists, the detail is claustrum's text
			// from before, the error of the open (row Q20).
			detail := dotGitErr.Error()
			if repoMissing {
				detail = hooksRefusalPrefix + (&fs.PathError{Op: "chdir", Path: repo, Err: errors.Unwrap(statErr)}).Error()
			}
			return refuse(fmt.Sprintf("failed to remove worktree: could not verify that %s is a "+
				"worktree of %s (%s); retry", filepath.Clean(p.WorktreePath), repo, detail))
		}
	}
	// The lock check. A verified entry is locked when it carries a `locked` marker of
	// any kind, a dangling symbolic link included (row L04). Without a verified entry
	// every registration that names the worktree is checked (rows R05 and L02).
	if entry != "" {
		if verifiedEntryLocked(commonDir, entry) {
			return refuse(lockedWorktreeRefusal(p.WorktreePath))
		}
	} else if !lockChecked {
		probe.pairIfWorktreesDir(commonDir)
		locked, readable, n := worktreeLockedByPath(commonDir, sp)
		if !readable {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		if locked {
			return refuse(lockedWorktreeRefusal(p.WorktreePath))
		}
		matches = n
	}
	if err := deleteWorktreeDir(target.parent, target.leaf, target.path); err != nil {
		return refuse(worktreeRemoveText(err))
	}
	// The entry goes after the tree. A verified entry that cannot be deleted is
	// reported (rows D01 to D03 of the f6010b97 runs). The branch step still runs
	// (rows R19 and B2-06 of the 89cb6289 runs). Without a verified entry, the one
	// entry whose record names the worktree is deleted. A failure of that delete is
	// not reported (rows R01 to R04 of the f6010b97 runs). If no entry names the
	// folder, the call log of 89cb6289 shows the pair of registrationProbe once more
	// (probe rows K1 and K1b, battery row E13, row D-83 on a Windows VM). With one
	// entry or two it does not (rows 12 and 11). claustrum runs that pair after the
	// delete of the tree.
	pending := ""
	switch {
	case !dropEntry:
	case entry != "":
		if err := dropWorktreeEntry(commonDir, entry); err != nil && !errors.Is(err, fs.ErrNotExist) {
			pending = fmt.Sprintf("removed the worktree but could not drop its registration (%v)", err)
		}
	default:
		if matches == 0 {
			probe.pairIfWorktreesDir(commonDir)
		}
		dropWorktreeEntryByPath(commonDir, sp)
	}
	// The branch step adds no text to the reply. A kept branch adds the member only.
	kept := runBranchStep(repo, p.BranchName).kept()
	return okResult(req.ID, worktreeRemoveResult{Success: pending == "", Error: pending, BranchKept: kept})
}

// removeGoneWorktree answers a remove whose worktree directory is not there. The
// registration is checked by path. A locked one is refused. Else the one entry whose
// record names the worktree is deleted. Then the branch step runs. path is the
// spelling of the worktree, or "" when a parent directory is missing too.
//
// In the repo, a config that cannot be listed, or a git directory that fails the trust
// check, is refused with its reason (rows G07, T01 and T02). A baseRepo that holds no
// repository skips the registration (rows G05 and G05b). Measured on the reference,
// f6010b97, on a macOS VM (rows G01 to G06).
func removeGoneWorktree(req *request, p *gitParams, repo, path string) response {
	refuse := func(msg string) response {
		return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
	}
	lockCheck := func(reason string) response {
		return refuse("failed to remove worktree: could not check whether " + p.WorktreePath +
			" is locked (" + reason + "); retry")
	}
	// The check of the daemon's own GIT_CONFIG_COUNT refuses in both modes. Without
	// worktreeRoot it comes after a trust refusal and before the "no repository"
	// answer (docs/PROTOCOL.md).
	checkRegistration := true
	// answered is the answer of `rev-parse --absolute-git-dir` in baseRepo, or "".
	answered := ""
	if p.WorktreeRoot == "" {
		t := requestGitDirTrust(repo, false)
		if t.verdict == gitDirRefused {
			return lockCheck(t.refusal)
		}
		// A daemon GIT_DIR that names a file: no git call, and the branch is kept
		// (gitDirTrust.noGit). An empty branchName is not measured there.
		msg, bad := daemonCountRefusal()
		if t.verdict == gitDirNoRepo && t.noGit && !bad {
			return okResult(req.ID, worktreeRemoveResult{Success: true, BranchKept: !skippedBranchName(p.BranchName)})
		}
		if bad {
			return lockCheck(msg)
		}
		switch t.verdict {
		case gitDirNoRepo:
			// 89cb6289 runs the listing and the rev-parse here too, and the rev-parse
			// fails (probe row 16 on Linux, macOS and Windows VMs). claustrum does not
			// use the answer of the rev-parse. A listing that fails refuses, and nothing
			// is deleted (row A-N4 on Linux and macOS VMs, cell P3-rmN on a Windows VM).
			// Without the entry of noRepoPinned the answer of the listing is not read.
			c := hostileConfigRefusal(repo, true)
			if c.refusal != "" && t.noRepoPinned() {
				return lockCheck(c.refusal)
			}
			if !c.refused() {
				noRepositoryAt(repo, c.listing)
			}
			checkRegistration = false
		default:
			// A listing that answers "no repository" skips the registration. The branch
			// step then runs its own listing, which fails the same way, and keeps the
			// branch with no other call. 89cb6289 runs those two listings and nothing
			// else (rows L14a, L14b, L14d, L14e and N01 to N04 on Linux and macOS VMs,
			// row P7 on Windows).
			c := hostileConfigRefusal(repo, true)
			if c.refusal != "" {
				return lockCheck(c.refusal)
			}
			if !c.noRepo {
				var checkErr error
				answered, checkErr = repositoryGitDir(repo, &c.listing)
				checkRegistration = checkErr == nil
			} else {
				checkRegistration = false
			}
		}
	} else if msg, bad := daemonCountRefusal(); bad {
		return lockCheck(msg)
	}
	if checkRegistration {
		// Without worktreeRoot the registrations are those of registrationGitDir. If
		// <git dir>/worktrees exists, the call log of 89cb6289 shows the pair of
		// registrationProbe once (probe rows 15, 15b and R17a on Linux and macOS VMs, 15
		// and 15b on Windows). If no entry names the worktree, it shows the pair twice
		// (battery rows W01 to W07-b, W14, W15 and W16 on a Windows VM, with an empty
		// worktrees directory). claustrum runs the first pair before it looks at the
		// entries and the second before the delete of the entry. With worktreeRoot
		// nothing changed.
		commonDir := verifyGitDir(repo)
		var probe registrationProbe
		if p.WorktreeRoot == "" {
			commonDir = registrationGitDir(repo, answered)
			probe = newRegistrationProbe(answered, inRepoWorkTree(repo))
			probe.pairIfWorktreesDir(commonDir)
		}
		// The baseRepo part of the path is also matched in its resolved form, and the
		// rest as sent. So a baseRepo sent in 8.3 form or through a junction still finds
		// a locked registration when the worktree and its parent are gone (Windows VM,
		// rows GL1 and GL2). A present worktree does not get this spelling, so an 8.3
		// leaf keeps its entry (rows N83_leaf and N83_base).
		underBase := ""
		if p.WorktreeRoot == "" {
			if rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath)); err == nil && filepath.IsLocal(rel) {
				underBase = filepath.Join(finalDirPath(repo), rel)
			}
		}
		sp := newWorktreePathSet(path, p.WorktreePath, underBase)
		goneLocked := "refusing to remove worktree: " + p.WorktreePath + " is gone but its " +
			"registration is locked (git worktree lock); unlock it to remove the registration and branch"
		// D22: a locked entry in the `.git` folder of baseRepo is refused too, with and
		// without worktreeRoot. 89cb6289 answers success there and deletes nothing (row
		// p6f on Linux, macOS and Windows VMs, row q4 with worktreeRoot on Linux).
		if msg := baseRepoLockRefusal(repo, commonDir, "", path, p.WorktreePath, sp, goneLocked); msg != "" {
			return refuse(msg)
		}
		// A worktrees directory that cannot be read does not stop a gone remove. The
		// reference answers success there (Linux VM, row K13 with worktreeRoot and a
		// gone target). Without worktreeRoot no row measures it.
		locked, _, matches := worktreeLockedByPath(commonDir, sp)
		if locked {
			return refuse(goneLocked)
		}
		if matches == 0 {
			probe.pairIfWorktreesDir(commonDir)
		}
		dropWorktreeEntryByPath(commonDir, sp)
	}
	// The branch step runs on this path too (rows R17a and R17b). Without a
	// repository its for-each-ref fails, and the branch counts as kept (rows R18 and
	// R18b).
	kept := runBranchStep(repo, p.BranchName).kept()
	return okResult(req.ID, worktreeRemoveResult{Success: true, BranchKept: kept})
}

// worktreeRemoveHomeRefusal is the home guard of git.worktree_remove (D2). It refuses a
// worktreePath that is the home directory or holds it, before anything is deleted.
// worktreePath is `~`-expanded first (expandpath.go), and at 5db5e4a the reference
// answered {"success":true} to "worktreePath":"~" and deleted the home directory. The
// containment checks refuse that input first today, so this guard is defense in depth.
// It is the same defect that destroyed the maintainer's home directory through
// files.extract_tar on 2026-08-02.
func worktreeRemoveHomeRefusal(worktreePath string) string {
	if wipesHomeDir(worktreePath) {
		return fmt.Sprintf("worktreePath must not be or contain the home directory: %q", worktreePath)
	}
	return ""
}

// pruneGitDir is the git directory whose `worktrees` holds the entries that a remove
// deletes: the repository git directory the trust check pinned for repo. For a main
// repository that is <repo>/.git. For a linked worktree used as baseRepo it is the main
// repository's git directory, since the worktree's own `.git` is a file. A look under
// <worktree>/.git/worktrees finds nothing, because that directory never exists.
// Measured side by side against f6010b97 on a Linux VM (C10, remove from a linked
// worktree whose commondir starts with white space: the reference deletes the entry). When the daemon's own environment sets
// GIT_COMMON_DIR, or the check pinned nothing, the old <repo>/.git stays. A removal
// without worktreeRoot then takes the answer of its rev-parse call (registrationGitDir).
func pruneGitDir(repo string) string {
	if !daemonCommonDirSet() {
		if t := gitDirTrustFor(repo); t.verdict == gitDirTrusted && t.pinCommonDir != "" {
			return t.pinCommonDir
		}
	}
	return filepath.Join(repo, ".git")
}

// lockCheckRefusal is git.worktree_remove's answer when baseRepo's registrations cannot
// be examined. The path is echoed as given, unquoted and not cut.
func lockCheckRefusal(worktreePath string) string {
	return "failed to remove worktree: could not check whether " + worktreePath +
		" is locked (its registrations could not be examined); retry"
}

// unreadableRepoLockCheckRefusal is the lock-check refusal of git.worktree_remove that
// names the repository (docs/PROTOCOL.md).
func unreadableRepoLockCheckRefusal(worktreePath, repo string) string {
	return "failed to remove worktree: could not check whether " + worktreePath +
		" is locked (the repository at " + repo + " could not be read); retry"
}

// worktreeAdminDir reads a linked worktree's `.git` pointer file
// ("gitdir: <mainGitDir>/worktrees/<name>") and returns that admin directory, or
// "" when worktreePath is not a linked worktree. The rollback of git.worktree_create
// uses it to find the entry of the worktree it created. A `.git` that is not a
// regular file, a FIFO for example, gives "" at once (readGitPlainFile).
func worktreeAdminDir(worktreePath string) string {
	b, err := readGitPlainFile(filepath.Join(worktreePath, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimSpace(line[len(prefix):])
}

// worktreeAdminBelongsTo reports whether adminDir's `gitdir` back-pointer resolves to
// worktreePath's own `.git` file. git writes `<adminDir>/gitdir` holding the absolute
// path of the linked worktree's `.git`, so a genuine registration points back at the
// worktree. Its only caller is the rollback of git.worktree_create, which deletes the
// entry of the worktree it created. Without this, a forged or stale
// `<worktreePath>/.git` that names a SIBLING worktree's admin dir (still under
// `.git/worktrees`, so the containment check alone passes) would let the rollback
// take that unrelated registration. git.worktree_remove verifies its entry in
// worktreeremove.go instead.
//
// A relative record counts from adminDir. git writes one with
// worktree.useRelativePaths (cell Z9b, macOS VM). readErr is the error of a record
// that cannot be read, and nil otherwise.
func worktreeAdminBelongsTo(adminDir, worktreePath string) (belongs bool, readErr error) {
	b, err := readGitPlainFile(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		return false, err
	}
	record := strings.TrimSpace(string(b))
	if !filepath.IsAbs(record) {
		record = filepath.Join(adminDir, record)
	}
	// sameCanonicalPath, not ==: on Windows git writes this record with forward slashes,
	// and once the worktree is deleted neither side can be resolved, so the two
	// spellings differ only in slash direction. Measured against f6010b97 on a
	// Windows 11 VM, where the reference deletes the entry.
	return sameCanonicalPath(canonicalPathOfGone(record),
		canonicalPathOfGone(filepath.Join(worktreePath, ".git"))), nil
}

// canonicalPathOfGone is canonicalPath for a path that possibly no longer exists. It resolves
// the nearest existing ancestor and joins the missing rest back on. After a delete
// the worktree is gone, but its parent still resolves. A remove named through a
// symlinked root (macOS /tmp -> /private/tmp) then still matches the path in the
// entry, where git records the resolved path. Measured against f6010b97 on a macOS
// VM, where the reference deletes the entry.
func canonicalPathOfGone(p string) string {
	p = filepath.Clean(p)
	var missing []string
	for cur := p; ; {
		if _, err := os.Lstat(cur); err == nil {
			return filepath.Join(append([]string{canonicalPath(cur)}, missing...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
}
