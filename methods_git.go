package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// below the 75 s probed, so a wedged git — an index/config lock, a credential
// prompt, a stalled network or filesystem, a hung checkout hook — leaves the
// request goroutine waiting without bound there, and now here too.
//
// ⚠️ Everything below describes what an operator opts INTO, measured against the
// retracted 60 s default. It shipped always-on and that failed rule 3: a
// wall-clock deadline cannot separate a hostile git from an honestly slow one, so
// a large repo on a loaded host or a cold network filesystem trips it too — and
// unlike the ldd probe, the fallback here IS observable — on git.status and
// git.list_branches the killed process surfaces as -32603 carrying
// "signal: killed" (docs/PROTOCOL.md -> git.list_branches; docs/DIVERGENCES.md D5).
// Normal git ops finish well under any sane bound, but "well under" is a statement
// about typical hosts, not a property of the predicate, and an honest 61 s git has
// never been measured on either binary. This is D5.
//
// ⚠️ A TIMEOUT IS NOT "the same as any other git failure". That is what this
// comment used to say, and it was true only while failure meant NOTHING HAPPENED.
// gitWorktreeRemove now treats a NON-LOCKED git failure as permission to delete the
// worktree itself, so a caller that ACTS on failure must distinguish our deadline from
// git's verdict — otherwise our own safety cap authorises a destructive act the
// reference cannot perform, since it showed no deadline at or below 75 s and
// simply blocks (measured on one method; see D5 for the scope).
//
// ⚠️ The flip does not retire that distinction, it narrows when it can fire. With
// the bound off, gitCtx hands back a context that never expires, so gitDeadline's
// timedOut is false by construction and the destructive fallback is reached only
// on git's own verdict. Opt the bound back in and the hazard returns exactly as
// measured. Do NOT collapse gitDeadline into git() while "the default is off" —
// it guards a recursive delete.
//
//	read-only callers (isRepo, gitInfo, gitStatus, …)  ok=false is enough
//	callers with a side effect (gitWorktreeRemove)     MUST use gitDeadline
//
// The reply shape is NOT unchanged either: the timeout branch answers
// {"success":false,"error":"git worktree remove timed out after …"}, a frame the
// reference never emits.
//
// ⚠️ This used to add "no OTHER frame moves because of the deadline". That is
// false: the deadline is the shared gitTimeout, applied independently at all
// three helpers (git, gitStdoutErr, gitDeadline), so a kill can surface through
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
// huge value, so exec.CommandContext has nothing to fire and gitDeadline's
// ctx.Err() is nil by construction — a wedged git then blocks, as the reference
// did at every duration probed (no deadline at or below 75 s, measured on
// git.worktree_remove only; above that, unmeasured on both binaries).
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
//	                     both of which a warning-prefixed string fails safely;
//	                     show-ref --verify --quiet and update-ref -d (the branch
//	                     delete), which discard their output entirely
//	echoes it verbatim   --show-toplevel → root/repo, branch --show-current →
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

