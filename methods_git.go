package main

import (
	"context"
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
	WorktreeRoot string `json:"worktreeRoot,omitempty"`
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
// ⚠️ THE BOUND IS SOFTER THAN IT LOOKS. CombinedOutput waits for the output pipe
// to close, not merely for git to exit, so a git that spawns a child which
// OUTLIVES it keeps the call blocked past the deadline — the orphan still holds
// the pipe. Measured: a stub `sleep 30` under `sh` took the full 30s against a
// 300ms gitTimeout, while the same stub as `exec sleep 30` returned promptly.
// Closing it means reading the streams explicitly instead of CombinedOutput,
// which is more code and more divergence, so it is recorded rather than fixed.
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
// below. git.worktree_create puts this string ON THE WIRE on failure
// ("git worktree add failed: " + out), and `git worktree add` writes both its
// progress and its fatal to stderr while leaving stdout empty. Measured against
// 5db5e4a with a branch name that already exists:
//
//	reference : "git worktree add failed: Preparing worktree (new branch 'dup')\nfatal: a branch named 'dup' already exists"
//	stdout-only: "git worktree add failed: "
//
// This helper's remaining callers fall into four groups, and only the first two
// are safe by argument:
//
//	compare or discard   isRepo, isRepoGitDir — exit status or an exact "true",
//	                     both of which a warning-prefixed string fails safely;
//	                     show-ref --verify --quiet and branch -D, which discard
//	                     their output entirely
//	wants the stderr     gitWorktreeCreate — the failure text lives there
//	echoes it verbatim   --show-toplevel → root/repo, symbolic-ref --short HEAD →
//	                     branch (rev-parse --short HEAD supplies only the
//	                     detached-HEAD sha fallback), remote get-url → repoSlug,
//	                     symbolic-ref → defaultBranch
//	echoed AND used      rev-parse --abbrev-ref HEAD → sourceBranch, which is both
//	  as argv            put on the wire AND passed to `git worktree add` as the
//	                     commit-ish. This is the most exposed of the set and had
//	                     no row at all: a folded warning here would not merely be
//	                     echoed, it would make the add fail and surface as a
//	                     wire-visible worktree_add_failed.
//
// The last two groups put combined output on the wire unsplit. No fixture has been
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
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
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

func isRepo(dir string) bool {
	out, ok := hardenedGit(dir, false, "rev-parse", "--is-inside-work-tree")
	return ok && out == "true"
}

func gitInfo(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// 7d193f89 opens git.info with the excludesFile probe (GIT_DIR=/dev/null),
	// reproduced for parity though its result is not on the wire.
	gitExcludesProbe(p.Path)
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
	branch, _ := hardenedGit(p.Path, false, "branch", "--show-current")
	if branch == "" {
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
	// A repo whose config cannot be enumerated is refused with -32603 before status
	// runs (7d193f89). The enumeration is on baseRepo — where git reports the config
	// path relative (".git/config"), matching the reference; the worktree path would
	// resolve to the same config but git would report it absolute. Measured on VM.
	if msg, bad := hostileConfigRefusal(p.BaseRepo); bad {
		return errResult(req.ID, codeInternal, msg)
	}
	if !gitStatusWorktreeOf(p.Path, p.BaseRepo) {
		// The reference returns the full status shape (clean:false), not the
		// bare notRepoResult that git.info uses.
		return okResult(req.ID, gitStatusResult{})
	}
	// 7d193f89 runs status under the heavy hardening profile with
	// `--untracked-files=all --ignore-submodules=all`. `--untracked-files=all` is
	// wire-visible: an untracked file inside an untracked directory is listed
	// individually (`?? sub/u.txt`) rather than as the directory (`?? sub/`).
	// (The reference builds this in an isolated gitdir to avoid refreshing the real
	// index; that isolation is output-neutral, so status runs against the worktree
	// here.) A path with no work tree still exits 128 → the reference propagates
	// the bare Go error string, not git's "fatal: …" output.
	out, err := hardenedGitStdout(p.Path, true, "status", "--porcelain",
		"--untracked-files=all", "--ignore-submodules=all")
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
func gitStatusWorktreeOf(path, baseRepo string) bool {
	// git canonicalizes the paths it reports (--show-toplevel / --git-common-dir
	// resolve symlinks — macOS /tmp -> /private/tmp — and expand Windows 8.3 short
	// names), so canonicalize our own operands the same way before comparing.
	// Without this a valid worktree under a symlinked or short-named directory
	// never matches git's output and status wrongly answers isRepo:false. No-op on
	// Linux paths with no symlinks, so the frame battery stays byte-identical.
	path = canonicalPath(path)
	baseRepo = canonicalPath(baseRepo)
	out, ok := hardenedGit(path, false, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir")
	if !ok {
		return false
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return false
	}
	top, gitDir, commonDir := lines[0], lines[1], lines[2]
	return samePath(top, path) &&
		!samePath(gitDir, commonDir) &&
		samePath(filepath.Dir(commonDir), baseRepo)
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
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("refusing to create worktree: %s already exists, and a new worktree is only ever created in a fresh directory", p.WorktreePath),
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
	// such as <repo>/.claude/worktrees/<id> succeed on a fresh repo.
	if err := os.MkdirAll(filepath.Dir(p.WorktreePath), 0o755); err != nil {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     fmt.Sprintf("failed to create parent directory: %v", err),
			ErrorCode: "mkdir_failed",
		})
	}
	// For an external worktreeRoot, tag the <directory> level as holding managed
	// session worktrees before git runs (7d193f89 writes the marker even if the add
	// later fails). This is the same marker baseRepoUnderManagedWorktrees looks for,
	// so a later create whose baseRepo sits under here is refused as a nested repo.
	if p.WorktreeRoot != "" {
		_ = ensureManagedWorktreesMarker(filepath.Dir(p.WorktreePath))
	}
	// 7d193f89 also creates the worktree directory ITSELF before `git worktree add`
	// (git adds into the pre-made empty dir). It does so by opening the parent and
	// `mkdirat`-ing the leaf, so an unwritable/foreign-owned parent fails HERE as
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
		if _, ok := hardenedGit(repo, false, "show-ref", "--verify", "--quiet", "refs/heads/"+source); !ok {
			source = "" // fall through to HEAD below
		}
	}
	if source == "" {
		if s, ok := hardenedGit(repo, false, "rev-parse", "--abbrev-ref", "HEAD"); ok {
			source = s
		}
	}
	addArgs := []string{"worktree", "add", "-b", p.BranchName, p.WorktreePath}
	if source != "" {
		addArgs = append(addArgs, source)
	}
	out, ok := hardenedGit(repo, false, addArgs...)
	if !ok {
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "git worktree add failed: " + boundedStderrHead(out),
			ErrorCode: "worktree_add_failed",
		})
	}
	// `git worktree add` checks out tracked files only, so the reference seeds the
	// new worktree with the untracked files that .worktreeinclude names AND git also
	// ignores (see copyWorktreeIncludes). .claude/ is no longer copied unconditionally
	// (7d193f89 dropped that). Best-effort: the worktree exists and the reference
	// reports success regardless.
	populateWorktree(repo, p.WorktreePath)
	return okResult(req.ID, worktreeResult{Success: true, Path: p.WorktreePath, SourceBranch: source})
}