// gitDeadline is git() plus one extra bit: whether OUR deadline killed the
// process, as opposed to git exiting non-zero on its own.
//
// It exists for exactly one caller. gitWorktreeRemove treats a NON-LOCKED git failure
// as permission to delete worktreePath itself, and gitTimeout is a CLAUSTRUM-ONLY
// divergence — the reference showed no deadline at or below 75 s and simply
// blocks. So
// without this distinction a wedged git turns a claustrum safety measure into a
// recursive delete the reference would never perform. Measured before the fix,
// with a stub git that sleeps and gitTimeout shrunk: the directory was deleted
// and the reply was {"success":true}.
func gitDeadline(dir string, args ...string) (out string, ok bool, timedOut bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	out, ok = gitContext(ctx, dir, args...)
	return out, ok, ctx.Err() != nil
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

func isRepo(dir string) bool {
	out, ok := hardenedGit(dir, false, "rev-parse", "--is-inside-work-tree")
	return ok && out == "true"
}

func gitInfo(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// The excludesFile the reference reads at git.info is now resolved once (cached)
	// and applied to every git op via hardenedArgs/userExcludesFile, rather than
	// probed here and discarded.
	//
	// The git-directory trust check runs first, on path (gitdirtrust.go). Measured side
	// by side against f6010b97 on Linux, macOS and Windows VMs.
	switch t := requestGitDirTrust(p.Path, true); t.verdict {
	case gitDirRefused:
		return errResult(req.ID, codeInternal, t.refusal)
	case gitDirNoRepo:
		return okResult(req.ID, notRepoResult{})
	}
	// If the repo's config cannot be enumerated (e.g. a corrupt .git/config), the
	// reference cannot pin its config-defined hooks off and refuses with -32603
	// rather than running git. Measured against 7d193f89 on an ephemeral VM.
	if msg, bad := hostileConfigRefusal(p.Path); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	// isRepo now requires BOTH a git dir and a work tree, under the light hardening
	// profile: a bare repo has a git dir but no work tree, so --show-toplevel fails
	// and it reports the bare notRepoResult (matching the old --is-inside-work-tree
	// verdict via a different pair of commands).
	if _, ok := hardenedGit(p.Path, false, "rev-parse", "--git-dir"); !ok {
		return okResult(req.ID, notRepoResult{})
	}
	top, ok := hardenedGit(p.Path, false, "rev-parse", "--show-toplevel")
	if !ok {
		return okResult(req.ID, notRepoResult{})
	}
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
		Repo:          filepath.Base(top),
		Branch:        branch,
		Root:          top,
		RepoSlug:      slug,
		DefaultBranch: defBranch,
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
func gitDefaultBranch(dir string) string {
	ref, ok := hardenedGit(dir, false, "symbolic-ref", "refs/remotes/origin/HEAD")
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
	// 7d193f89 rebuilt git.status around session worktrees. baseRepo is now
	// required, and status is reported ONLY when path is a linked worktree that
	// belongs to baseRepo. A plain path, a plain subdir, a nested repo, the repo
	// root itself, a worktree of another repo, or the right worktree named against
	// the wrong baseRepo all answer the bare isRepo:false shape — confirmed
	// byte-for-byte against the reference. The check comes before status runs.
	if p.BaseRepo == "" {
		return errResult(req.ID, codeInvalidParam, "baseRepo is required")
	}
	// The git-directory trust check runs on baseRepo, not on path (gitdirtrust.go).
	// Measured side by side against f6010b97 on Linux, macOS and Windows VMs.
	switch t := requestGitDirTrust(p.BaseRepo, false); t.verdict {
	case gitDirRefused:
		return errResult(req.ID, codeInternal, t.refusal)
	case gitDirNoRepo:
		return okResult(req.ID, gitStatusResult{})
	}
	// A repo whose config cannot be enumerated is refused with -32603 before status
	// runs (7d193f89). The enumeration is on baseRepo. With the GIT_COMMON_DIR pin of
	// the trust check, git names the config by its absolute path. In the create and
	// remove frames f6010b97 names the absolute path too, and 90fca6e6 names
	// ".git/config" (Linux VM, K06). This frame is not measured on f6010b97.
	if msg, bad := hostileConfigRefusal(p.BaseRepo); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	gitDir, commonDir, ok := gitStatusWorktreeOf(p.Path, p.BaseRepo)
	if !ok {
		// The reference returns the full status shape (clean:false), not the
		// bare notRepoResult that git.info uses.
		return okResult(req.ID, gitStatusResult{})
	}
	// 7d193f89 runs status under the heavy hardening profile with
	// `--untracked-files=all --ignore-submodules=all`. `--untracked-files=all` is
	// wire-visible: an untracked file inside an untracked directory is listed
	// individually (`?? sub/u.txt`) rather than as the directory (`?? sub/`).
	//
	// The reference builds status in an ISOLATED temp gitdir so the caller's index is
	// never refreshed: a fresh GIT_DIR with GIT_COMMON_DIR pointing at the shared
	// repo, and --work-tree at the worktree. hardenedGitStatus reproduces that
	// assembly (reconstructed from the reference's runtime git argv+env,
	// scratch/probe/gitargv). It is byte-identical on Linux and macOS. On Windows it
	// is NOT: the reference's own git.status of a linked worktree errors -32603
	// "exit status 128" there (measured), while claustrum returns the status. That
	// is intentional divergence D16 (claustrum more correct). The exact
	// reason the reference fails on Windows is not yet pinned; an earlier hardcoded-/tmp
	// hypothesis is contradicted (the reference respects $TMPDIR). See docs/DIVERGENCES.md
	// D16. A path with no work tree still exits 128, and the reference propagates the bare
	// Go error string, not git's "fatal:" output.
	//
	// --attr-source=<empty-tree> makes git ignore the repo's in-repo .gitattributes,
	// matching 4534d86: without it a .gitattributes clean filter runs during status
	// and flips a file's modified-ness (wire-visible) plus executes its command. It is
	// a top-level option before the subcommand, and a no-op on a repo with no
	// attribute rules (so ordinary status stays byte-identical).
	statusArgs := append(attrSourceArgs(), "status", "--porcelain",
		"--untracked-files=all", "--ignore-submodules=all")
	out, err := hardenedGitStatus(p.Path, gitDir, commonDir, statusArgs...)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if out == "" {
		return okResult(req.ID, gitStatusResult{IsRepo: true, Clean: true})
	}
	var changes []string
	// 7d193f89 rebuilt git.status to pass the porcelain through verbatim: every
	// line keeps its leading space, the FIRST included. 5db5e4a trimmed the whole
	// blob before splitting, so its first line lost its leading space, and claustrum
	// reproduced that. Re-measured against the reference daemons 7d193f89 AND 4534d86:
	// a worktree whose first two entries are " M a1" and " M a2" comes back as
	// [" M a1"," M a2"], NOT ["M a1"," M a2"] (5db5e4a). So split on the trailing
	// newline only — never TrimSpace the blob, which eats the first line's leading
	// space. Evidence: scratch/probe/attrsrc (git.status vs each reference build).
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		// Past the first line, pass porcelain through verbatim apart from the
		// line ending: the XY status column is positional, so the leading space
		// of an unstaged-only change (" M f") is data, not padding. Trimming it
		// made staged ("M  f") and unstaged (" M f") differ only in space count.
		//
		// Kept as a single guarded append rather than an `if ... { continue }`:
		// gitStdoutErr already strips the trailing newline and an empty `out`
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

// gitStatusWorktreeOf reports whether path is a linked git worktree whose main
// repository is baseRepo — the gate 7d193f89's git.status applies before it will
// run status. One `git rev-parse` at path yields the three facts that decide it:
// path must be the worktree's own top level (so a subdir inside a worktree is
// rejected), it must be a LINKED worktree (git-dir differs from the common dir,
// so the main checkout is rejected), and the common dir's parent must be baseRepo
// (so a worktree of another repository is rejected). Reproduces the reference's
// isRepo verdict on all six probed shapes; git failing at path (absent, not a
// repo) falls through to false.
func gitStatusWorktreeOf(path, baseRepo string) (gitDir, commonDir string, ok bool) {
	// git canonicalizes the paths it reports (--show-toplevel / --git-common-dir
	// resolve symlinks — macOS /tmp -> /private/tmp — and expand Windows 8.3 short
	// names), so canonicalize our own operands the same way before comparing.
	// Without this a valid worktree under a symlinked or short-named directory
	// never matches git's output and status wrongly answers isRepo:false. No-op on
	// Linux paths with no symlinks, so the frame battery stays byte-identical.
	path = canonicalPath(path)
	baseRepo = canonicalPath(baseRepo)
	out, o := hardenedGit(path, false, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir")
	if !o {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return "", "", false
	}
	top, gd, cd := lines[0], lines[1], lines[2]
	if samePath(top, path) && !samePath(gd, cd) && samePath(filepath.Dir(cd), baseRepo) {
		return gd, cd, true
	}
	return "", "", false
}

// canonicalPath resolves p to the spelling git reports — symlinks resolved and
// (on Windows) 8.3 short names expanded. Defined per-OS in pathcanon_{unix,windows}.go.

// samePath compares two paths after lexical cleaning. Callers that compare against
// git's output canonicalize their operands first (see gitStatusWorktreeOf).
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func gitListBranches(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// 7d193f89 refuses a baseRepo that sits inside a managed worktrees tree before
	// listing anything, answering the bare isRepo:false shape (branches:[]). Measured
	// against 7d193f89 on an ephemeral VM.
	if baseRepoUnderManagedWorktrees(p.repoDir()) {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// The git-directory trust check runs on path (gitdirtrust.go). Measured side by
	// side against f6010b97 on Linux, macOS and Windows VMs.
	switch t := requestGitDirTrust(p.Path, true); t.verdict {
	case gitDirRefused:
		return errResult(req.ID, codeInternal, t.refusal)
	case gitDirNoRepo:
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// A repo whose config cannot be enumerated is refused with -32603 (7d193f89),
	// measured on an ephemeral VM.
	if msg, bad := hostileConfigRefusal(p.Path); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	// 7d193f89 runs this under the light hardening profile with the config
	// precursor; the repo gate is `rev-parse --git-dir` (true for a bare repo and
	// from inside a `.git` directory, unlike git.info's --is-inside-work-tree). The
	// reference returns the full branches shape (branches:[]) on a non-repo, not
	// git.info's bare notRepoResult.
	if _, ok := hardenedGit(p.Path, false, "rev-parse", "--git-dir"); !ok {
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
	// never from inside another session's worktree tree.
	if baseRepoUnderManagedWorktrees(repo) {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     managedWorktreesRefusal,
			ErrorCode: "nested_base_repo",
		})
	}
	// The git-directory trust check runs on baseRepo before anything is created
	// (gitdirtrust.go). A refusal creates no worktree directory, no entry and no
	// branch. When the daemon's own environment carries GIT_COMMON_DIR, the refusal
	// text is wrapped as a failed add. Measured side by side against f6010b97 on Linux,
	// macOS and Windows VMs.
	switch t := requestGitDirTrust(repo, false); t.verdict {
	case gitDirRefused:
		msg := t.refusal
		if daemonCommonDirSet() {
			msg = gitDirAddWrap + msg
		}
		return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	case gitDirNoRepo:
		return okResult(req.ID, worktreeResult{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
	}
	// A repo whose config cannot be enumerated is refused before git runs — the
	// reference surfaces the same "config-defined hooks could not be pinned off"
	// detail under errorCode worktree_add_failed. Measured on an ephemeral VM.
	if msg, bad := hostileConfigRefusal(repo); bad {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     msg,
			ErrorCode: "worktree_add_failed",
		})
	}
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
	// With a worktreeRoot the session folder is created OUTSIDE the repo, so the
	// in-repo containment is replaced by the external-location checks: absolute /
	// no-".." spelling of the root and path, 2-level containment, ownership /
	// writability of the root, and the <directory> must start out empty (or already
	// be a managed worktree directory). Order measured against 7d193f89; an empty
	// worktreePath is judged here as a relative path, not the in-repo mkdir failure.
	if p.WorktreeRoot != "" {
		root := filepath.Clean(p.WorktreeRoot)
		if msg := externalWorktreeUnsupportedRefusal(p.WorktreeRoot, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalContainmentRefusal(p.WorktreeRoot, p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeRootShareRefusal(root); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
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
		if msg := worktreePathRefusal(repo, p.WorktreePath, "create"); msg != "" {
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
		// No probe sent an in-repo path with a slash to this refusal.
		existing := p.WorktreePath
		if p.WorktreeRoot != "" {
			existing = filepath.Clean(p.WorktreePath)
		}
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("refusing to create worktree: %s already exists, and a new worktree is only ever created in a fresh directory", existing),
			ErrorCode: "unsafe_path",
		})
	}
	// The target is confirmed missing above, so any worktree registration still
	// naming it is stale (its session folder was deleted out from under git). Drop
	// just that registration so the add below recreates cleanly, the way 7d193f89
	// does — where claustrum otherwise failed "missing but already registered".
	dropStaleWorktreeRegistration(repo, p.WorktreePath)
	// `git worktree add` does not create leading directories, so the reference
	// makes the parent before adding — this is what lets a nested session path
	// such as <repo>/.claude/worktrees/<id> succeed on a fresh repo. The parent comes
	// from the cleaned path. With a trailing slash, filepath.Dir of the raw path is
	// the leaf itself, and the leaf mkdir below then failed with "file exists". The
	// references create that worktree and echo the path as sent. Measured on Linux
	// and macOS VMs. Every frame below therefore keeps the raw p.WorktreePath.
	leafParent := filepath.Dir(filepath.Clean(p.WorktreePath))
	if err := os.MkdirAll(leafParent, 0o755); err != nil {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("failed to create parent directory: %v", err),
			ErrorCode: "mkdir_failed",
		})
	}
	// For an external worktreeRoot, tag the <directory> level as holding managed
	// session worktrees before git runs. This is the same marker
	// baseRepoUnderManagedWorktrees looks for, so a later create whose baseRepo sits
	// under here is refused as a nested repo.
	if p.WorktreeRoot != "" {
		_ = ensureManagedWorktreesMarker(leafParent)
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
	gitDir := repoGitDir(repo)
	if d, ok := hardenedGit(repo, true, "rev-parse", "--absolute-git-dir"); ok && d != "" {
		gitDir = filepath.Clean(filepath.FromSlash(d))
	}
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
		// then ends up on that new branch, and the rollbacks below delete it. If the
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
	// The add succeeded. If the caller's deadline expired during it, the request
	// answers timeout "before the checkout started" and rolls back. errorCode
	// "timeout" is reserved for the caller's own deadline. The reference has no D5.
	// Every rollback below appends an undo text when one of its steps fails.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired before the checkout started)", p.TimeoutMs)
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     msg + undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint),
			ErrorCode: "timeout",
		})
	}
	// Second half of the two-step: the read-tree fills the working tree from the new
	// branch. It runs in the leaf with --git-dir set to the git dir of baseRepo and its
	// index in a new temporary directory (GIT_INDEX_FILE). After git exits 0, that
	// index becomes the new worktree's own index. Measured against f6010b97 and
	// 90fca6e6 on a Windows VM. A checkout that fails also fails the request. See the
	// last arm of the switch.
	if adminDir := worktreeAdminDir(p.WorktreePath); adminDir != "" {
		rtStderr, rtDrained, rtErr := runWorktreeCheckout(callerCtx, p.WorktreePath, gitDir, adminDir, checkoutRev, commonDirPinEnv(repo))
		switch {
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
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     msg + undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint),
				ErrorCode: "timeout",
			})
		case rtErr != nil:
			// The checkout failed on its own (for example a tree object it cannot
			// read). The request fails with git's text, and the rollback removes the
			// new directory, its registration and the branch this call created. The
			// frame matches 90fca6e6 byte for byte, apart from git's graft-file
			// deprecation hint: lines. f6010b97 and claustrum both set GIT_GRAFT_FILE,
			// so where git prints the hint, their text starts with it. In attach mode
			// createdBranch is "", so the attached branch is kept, as measured against
			// f6010b97.
			msg := "git worktree add failed (checkout): " + worktreeGitText(rtStderr, rtErr)
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     msg + undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint),
				ErrorCode: "worktree_add_failed",
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
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     msg + undoFailedCheckout(repo, p.WorktreePath, createdBranch, checkpoint),
			ErrorCode: "timeout",
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
	// 7d193f89 refuses a baseRepo inside a managed worktrees tree as an invalid
	// trust root (no errorCode on remove). Measured against 7d193f89 on an ephemeral
	// VM. Comes before the containment/removal below.
	if baseRepoUnderManagedWorktrees(repo) {
		return okResult(req.ID, worktreeRemoveResult{
			Success: false,
			Error:   managedWorktreesRefusal,
		})
	}
	// worktree-location containment, added by the reference in 7d193f89 and matched
	// here byte-for-byte (verb "remove", no errorCode field). It gates the
	// os.RemoveAll fallback below: only a path strictly inside the repo survives to
	// reach it, so a "~"-expanded home path is refused here — with the reference's
	// wording — before the wipesHomeDir guard is consulted. Empty is failed as a
	// non-directory, not as a relative path (measured against 7d193f89).
	if p.WorktreePath == "" {
		return okResult(req.ID, worktreeRemoveResult{
			Success: false,
			Error:   fmt.Sprintf("failed to remove worktree: %q does not name a directory", p.WorktreePath),
		})
	}
	// With a worktreeRoot the worktree lives OUTSIDE the repo, so the in-repo
	// containment is replaced by the external checks: 2-level containment, then a
	// full registration verify (externalWorktreeVerify) — a path that is not a
	// genuine registered worktree of baseRepo is refused and LEFT IN PLACE (or, when
	// baseRepo's worktrees dir is missing, reported as a transient "could not
	// verify … retry"), where an in-repo remove would fall back to a recursive
	// delete. Measured against 7d193f89 on an ephemeral VM.
	if p.WorktreeRoot != "" {
		if msg := externalWorktreeUnsupportedRefusal(p.WorktreeRoot, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		if msg := worktreeExternalContainmentRefusal(p.WorktreeRoot, p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		// The work-tree check comes before the dir-symlink check. A base that the check
		// refuses gets the work-tree refusal even when the directory level is a symlink.
		// Measured side by side against f6010b97 on a Linux VM (rows K10, K11, K14 and
		// K16 with a symlinked directory level).
		if msg := externalWorkTreeRefusal(repo); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		// A worktree registry that exists but cannot be read answers the lock-check text.
		// A worktree that is already gone skips this, as on f6010b97 (row K13, Linux VM).
		if externalRegistrationsUnreadable(repo, p.WorktreePath) {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: lockCheckRefusal(p.WorktreePath)})
		}
		if reason, transient := externalWorktreeVerify(repo, p.WorktreePath); reason != "" {
			wp := filepath.Clean(p.WorktreePath)
			if transient {
				return okResult(req.ID, worktreeRemoveResult{Success: false,
					Error: fmt.Sprintf("failed to remove worktree: could not verify that %s is a "+
						"worktree of %s (%s); retry", wp, repo, reason)})
			}
			return okResult(req.ID, worktreeRemoveResult{Success: false,
				Error: fmt.Sprintf("refusing to remove worktree: %s is not a worktree of %s (%s), "+
					"so it is left in place; remove it by hand if it is a leftover", wp, repo, reason)})
		}
	} else {
		if msg := worktreePathRefusal(repo, p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		// A symlinked component under .claude/worktrees is refused before the removal —
		// this is what keeps the os.RemoveAll fallback below from following a planted
		// link out of the repo (matches 7d193f89; no errorCode on remove).
		if msg := worktreeSymlinkRefusal(repo, p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
	}
	// ⚠️ INTENTIONAL DIVERGENCE, and the only one on this method — refused before
	// git is run at all, so neither the removal nor the branch delete happens.
	//
	// Everything below documents that a failed `git worktree remove` hands
	// worktreePath to os.RemoveAll, and that this is measured PARITY rather than a
	// claustrum invention. What that measurement did not cover is that
	// worktreePath is `~`-expanded first (expandpath.go), so `"worktreePath":"~"`
	// makes that line os.RemoveAll($HOME) — git fails on a home directory, which
	// is not a worktree, so the fallback is the arm that runs.
	//
	// That gap is now closed, not inferred: probed 2026-08-06 at 5db5e4a on an
	// ephemeral VM with HOME pinned to a fixture, `"worktreePath":"~"` answers
	// {"success":true} and the home directory is GONE. Two instrument checks ran
	// first, so a null result could not be mistaken for a refusal — files.validate
	// on ~/KEEP.txt returned valid:true, and an ordinary non-worktree directory
	// was deleted as the table below predicts.
	//
	// It is the same defect that destroyed the maintainer's home directory through
	// files.extract_tar on 2026-08-02; only the method differs.
	//
	// The comment below justifies the parity by "the caller did name the path and
	// ask for it to be removed". That is the right test, and a home directory
	// fails it: the caller named "~", and no caller asking to remove a worktree
	// means "delete my home directory". Matching the reference is this project's
	// hard rule for FRAMES; it was never a commitment to reproduce an
	// unrecoverable data loss the reference reaches by accident.
	// The empty check is not defensive noise — without it this guard CHANGED a
	// reference-reachable frame. gitWorktreeRemove has no required-param check, so
	// an omitted worktreePath arrives here as "", and filepath.Abs("") resolves to
	// the daemon's working directory: on a daemon started in the user's home (what
	// an SSH-launched one inherits) that equals home and the guard fired. It would
	// have refused an input where os.RemoveAll("") is a documented no-op returning
	// nil — nothing to protect. Worse, the frame varied with the daemon's cwd, which
	// no golden can observe because the harness runs from a temp dir. Raised in
	// review on PR 232; pinned by TestWorktreeRemoveEmptyPathIsNotRefused.
	if p.WorktreePath != "" && wipesHomeDir(p.WorktreePath) {
		return okResult(req.ID, worktreeRemoveResult{
			Success: false,
			Error:   fmt.Sprintf("worktreePath must not be or contain the home directory: %q", p.WorktreePath),
		})
	}
	// 7d193f89 prunes the worktree registration on every removal. `git worktree
	// remove` already drops it when it succeeds, but the manual-cleanup fallback
	// below (a locked worktree, exit 128) leaves it, so a re-create at the same
	// path would then fail "already registered" where the reference re-creates
	// cleanly. Capture the admin dir now, before the removal deletes the pointer,
	// and prune it after a successful removal.
	// If the repo's config cannot be enumerated, the reference cannot examine the
	// worktree's registrations to tell whether it is locked, so it refuses with its
	// own message (a corrupt config, not the read methods' -32603). Measured against
	// 7d193f89 on an ephemeral VM.
	//
	// The same refusal answers a baseRepo that is an existing directory in which git
	// finds no repository: a plain directory, a repository whose git directory is
	// broken, or a daemon GIT_DIR that names nothing usable. There are no
	// registrations to examine, so nothing is deleted. Before, git's own remove
	// failed there and the manual-cleanup fallback below deleted worktreePath with
	// {"success":true}. Measured against f6010b97 on Linux, macOS and Windows VMs,
	// and 90fca6e6 answers the same. A baseRepo that does not exist at all is left
	// to the paths below, as before. With worktreeRoot this refusal does not apply.
	// externalWorkTreeRefusal answers those inputs with its own texts instead.
	// Without worktreeRoot, a baseRepo the daemon may open but not search (mode
	// 0600), or cannot open at all, answers with the error from its look at
	// .claude, and that error is the whole reply. With worktreeRoot there is no
	// such look: a search-only baseRepo (mode 0100) still removes. Measured side
	// by side against f6010b97 on Linux, and the reference's answers also on
	// macOS.
	if p.WorktreeRoot == "" {
		if err := statInsideDir(repo, ".claude"); err != nil {
			return okResult(req.ID, worktreeRemoveResult{
				Success: false,
				Error:   "failed to remove worktree: " + err.Error(),
			})
		}
		// The same refusal answers a baseRepo that fails the git-directory trust check
		// (gitdirtrust.go), with a refused git directory or with no repository. Nothing
		// is deleted then. The worktree directory, its entry and its branch all stay.
		// The check looks at baseRepo only. Damage to the entry of the worktree being
		// removed therefore does not stop the removal. Measured side by side against
		// f6010b97 on Linux, macOS and Windows VMs. With worktreeRoot,
		// externalWorkTreeRefusal answered these inputs above.
		if v := requestGitDirTrust(repo, false).verdict; v == gitDirRefused || v == gitDirNoRepo {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: lockCheckRefusal(p.WorktreePath)})
		}
		if _, bad := hostileConfigRefusal(repo); bad || noRepositoryAt(repo) {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: lockCheckRefusal(p.WorktreePath)})
		}
	}
	adminDir := worktreeAdminDir(p.WorktreePath)
	// 7d193f89 runs no `git worktree remove` at all (git-argv trace), yet it refuses a
	// locked worktree. Check the `locked` marker (<admin>/locked) here, before the
	// destructive `git worktree remove --force` + os.RemoveAll fallback below, so a
	// non-C locale (where the "cannot remove a locked working tree" stderr match
	// misses) cannot delete a locked worktree the reference refuses. Same fixed
	// message as the stderr branch.
	if adminDir != "" && fileExists(filepath.Join(adminDir, "locked")) {
		return okResult(req.ID, worktreeRemoveResult{
			Success: false,
			Error: fmt.Sprintf("refusing to remove worktree: %s is locked "+
				"(git worktree lock); unlock it to remove it", p.WorktreePath),
		})
	}
	// When `git worktree remove --force` fails for a NON-LOCKED reason, the reference
	// removes worktreePath itself and still answers {"success":true}; it reports
	// failure only when that manual cleanup ALSO fails. A LOCKED worktree is the
	// exception — 7d193f89 refuses it (the branch above) rather than deleting it.
	//
	// Measured: at 5db5e4a, checking the DIRECTORY after each git failure; at
	// 7d193f89, re-probing the locked case on an ephemeral VM (both binaries):
	//
	//	fixture      git fails because             reference at 7d193f89
	//	locked       the worktree is locked        REFUSED, dir left in place
	//	plain-dir    the path was never a worktree DELETED (fallback)
	//
	// A baseRepo that is not a repository used to reach the fallback too (measured
	// at 5db5e4a). 90fca6e6 and f6010b97 refuse it instead, with the lock-check
	// text above, and claustrum now matches.
	//
	// So for a NON-LOCKED failure `git.worktree_remove` is a recursive delete of the
	// caller-supplied worktreePath, and that is parity, not a claustrum invention.
	// Documented in PROTOCOL.md because it will otherwise read as a bug. The caller
	// did name the path and ask for it to be removed, which is why this is not
	// treated like the -cli-version escape in PR 196 — there the deletion reached a
	// path the caller never named. (Pre-7d193f89 the reference deleted the locked
	// worktree too and did NOT prune, so the repo kept listing it; 7d193f89 both
	// refuses the locked case and prunes the registration on a completed removal.)
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
		// 7d193f89 REFUSES a LOCKED worktree rather than falling back to a delete:
		// `git worktree remove --force` fails with "cannot remove a locked working
		// tree", and the reference answers success:false with its own fixed message
		// and leaves the directory in place. Only the
		// OTHER git-failure modes (an ordinary non-worktree directory) reach the
		// os.RemoveAll fallback below. Before 7d193f89 the reference DELETED a locked
		// worktree here — a wire change, measured against 7d193f89 on an ephemeral VM
		// (the frame battery never removes a locked worktree, so it did not catch it).
		// Anchor on git's FULL phrase, not the bare "locked working tree": git echoes
		// the caller's path in the non-locked failure ("'<path>' is not a working
		// tree"), so a worktreePath literally containing "locked working tree" would
		// substring-match and be wrongly refused (leaving a directory the reference
		// would delete). The full phrase cannot appear in that path-echo. Raised by
		// wire-byte review.
		if strings.Contains(out, "cannot remove a locked working tree") {
			return okResult(req.ID, worktreeRemoveResult{
				Success: false,
				Error: fmt.Sprintf("refusing to remove worktree: %s is locked "+
					"(git worktree lock); unlock it to remove it", p.WorktreePath),
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
	// The reference also deletes the branch when branchName is given, via a raw ref
	// delete (update-ref --no-deref -d), NOT `git branch -D`. Both remove the ref and
	// its reflog forcefully — an unmerged branch goes too — but `git branch -D` ALSO
	// drops the branch's config section ([branch "<name>"]), where the reference
	// leaves it (measured against 4534d86, scratch/probe/gitmut: after remove the ref
	// and reflog are gone on both, but the config section survives on the reference
	// and claustrum's `branch -D` deleted it). update-ref matches on all three and is
	// the same primitive undoFailedCheckout already uses. Best-effort: a branch that
	// does not exist still answers {"success":true}, so a failed delete is not surfaced.
	if p.BranchName != "" {
		hardenedGit(repo, false, "update-ref", "--no-deref", "-d", "refs/heads/"+p.BranchName)
	}
	// Prune the registration (see above). This is the fix for the fallback path. It
	// does nothing when `git worktree remove` already dropped the registration. For a
	// baseRepo whose .git is a directory, the only valid admin dir is
	// `<repo>/.git/worktrees/<name>`. The pruned path must therefore resolve strictly
	// inside `<repo>/.git/worktrees` before the delete. Inside the repo is not enough.
	// worktreeAdminDir returns the contents of the `.git` pointer as they are, and an
	// attacker can write them. A stale or forged pointer can name `<repo>/src` or
	// `<repo>/.git/objects`, or a `..` variant that Clean or EvalSymlinks brings back
	// under the repo. Without the check, the prune then deletes unrelated repo data.
	// The check refuses every such target and still prunes a real registration.
	// Off-wire: the result is discarded, and the reply is success:true either way.
	// Raised by review on PR 286.
	if adminDir != "" && worktreeAdminBelongsTo(adminDir, p.WorktreePath) &&
		pathStrictlyUnder(canonicalPath(adminDir), canonicalPath(filepath.Join(pruneGitDir(repo), "worktrees"))) {
		_ = os.RemoveAll(adminDir)
	}
	return okResult(req.ID, worktreeRemoveResult{Success: true})
}

// pruneGitDir is the git directory whose `worktrees` holds the entries that a remove
// prunes: the repository git directory the trust check pinned for repo. For a main
// repository that is <repo>/.git. For a linked worktree used as baseRepo it is the main
// repository's git directory, since the worktree's own `.git` is a file. Before, the
// prune looked under <worktree>/.git/worktrees, which never exists, so after the
// recursive-delete fallback the entry stayed. Measured side by side against f6010b97
// on a Linux VM (C10, remove from a linked worktree whose commondir starts with white
// space: the reference deletes the entry). When the daemon's own environment sets
// GIT_COMMON_DIR, or the check pinned nothing, the old <repo>/.git stays.
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

// worktreeAdminDir reads a linked worktree's `.git` pointer file
// ("gitdir: <mainGitDir>/worktrees/<name>") and returns that admin directory, or
// "" when worktreePath is not a linked worktree. Removing it drops the worktree's
// registration from the main repository.
func worktreeAdminDir(worktreePath string) string {
	b, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
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
// worktree being removed. Without this, a forged or stale `<worktreePath>/.git` that
// names a SIBLING worktree's admin dir (still under `.git/worktrees`, so the
// containment check alone passes) would let the prune delete that unrelated
// registration. Measured against 7d193f89: the reference leaves the sibling's
// registration in place, so requiring the back-pointer to match keeps claustrum's
// prune to the worktree the caller actually named.
func worktreeAdminBelongsTo(adminDir, worktreePath string) bool {
	b, err := os.ReadFile(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		return false
	}
	// sameCanonicalPath, not ==: on Windows git writes this record with forward slashes,
	// and once the fallback delete has removed the worktree neither side can be
	// resolved, so the two spellings differ only in slash direction. Before this the
	// prune was skipped there and the entry stayed. Measured against f6010b97 on a
	// Windows 11 VM, where the reference deletes the entry.
	return sameCanonicalPath(canonicalPathOfGone(strings.TrimSpace(string(b))),
		canonicalPathOfGone(filepath.Join(worktreePath, ".git")))
}

// canonicalPathOfGone is canonicalPath for a path that possibly no longer exists. It resolves
// the nearest existing ancestor and joins the missing rest back on. After the fallback
// delete the worktree is gone, but its parent still resolves. Before, a remove named
// through a symlinked root (macOS /tmp -> /private/tmp) did not match the path in the
// entry. Git records the resolved path there, so the entry stayed. Measured against
// f6010b97 on a macOS VM, where the reference deletes the entry.
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