func gitWorktreeRemove(req *request) response {
	var p gitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	repo := p.repoDir()
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
	// non-destructive `.git`-file gate — an external path that is not a real
	// worktree is refused and LEFT IN PLACE, where an in-repo remove would fall back
	// to a recursive delete. Measured against 7d193f89 on an ephemeral VM.
	if p.WorktreeRoot != "" {
		if msg := worktreeExternalContainmentRefusal(p.WorktreeRoot, p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "remove"); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
		}
		if msg := externalWorktreeMissingGitRefusal(repo, p.WorktreePath); msg != "" {
			return okResult(req.ID, worktreeRemoveResult{Success: false, Error: msg})
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
	// nil — nothing to protect — and skipped the branchName delete the reference
	// still performs. Worse, the frame varied with the daemon's cwd, which no
	// golden can observe because the harness runs from a temp dir. Raised in review
	// on #232; pinned by TestWorktreeRemoveEmptyPathIsNotRefused.
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
	if _, bad := hostileConfigRefusal(repo); bad {
		return okResult(req.ID, worktreeRemoveResult{
			Success: false,
			Error: "failed to remove worktree: could not check whether " + p.WorktreePath +
				" is locked (its registrations could not be examined); retry",
		})
	}
	adminDir := worktreeAdminDir(p.WorktreePath)
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
	//	bogus-repo   baseRepo is not a repo at all DELETED (fallback)
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
		// (independent of the lock reason) and leaves the directory in place. Only the
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
	// The reference also deletes the branch when branchName is given, and does
	// so FORCEFULLY — an unmerged branch goes too (probe-measured at 5db5e4a:
	// both a merged and an unmerged branch are gone afterwards, where claustrum
	// left both behind). Best-effort: naming a branch that does not exist still
	// answers {"success":true}, so a failed delete is not surfaced.
	if p.BranchName != "" {
		hardenedGit(repo, false, "branch", "-D", p.BranchName)
	}
	// Prune the registration (see above). A no-op when `git worktree remove`
	// already dropped it; the fix for the fallback path. The ONLY legitimate admin
	// dir is `<repo>/.git/worktrees/<name>`, so require the pruned path to resolve
	// strictly inside `<repo>/.git/worktrees` before the delete — not merely inside
	// the repo. worktreeAdminDir returns the `.git` pointer's contents verbatim, and
	// those contents are attacker-writable, so a stale or forged pointer such as
	// `gitdir: <repo>/src` or `<repo>/.git/objects` (or a `..` variant that Clean or
	// EvalSymlinks collapses back under the repo) would otherwise turn the prune into
	// an os.RemoveAll of unrelated repo data. Constraining to `.git/worktrees` rejects
	// every such target while still pruning a real registration. Off-wire: the result
	// is discarded and the reply is success:true either way. Raised by review on PR 286.
	if adminDir != "" && worktreeAdminBelongsTo(adminDir, p.WorktreePath) &&
		pathStrictlyUnder(canonicalPath(adminDir), canonicalPath(filepath.Join(repo, ".git", "worktrees"))) {
		_ = os.RemoveAll(adminDir)
	}
	return okResult(req.ID, worktreeRemoveResult{Success: true})
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
	return canonicalPath(strings.TrimSpace(string(b))) == canonicalPath(filepath.Join(worktreePath, ".git"))
}
