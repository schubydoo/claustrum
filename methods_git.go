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
	req.shadow = s.gitShadow
	// The daemon-wide window of gitWideLimit counts this request while it runs.
	if offSwitchSet(req.Params) {
		s.offGitRequests.Add(1)
		defer s.offGitRequests.Add(-1)
	}
	// While a request with "shadowDirOff":true runs, every git request that
	// starts, and each later git command of a request, run as with the member
	// (cells H1 and H4 of round task073-02, Linux VM). The count is read live
	// (request.offLive).
	req.offHold = &s.offGitRequests
	if !limitedGitMethod(req.Method) {
		return fn(req)
	}
	req.stop = &gitStop{wide: &s.offGitRequests}
	req.use.stop = req.stop
	return req.stop.reply(req, fn(req))
}

// gitParams holds the members of the five git methods. Each method decodes
// only its own members (gitparams.go).
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
	// Caller-activated (like timeoutMs), not an operator divergence. Only
	// git.worktree_create reads it (createViewParams).
	ExistingBranch string `json:"existingBranch,omitempty"`
	WorktreeRoot   string `json:"worktreeRoot,omitempty"`
	// TimeoutMs is a caller-supplied per-request deadline (milliseconds) on the
	// git.worktree_create add + checkout, added by the reference in 4534d86 and
	// advertised as the git.worktree_create.timeoutMs feature. Absent or 0 arms no
	// deadline, so the default path is byte-identical. This is caller-activated
	// (like CT-1's wantPid), not an operator divergence. Only git.worktree_create
	// reads it (createViewParams).
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// ShadowDirOff turns off the filter rule of the git.worktree_create checkout
	// (gitfilter.go) for this request. With it, 5fd08069 passes no filter pairs, runs
	// the repository's filter and succeeds (cell E1, Linux VM). It also turns off
	// the daemon's own read of the repository's config on the five git methods
	// (repoConfigGate). Cells X1 measured that on git.info and git.status, and
	// cells V2 on the three other methods. It turns off the settings copy for the
	// request too (shadowHost.control). Only the JSON value true sets it
	// (jsonTrueSwitch). A request whose first git command starts while another
	// one with it runs gets it too (bindGitParams). The five methods read the
	// switch again at later commands (gitUse.atCommand).
	ShadowDirOff jsonTrueSwitch `json:"shadowDirOff,omitempty"`
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
// git, gitContext and gitStdoutErr serve tests only. No production code calls them.
func git(dir string, args ...string) (string, bool) {
	return gitContext(context.Background(), dir, args...)
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
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

// worktreeCreateCtx builds the two contexts of git.worktree_create.
//
// base carries the time limits of the request (gitStop.context). The add runs
// under base only, so the caller's timeoutMs never ends the add. A held add
// gets the stop at 120 s, also with a timeoutMs of 5000 (cell T6 of round
// task010-01, Linux and macOS VMs).
//
// caller is base plus the caller's timeoutMs (4534d86 parity). The read-tree
// checkout runs under caller, so the caller's deadline kills the checkout. The
// daemon also tests caller after the add and after the copy step. When timeoutMs
// is 0 or less, caller is base itself.
//
// errCallerTimeoutMs is the cause stamped on the caller's deadline
// (callerTimeoutFired).
var errCallerTimeoutMs = errors.New("git.worktree_create caller timeoutMs deadline")

// callerDeadline arms the caller's deadline on base. It is a variable so that a
// test can also fire the deadline at a marker of its git stub.
var callerDeadline = func(base context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(base, d, errCallerTimeoutMs)
}

func worktreeCreateCtx(s *gitStop, timeoutMs int) (base, caller context.Context, cancel context.CancelFunc) {
	base = s.context()
	if timeoutMs <= 0 {
		return base, base, func() {}
	}
	ctx, c := callerDeadline(base, time.Duration(timeoutMs)*time.Millisecond)
	return base, ctx, c
}

// callerTimeoutFired reports whether ctx was canceled by the caller's own timeoutMs
// deadline. Only that deadline earns the 4534d86 errorCode "timeout".
func callerTimeoutFired(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errCallerTimeoutMs)
}

// isRepo runs right after hostileConfigRefusal on dir, so it runs no precursor
// (hardenedGitFirst) and pins the hooks of that check's listing l. err is the
// error of the call.
func isRepo(u gitUse, dir string, l configListing) (bool, error) {
	out, err := hardenedGitRunPre(u, dir, false, &l, nil, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true", err
}

// copyGoneResult is the frame of git.worktree_create for a git call whose
// repository folder went: the text of that call alone, as for the text of
// `repoLimitError` (from the code, not measured for `repoCopyGoneText` and its
// listing form).
func copyGoneResult(req *request, err error) response {
	return okResult(req.ID, worktreeResult{Success: false, Error: err.Error(), ErrorCode: "worktree_add_failed"})
}

func gitInfo(req *request) response {
	p, bad := bindGitParams(req, &infoViewParams{})
	if bad != nil {
		return *bad
	}
	// The excludesFile the reference reads at git.info is now cached
	// and applied to every git op via hardenedProfileArgs/userExcludesFile, rather than
	// probed here and discarded. The read comes first, so it also runs before a trust
	// refusal, as on f6010b97 and 89cb6289 (every trust refusal row).
	userExcludesFile()
	// notRepo is the "no repository" answer. It carries the settingsControl member
	// that the request has at that moment (shadowHost.replyControl): none in a daemon
	// that has not passed its start test, as for a HEAD that is no ref as the first
	// request (s3p2-linux cell E5, Linux VM).
	notRepo := func() response {
		return okResult(req.ID, notRepoResult{SettingsControl: req.shadow.replyControl(req.offLive)})
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
	// A path that does not exist gets the "no repository" answer, with no start
	// test and no git call beyond the excludes read of a first request. It first
	// takes the steps of a folder with no repository up to the start of the config
	// listing (missingPathListing). That start fails on the missing folder. These
	// steps are claustrum's own and are not measured. The place of this test after the count refusal is not
	// measured.
	// On 5fd08069 such a git.info as the first git
	// request of a daemon makes 1 call, the excludes read, and its reply has no
	// settingsControl member. The next request on a repository runs the start
	// test. A later git.info on such a path makes 0 calls (cell F1 of
	// val-s3p2b2-6-windows, Windows VM, 2 of 2 runs, and request 3 of the 15
	// runs of its group P1). A Linux VM has the same counts for a missing
	// path, a path with a missing parent and a dangling link. An empty path
	// makes 3 calls there, and 2 later (row Q1 of val-s3p2b2-8-linux2). A macOS
	// VM makes 1 and 0 calls for a missing path (cell q1-missing of
	// val-s3p2b2-9-macos). A path that is a file is not such a
	// path: both builds run a `git version` call there and answer the hooks
	// refusal (cell F1).
	if p.Path != "" {
		if _, err := os.Stat(p.Path); errors.Is(err, fs.ErrNotExist) {
			_ = missingPathListing(p.Path)
			return notRepo()
		}
	}
	if t.verdict == gitDirNoRepo {
		// A `.git` file to a missing path runs the start test, and the reply has
		// the member. 5fd08069 does so as the first git request of a daemon (cell V3
		// of round task083-01, Linux, macOS and Windows VMs). A missing entry under
		// `worktrees` of an existing git directory runs no test (cell V4). One in a
		// missing repository runs it (cell V4g of round task083-02, Linux VM). Not
		// measured: the other
		// methods there, a failed test there, and a host with no git. claustrum
		// runs no test in those methods or with no git. A failed test answers its
		// text.
		if t.goneGitFile && gitLookupError() == nil {
			if msg := req.shadow.gate(bool(p.ShadowDirOff)); msg != "" {
				return errResult(req.ID, codeInternal, msg)
			}
		}
		// A listing that fails here refuses (rows A-N1 and A-X2 on Linux and macOS VMs,
		// cells P3-infoN and P3-infoX on a Windows VM).
		if msg := noRepoInfoCalls(t, req.stop, p.Path, bool(p.ShadowDirOff)); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return notRepo()
	}
	// The start test and the daemon's own read of the repository's config come
	// before the listing (settingsGateRefusal). Their place after the count check is
	// not measured.
	//
	// A path that is a regular file gets the gate of the repository of its
	// folder, and no gate when that folder is in no repository.
	//
	//   - Inside a repository: the call about the repository, the read of its
	//     config and the folder of the repository. As the first git request of
	//     a daemon, such a git.info of 5fd08069 makes 14 calls. Calls 12 and 13
	//     are `rev-parse --git-dir` in the root of the repository and the read
	//     of the config, and the daemon then has `.probe` and the copy. The next
	//     git.info on the repository makes 8 calls (row Q1 of
	//     val-s3p2b2-8-linux2, Linux VM).
	//   - Outside any repository: no start test. As the first git request,
	//     5fd08069 makes 2 calls, the excludes read and `version`. The reply has
	//     no settingsControl member, and no folder `git-shadow` is made. The
	//     next git.info on a repository runs the start test (cell q1-file of
	//     val-s3p2b2-8-macos, macOS VM, 2 of 2 runs).
	//
	// The calls after the gate keep their form: the request has no copy for
	// them. Round val-s3p2b2-9 has both rows on the Linux and macOS VMs, and a
	// refused config of the repository of the file (Linux VM). On a Windows VM
	// both builds answer the hooks refusal for such a file, with `fork/exec
	// <git.exe>: The directory name is invalid.` (part V of
	// val-s3p2b2-9-windows, 2 runs). Not measured: a folder whose git
	// directory the trust check refuses, and another kind of file.
	gateTrust, gateDir, gate := t, p.Path, true
	inFile := false
	if fi, err := os.Stat(p.Path); err == nil && fi.Mode().IsRegular() && t.verdict == gitDirTrusted && t.pinCommonDir == "" {
		ft := requestGitDirTrust(filepath.Dir(p.Path), true)
		inFile = ft.verdict == gitDirTrusted && ft.pinCommonDir != ""
		gateTrust, gateDir, gate = ft, filepath.Dir(p.Path), inFile
	}
	// With no git in the PATH of the daemon, 5fd08069 answers "no repository"
	// with no member, runs no test and logs no line about git. Its folder holds
	// the lock file only (cell C7 of round tier0-5, Linux VM, 3 of 3 runs). The
	// other git methods are not measured there: they keep the failed start test.
	// A request with "shadowDirOff":true is not measured either, and it keeps
	// its answer. The settingsControl member follows the state of the daemon:
	// a daemon that passed its test before it lost git is not measured.
	if gate && !bool(p.ShadowDirOff) && gitLookupError() != nil {
		req.shadow.folderWithNoTest()
		return notRepo()
	}
	if gate {
		if msg, noRepo := req.settingsGateRefusal(gateTrust, p.ShadowDirOff, gateDir); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		} else if noRepo {
			return notRepo()
		}
	}
	if inFile {
		req.use = gitUse{stop: req.stop}
	} else if gate {
		req.keepLive(bool(p.ShadowDirOff), gateTrust, gateDir)
	}
	req.listEntryConfig(t)
	// If the repo's config cannot be enumerated (e.g. an `include.path` with no value), the
	// reference cannot pin its config-defined hooks off and refuses with -32603
	// rather than running git. Measured against 7d193f89 on an ephemeral VM.
	c := hostileConfigRefusal(req.use, p.Path, true)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	}
	if c.noRepo {
		return notRepo()
	}
	// A call whose repository folder went answers its text (from the code, not
	// measured). The later calls of git.info keep their answers on a failure.
	if _, err := hardenedGitRunPre(req.use, p.Path, true, &c.listing, nil, "rev-parse", "--git-dir"); copyGone(err) {
		return errResult(req.ID, codeInternal, err.Error())
	} else if err != nil {
		return notRepo()
	}
	// The root is the folder where the walk of the trust check found the repository
	// (gitWalkRoot). 89cb6289 runs no `rev-parse --show-toplevel` here on Linux and
	// macOS VMs. A walk that found a git directory with no work tree (a bare repository,
	// a path inside .git), or nothing, answers the bare shape (rows I04a, I04b, I05a,
	// I05b and T11a). On Windows git is asked to confirm the root (gitInfoRoot).
	walkRoot, walkGitDir := gitWalkRoot(p.Path)
	if walkRoot == "" {
		return notRepo()
	}
	// The Windows root pair takes the form of the off switch at this moment, as
	// each later command does (cell O2 of round task103-04, Windows VM). On Linux
	// and macOS the pair runs no git call, so a pending gate runs here, right
	// before the first command of gitRepoSlug.
	u := req.use.atCommand()
	copyOf := ""
	var front []string
	if _, ok := u.names(p.Path); ok {
		copyOf = u.gitDir
		front = u.gitDirFront()
	}
	top := gitInfoRoot(req.stop, walkRoot, walkGitDir, copyOf, u.gitDirPin(p.Path), front)
	// slug and defaultBranch come before the branch, matching the reference order.
	slug := gitRepoSlug(req.use, p.Path)
	defBranch := gitDefaultBranch(req.use, p.Path)
	// The branch is `branch --show-current` (works on a normal and an unborn HEAD),
	// empty on a detached HEAD → reported as "detached:<short-sha>".
	//
	// When `branch --show-current` fails, the result has no `branch` member at all,
	// never git's error text. Measured side by side against 90fca6e6 and f6010b97 on a
	// Linux VM: a HEAD git fails to resolve ("ref: refs/heads/main" or 40 hex digits,
	// each followed by junk, H15 H18 NH21 NH22) answers without `branch` on both.
	branch, ok := hardenedGit(req.use, p.Path, true, "branch", "--show-current")
	switch {
	case !ok:
		branch = ""
	case branch == "":
		// A `rev-parse --short HEAD` that fails gives no `branch` member either.
		// On a Windows VM that call exits 128 in a files repository with
		// `extensions.refStorage = reftable` and format version 1, and the reply
		// of 5fd08069 has no `branch` (cell V6-13d of val-s3p2b2-3-windows).
		if sha, ok := hardenedGit(req.use, p.Path, true, "rev-parse", "--short", "HEAD"); ok {
			branch = "detached:" + sha
		}
	}
	return okResult(req.ID, gitInfoResult{
		IsRepo:        true,
		Repo:          infoRepoName(top),
		Branch:        branch,
		Root:          top,
		RepoSlug:      slug,
		DefaultBranch: defBranch,

		SettingsControl: req.shadow.replyControl(req.offLive),
	})
}

// keepLive gives the later git commands of a git.info or a git.list_branches the
// off switch of the moment (liveSwitch). off is the hold at the gate of the
// request, and t and dir are the trust verdict and the folder of that gate.
func (req *request) keepLive(off bool, t gitDirTrust, dir string) {
	req.followSwitch(off, func(l *liveSwitch, _ bool) (gitUse, bool) {
		msg, noRepo := req.settingsGateRefusal(t, false, dir)
		if msg != "" || noRepo {
			return gitUse{}, false
		}
		req.listEntryConfig(t)
		req.use.live = l
		return req.use, true
	})
}

// keepLiveForm is keepLive for git.status and git.worktree_remove. The later
// gate asks git about the repository only with ask (liveSwitch). It changes
// only the form of the git commands. The request keeps the use of its first
// gate, with its read of the config and its short-lived folders, for every
// step that is no git command.
func (req *request) keepLiveForm(off bool, t gitDirTrust, dir string) {
	req.followSwitch(off, func(l *liveSwitch, ask bool) (gitUse, bool) {
		use, read, gitRefused, configRefused := req.use, req.repoRead, req.gitRefused, req.configRefused
		defer func() {
			req.use, req.repoRead, req.gitRefused, req.configRefused = use, read, gitRefused, configRefused
		}()
		msg, noRepo := req.settingsGate(t, false, dir, ask)
		if msg != "" || noRepo {
			return gitUse{}, false
		}
		u := req.use
		u.live = l
		return u, true
	})
}

// keepLiveCreate is keepLiveForm for git.worktree_create in repo. Before the
// first good `worktree add` (added false) the create adopts its later gate.
// On 5fd08069 the add and every later command of such a create name the copy
// (cells C4 and C5 of round task105-04, Linux VM). The create then holds its
// git directory as a create whose first gate had the copy does: the folder is
// read before the gate and held after it (acceptGitDir), and held takes it for
// the release at the reply. The request takes the copy, the read of the config
// and that hold as its own use, so every later step acts through the held
// folder. A git directory that is no longer that real directory refuses the
// later gate, and the form stays without a copy (claustrum's own, not
// measured). After the add the later gate changes only the form, as in
// keepLiveForm (cell C3 of round task105-01).
func (req *request) keepLiveCreate(off bool, t gitDirTrust, repo string, added *bool, held **acceptedGitDir) {
	req.followSwitch(off, func(l *liveSwitch, ask bool) (gitUse, bool) {
		use, read, gitRefused, configRefused := req.use, req.repoRead, req.gitRefused, req.configRefused
		keep := false
		defer func() {
			req.gitRefused, req.configRefused = gitRefused, configRefused
			if !keep {
				req.use, req.repoRead = use, read
			}
		}()
		var before os.FileInfo
		if gitDirMustBeReal && !*added && t.verdict == gitDirTrusted && t.pinCommonDir != "" {
			before = gitDirIdentity(t.pinCommonDir)
		}
		msg, noRepo := req.settingsGate(t, false, repo, ask)
		if msg != "" || noRepo {
			return gitUse{}, false
		}
		req.listEntryConfig(t)
		if *added || !req.use.shadow() {
			u := req.use
			u.live = l
			return u, true
		}
		if gitDirMustBeReal {
			h := acceptGitDir(req.use.gitDir, before)
			if h == nil {
				return gitUse{}, false
			}
			h.holdFor(repo, t)
			*held = h
			req.use.accepted = h
		}
		keep, l.adopted = true, true
		req.use.live = l
		return req.use, true
	})
}

// followSwitch gives req.use the off switch of the request, with gate as its
// later gate. off is the hold at the gate of the request.
func (req *request) followSwitch(off bool, gate func(l *liveSwitch, ask bool) (gitUse, bool)) {
	if !req.shadow.active() {
		return
	}
	l := &liveSwitch{off: req.offLive, pending: off && !req.offOwn, stop: req.stop}
	l.gate = func(ask bool) (gitUse, bool) { return gate(l, ask) }
	req.use.live = l
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
func gitRepoSlug(u gitUse, dir string) string {
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
	url, ok := hardenedGit(u, dir, true, "remote", "get-url", "origin")
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
// Else `rev-parse --verify --quiet refs/remotes/origin/<name>^{commit}` runs in dir,
// with its listing. Exit 0 keeps the name, and any failure gives "". f6010b97 and
// 89cb6289 do so on Linux and macOS VMs (rows I10a to I10c: a dangling origin/HEAD,
// one that names a blob, and origin/-x). The call has the heavy profile, as on
// 5fd08069 (s3p2-linux cell B3d, Linux VM, with no "shadowDirOff" member, and
// val-s3p2b1 cell A2 with it, Linux, macOS and Windows VMs). Not
// measured: other invalid names, and a symbolic-ref answer that does not start with
// refs/remotes/origin/. claustrum verifies that answer the same way.
func gitDefaultBranch(u gitUse, dir string) string {
	ref, ok := hardenedGit(u, dir, true, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return ""
	}
	name := strings.TrimPrefix(ref, "refs/remotes/origin/")
	if strings.HasPrefix(name, "-") {
		return ""
	}
	if _, ok := hardenedGit(u, dir, true, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name+"^{commit}"); !ok {
		return ""
	}
	return name
}

func gitStatus(req *request) response {
	p, bad := bindGitParams(req, &statusViewParams{})
	if bad != nil {
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
		if msg := statusGitFileNowhere(t, req.stop, p.BaseRepo, bool(p.ShadowDirOff)); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		if msg := statusNoRepoCalls(req.stop, p.BaseRepo, t.noGit); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return notRepo
	}
	// A baseRepo that resolves and cannot be opened answers here, with no further git
	// call. On a Windows VM that is a dangling junction (rows n11-j and x11).
	if _, err := os.Stat(p.BaseRepo); err != nil {
		return notRepo
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate).
	if msg, noRepo := req.settingsGateRefusal(t, p.ShadowDirOff, p.BaseRepo); msg != "" {
		return errResult(req.ID, codeInternal, msg)
	} else if noRepo {
		return notRepo
	}
	// The later git commands follow the off switch (cells S1 and S2 of round
	// task105-01, Linux VM).
	req.keepLiveForm(bool(p.ShadowDirOff), t, p.BaseRepo)
	// A repo whose config cannot be enumerated is refused with -32603 (row o22b). The
	// listing runs in baseRepo as sent and carries the heavy profile (K1 call 2).
	c := hostileConfigRefusal(req.use, p.BaseRepo, true)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	} else if c.noRepo {
		return notRepo
	}
	// K1 call 3. Its answer is the common directory, in the spelling that git gives
	// it. A failure answers isRepo:false (row n17b).
	out, err := hardenedGitRunPre(req.use, p.BaseRepo, true, &c.listing, nil, "rev-parse", "--absolute-git-dir")
	if copyGone(err) {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if err != nil || out == "" {
		return notRepo
	}
	common := filepath.Clean(strings.TrimRight(out, "\r"))
	// On Windows the request goes on with the git directory that git printed,
	// when it is another spelling of the same folder (usePrintedGitDir).
	if statusFollowsPrintedGitDir {
		if msg := req.usePrintedGitDir(t, common); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
	}
	// No `worktrees` folder in the common directory: no further git call (rows n18
	// and n18c).
	if fi, err := os.Stat(filepath.Join(common, "worktrees")); err != nil || !fi.IsDir() {
		return notRepo
	}
	sp, ok := statusPathOf(p.Path, p.BaseRepo)
	if !ok {
		return notRepo
	}
	probeUse := req.use.atGitDirCommand()
	top, ok, err := statusWorkTreeProbe(req.stop, common, sp.probeTree, statusCommonPin(probeUse, common), probeUse.gitDirFront())
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	} else if !ok {
		return notRepo
	}
	if statusFollowsPrintedGitDir {
		if s, ok := statusPrintedRootSpelling(p.Path, p.BaseRepo, top); ok {
			sp.spellings = append(sp.spellings, s)
		}
	}
	entry, ok := statusEntryOf(req.stop, common, sp, req.use.shadow() || bool(p.ShadowDirOff))
	if !ok {
		return notRepo
	}
	defer func() { _ = entry.root.Close() }()
	// The exec error of a failed command goes on the wire as it is, for example
	// "exit status 128". The text of git does not show for status, ls-files and
	// diff-index. A relative path passes the gate and
	// then fails here, because git gets it as sent (row n25c).
	changes, err := statusChanges(req.use, p.Path, common, entry)
	if errors.Is(err, errStatusNoFolder) {
		return notRepo
	}
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
	p, bad := bindGitParams(req, &infoViewParams{})
	if bad != nil {
		return *bad
	}
	// A path inside a managed worktrees tree, and a non-empty path that does not
	// resolve, answer the bare isRepo:false shape (branches:[]) before any git call
	// and before the check of the daemon's own GIT_CONFIG_COUNT.
	// git.list_branches ignores a baseRepo member, as 5fd08069 does (cells LB1 and
	// LB2 of round task015-02, Linux VM). The managed-worktrees test also runs on
	// the daemon's working directory.
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
		// One more call follows a listing that passed (noRepoInfoCalls).
		if msg := noRepoInfoCalls(t, req.stop, p.Path, bool(p.ShadowDirOff)); msg != "" {
			return errResult(req.ID, codeInternal, msg)
		}
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate). The answer has the error object of git.info (cell X1).
	if msg, noRepo := req.settingsGateRefusal(t, p.ShadowDirOff, p.Path); msg != "" {
		return errResult(req.ID, codeInternal, msg)
	} else if noRepo {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	req.keepLive(bool(p.ShadowDirOff), t, p.Path)
	// A repo whose config cannot be enumerated is refused with -32603 (7d193f89),
	// measured on an ephemeral VM.
	c := hostileConfigRefusal(req.use, p.Path, true)
	if c.refusal != "" {
		return errResult(req.ID, codeInternal, c.refusal)
	}
	if c.noRepo {
		return okResult(req.ID, branchesResult{Branches: []string{}})
	}
	// 5fd08069 runs this under the heavy profile with the config precursor
	// (s3-linux cells B0 and B1, Linux VM). The repo gate is `rev-parse --git-dir` (true for a bare repo and
	// from inside a `.git` directory, unlike git.info's --is-inside-work-tree). The
	// reference returns the full branches shape (branches:[]) on a non-repo, not
	// git.info's bare notRepoResult. The refusal check above was its precursor.
	if _, err := hardenedGitRunPre(req.use, p.Path, true, &c.listing, nil, "rev-parse", "--git-dir"); copyGone(err) {
		return errResult(req.ID, codeInternal, err.Error())
	} else if err != nil {
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
	out, err := hardenedGitStdout(req.use, p.Path, true, "for-each-ref",
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

// pairBeforeAdd runs the two calls of 5fd08069 before the add of a create in
// the state shadow or with "shadowDirOff":true, when the step before the add
// found no entry that names the new leaf. They run when the folder of the
// registrations of gitDir is a real folder, empty or not (cell R-Q2b of round task084-01, Linux, macOS
// and Windows VMs). With the member 5fd08069 runs them in a repository with a
// linked worktree (cell 50a of s3p2b-edges-linux, cell A3-create-plain of
// val-s3p2b1-linux). A missing folder and an entry that names the leaf give no
// pair (cells R-Q2a, R-Q2c, R-Q2d and R-Q4i0). The state fallback-git gets no
// pair (claustrum's own, not measured). For a linked baseRepo that folder is the one of the main
// repository (gitDirRegistryDir, not measured). The calls come after
// `rev-parse --absolute-git-dir` and before the listing of the add (R-Q2b,
// macOS and Windows VMs).
//
// The test does not follow a link. A symbolic link to a real folder there
// gives no pair, also when that folder holds a registration (cells C6, C6n and
// C6b of round task109-01, cells E1 and E2 of round task109-02, Linux VM). A
// broken link, a file and a Windows junction give none either (not measured).
//
// The calls are gitDirWorkTreeToplevel in gitDir, with repo as the work tree.
// Their answers are not used, and a failure changes nothing (cell R-Q4e,
// Linux VM). The timeoutMs of the caller does not end them. With the second
// call held 10 s and a timeoutMs of 3000, 5fd08069 sends no signal, then runs
// the add and answers the timeout before the checkout, with the rollback
// (cell R-Q4d, Linux VM). The limits of the two calls are those of their kind
// (gitstop.go). No cell measured a limit there: that is claustrum's own.
func pairBeforeAdd(req *request, gitDir, repo string) {
	if fi, err := os.Lstat(gitDirRegistryDir(gitDir)); err != nil || !fi.IsDir() {
		return
	}
	u := req.use.atGitDirCommand()
	_, _ = gitDirWorkTreeToplevel(req.stop, gitDir, repo, u.gitDirPin(repo), u.gitDirFront())
}

func gitWorktreeCreate(req *request) response {
	p, bad := bindGitParams(req, &createViewParams{})
	if bad != nil {
		return *bad
	}
	if p.BranchName == "" {
		return errResult(req.ID, codeInvalidParam, "branchName is required")
	}
	repo := p.repoDir()
	defer req.runAtReply()
	return withWorktreeCreateLock(repo, func() response {
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
	// comes after a trust refusal and before the "no repository" answer.
	t := requestGitDirTrust(repo, false)
	// The git directory as it is before the gate (acceptedGitDir).
	var gitDirBefore os.FileInfo
	if gitDirMustBeReal && t.verdict == gitDirTrusted && t.pinCommonDir != "" {
		gitDirBefore = gitDirIdentity(t.pinCommonDir)
	}
	if t.verdict == gitDirRefused {
		msg := t.refusal
		if daemonCommonDirSet() {
			msg = gitDirAddWrap + msg
		} else if !bool(p.ShadowDirOff) && req.shadow.active() && req.shadow.readsRepoConfig() && entryIsLink(repo) {
			msg = gitDirAddWrap + msg
			makeRefusedLeafParents(p, repo)
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
		// One more call follows a listing that passed (noRepoCreateCalls).
		if msg := noRepoCreateCalls(t, req.stop, !bool(p.ShadowDirOff) && req.shadow.active(), bool(p.ShadowDirOff), repo); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
		}
		return okResult(req.ID, worktreeResult{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate). Nothing is created (cells W7 and E1, Linux VM).
	if msg, noRepo := req.settingsGateRefusal(t, p.ShadowDirOff, repo); msg != "" {
		return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	} else if noRepo {
		return okResult(req.ID, worktreeResult{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
	}
	// The create holds the git directory that its gate accepted until it answers
	// (acceptedGitDir). A git directory that changed during the gate is not
	// measured: it gets the refusal of a config behind a link (cells X1c-01 and
	// X1c-02 of val-s3p2b2-7-windows have that text for a change after the gate).
	if gitDirMustBeReal && req.use.shadow() {
		held := acceptGitDir(req.use.gitDir, gitDirBefore)
		if held == nil {
			return okResult(req.ID, worktreeResult{Success: false,
				Error: repoConfigPrefix + gitDirLinkText(req.use.gitDir), ErrorCode: "worktree_add_failed"})
		}
		defer held.release()
		held.holdFor(repo, t)
		req.use.accepted = held
	}
	// A linked baseRepo: the calls in it get the items of the `config.worktree`
	// file of its entry, as on git.info. 5fd08069 has `-c core.eol=lf` from that
	// file on the rev-parse calls and on the `worktree add` (cell C12 of round
	// task007-02, macOS VM).
	req.listEntryConfig(t)
	// The later git commands follow the off switch (cells C1 to C5 of rounds
	// task105-01 and task105-04, Linux VM). added is true after a good add.
	added := false
	var laterHeld *acceptedGitDir
	defer func() { laterHeld.release() }()
	req.keepLiveCreate(bool(p.ShadowDirOff), t, repo, &added, &laterHeld)
	// A repo whose config cannot be enumerated is refused before git runs — the
	// reference surfaces the same "config-defined hooks could not be pinned off"
	// detail under errorCode worktree_add_failed. Measured on an ephemeral VM.
	c := hostileConfigRefusal(req.use, repo, false)
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
	//
	// A baseRepo with no work tree gets that answer too, also when the call
	// prints `true` (gitUse.noWorkTree).
	//
	// A repository test that its time limit stopped answers the stop text as it
	// is, and nothing is made (cell TI of round task010-04, Linux VM).
	inRepo := false
	if !c.noRepo {
		var err error
		if inRepo, err = isRepo(req.use, repo, c.listing); copyGone(err) {
			return copyGoneResult(req, err)
		}
	}
	if !inRepo || req.use.noWorkTree(repo) {
		if err := req.stop.halted(); err != nil {
			return okResult(req.ID, worktreeResult{Success: false, Error: err.Error(), ErrorCode: "worktree_add_failed"})
		}
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
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "create", false); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A root in the repository is refused before the root-chain step. The first
		// test compares the cleaned paths and resolves no symlink (rows K1 to K3 and
		// MX14). Then claustrum makes the calls that f6010b97 and 89cb6289 make before
		// their next refusal. They are a light `rev-parse --show-toplevel`, a heavy
		// `rev-parse --absolute-git-dir` and a light `worktree list --porcelain -z`.
		// Each runs in baseRepo with its listing. With the repo check that is 9 calls
		// (Linux and macOS VMs, rows E2, W3, W4 and Y11a to Y11c). claustrum does not
		// use the answer of the absolute-git-dir call.
		if msg := worktreeRootInRepoRefusal(p.WorktreeRoot, p.BaseRepo, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A failed show-toplevel stops the create, and so does a `worktree list` that
		// fails with -z and then without it. Nothing is made (5fd08069, cells C-W17b and
		// C-W17 of round tier0-20, Linux VM). A show-toplevel that its time limit
		// stopped gets the same frame, with the stop text (cell TB of round
		// task010-02, Linux VM).
		topLevel, err := repoTopLevelCall(req.use, repo, nil)
		if copyGone(err) {
			return copyGoneResult(req, err)
		}
		if err != nil {
			return okResult(req.ID, worktreeResult{Success: false, ErrorCode: "mkdir_failed",
				Error: parentStepPrefix + "cannot determine the repository's work tree: " + err.Error()})
		}
		_ = repositoryCheckError(req.use, repo, nil)
		listed, err := worktreeListTwice(req.use, repo)
		if copyGone(err) {
			return copyGoneResult(req, err)
		}
		if err != nil {
			return okResult(req.ID, worktreeResult{Success: false, ErrorCode: "mkdir_failed",
				Error: parentStepPrefix + "cannot list the repository's worktrees: " + err.Error()})
		}
		// Not measured: this test before the ancestor walk. A failing folder above a
		// loop gets the loop text.
		if msg := locationLinkRefusal(p.WorktreeRoot, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "create", true); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		checkouts := newRepoCheckouts(repo, p.BaseRepo, topLevel, listed)
		// A worktreeRoot is refused below a directory owned by a user other than you
		// or uid 0. It is also refused below a directory writable by a shared group or
		// by every user, without the sticky bit. The refusal comes after the 9 calls,
		// with no further git call (rows K1, G2 to G18, Linux and macOS VMs). In rows
		// Y11d and T7 both references send it where the checkout text also applies. It
		// comes before the root-chain step (rows G18, G36a, G40a and G40b) and the
		// tests of the root itself (rows G16 and G17). The walk also runs the checkout
		// tests and the .git test on each directory, so the first failing directory
		// from the top decides.
		if msg, code := worktreeRootAncestorRefusal(p.WorktreeRoot, checkouts); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: code})
		}
		// A listed worktree whose folder is gone gets that folder, empty, before the
		// refusal (5fd08069, cell C-W8 of round tier0-20, Linux VM). Only the folder
		// itself is made, and only below a folder that exists (not measured).
		if msg, gone := worktreeRootCheckoutRefusal(p.WorktreeRoot, checkouts, "create", true); msg != "" {
			makeGoneWorktreeFolder(gone)
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// The root-chain step comes before the owner and write refusals of the root.
		// The <directory> symlink refusal comes after them, then the <directory> step.
		// That is the measured order. Not measured: the session tests before the
		// owner and write tests of the root, and the write tests of the <directory>
		// level before its .git test.
		dir, msg, code := externalChainCheck(p.WorktreeRoot, p.WorktreePath)
		if msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: code})
		}
		externalDir = dir
		if msg := sessionLocationRefusal(p.WorktreeRoot); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeRootShareRefusal(root); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "create"); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// The <directory> level gets the owner and write tests of the root. The refusal
		// comes before the step that drops an old registration of the leaf, so that
		// registration stays (5fd08069, cell C-W18 of round tier0-20, Linux VM). Its
		// owner test is not measured.
		if msg := worktreeRootShareRefusal(filepath.Dir(filepath.Clean(p.WorktreePath))); msg != "" {
			return okResult(req.ID, worktreeResult{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := repoFolderInWorktreeRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, listed, "create"); msg != "" {
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
		// On Linux and macOS the refusal names the cleaned path, with the symlinks
		// of its parent folder resolved and its last name kept (parentResolvedPath:
		// cells T9, U1a to U1d and U2a to U2c without a worktreeRoot, cell U3a2
		// with one, Linux and macOS VMs). f6010b97 and 90fca6e6 quote "R/cp/w1" for a
		// worktreePath sent as "R/cp/w1/" with a worktreeRoot (Linux and macOS VMs).
		// On Windows the path with a worktreeRoot is the cleaned path, and the
		// in-repo path is spelled with the on-disk letter case of each component that
		// exists (existingPathSpelling, row W15).
		existing := externalPathSpelling(p.WorktreePath)
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
	// The target is confirmed missing above, so a worktree registration still
	// naming it is stale (on Windows not always, D27). Drop
	// such a registration if it is the only one and is not locked
	// (dropStaleWorktreeRegistration). Windows keeps its earlier step, which has
	// neither test.
	// A create that holds its git directory runs the step through the held
	// folder (gitdirheld.go).
	if createStepHook != nil && req.use.accepted != nil {
		createStepHook("stale")
	}
	staleMatched := dropStaleWorktreeRegistration(repo, p.WorktreePath, req.use.accepted)
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
	// The steps below test the held git directory (acceptedGitDir). The texts
	// are those of 5fd08069 on a Windows VM, with a junction at `repo\.git` to a
	// copy of the git directory (cells X1c of val-s3p2b2-7-windows). refuseAdd
	// answers one of them and removes the empty leaf: the rows have no folder
	// of the new worktree at the reply.
	refuseAdd := func(text string) response {
		undoFailedAdd(p.WorktreePath, checkpoint)
		return okResult(req.ID, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	}
	// A junction that was put there after the first or the second git call of
	// the create: the refusal of the config, with no words before it (cells
	// X1c-01 and X1c-02).
	if msg := req.use.swapRefusal("leaf", repo); msg != "" {
		return refuseAdd(msg)
	}
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
		startCommit = pickSourceCommit(req.use, repo, source)
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
		if _, ok := hardenedGit(req.use, repo, false, "show-ref", "--verify", "--quiet", "refs/heads/"+p.ExistingBranch); ok {
			attachBranch = p.ExistingBranch
		}
	}
	if startCommit == "" {
		// The fallback echoes the current branch. On a detached HEAD abbrev-ref
		// prints "HEAD", and the member is omitted (measured against f6010b97 and
		// 90fca6e6). On an unborn HEAD abbrev-ref fails, the member is omitted, and
		// the add below fails (see TestGitWorktreeCreateEmptyRepo).
		source = ""
		if s, ok := hardenedGit(req.use, repo, false, "rev-parse", "--abbrev-ref", "HEAD"); ok && s != "HEAD" {
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
	//
	// A junction that was put at `.git` before that call: the refusal of the
	// config, with the words of a git directory that cannot be located (cells
	// X1c-03 and X1c-04).
	if msg := req.use.swapRefusal("gitdir", repo); msg != "" {
		return refuseAdd(gitDirAddWrap + msg)
	}
	gitDir, gitAnswered, gitDirErr := worktreeBaseGitDir(req.use, repo)
	// Where gitDirMustBeReal is set, a failed call stops the create in the
	// state shadow. 5fd08069 then makes no other git call and answers `git
	// worktree add failed: cannot locate the repository's git directory: exit
	// status 128`. The cells are W1c and X1p-02 to X1p-05 of
	// val-s3p2b2-7-windows (Windows VM): the call exits 128 after `.git` became
	// a junction to a folder that is no git directory. The text after the
	// colon is the exec error. On a Linux VM 5fd08069 answers the same text
	// (cell V12 of round tier0-10). macOS is not measured: it gets the same
	// stop. A call that a signal ended is not measured either. In the
	// other states the create goes on with the directory of repoGitDir.
	if gitDirMustBeReal && req.use.shadow() && copyGone(gitDirErr) {
		return refuseAdd(gitDirErr.Error())
	}
	if gitDirMustBeReal && req.use.shadow() && gitDirErr != nil {
		return refuseAdd(gitDirAddWrap + gitDirErr.Error())
	}
	// After a call that answered, the path of the git directory must still name
	// the held folder. On Windows the create is refused here (cells X1c-06 and
	// X1p-06: a junction that was put at `.git` after the call). The text names
	// the git directory of the request. Two states get no refusal at this step
	// on Windows (answeredElsewhere). On Linux and macOS 5fd08069 makes more
	// calls before it refuses (cells swap-copy and swap-marker of
	// val-s3p2b2-9-macos, macOS VM): the refusal is at the step of the add
	// (swapRefusal, worktreeAdd).
	//
	// On Linux and macOS two rows of 5fd08069 come first:
	//
	//   - A `.git` that became a file with `gitdir: <X>` after the call: the
	//     create stops with `git worktree add failed: cannot locate the
	//     repository's git directory: lstat <repo>/.git/commondir: not a
	//     directory`, and that call is its last (cell swc-file of
	//     val-s3p2b2-10-linux2 and cell c-cfile of val-s3p2b2-10-macos). An
	//     lstat of `commondir` in the answered directory that fails with
	//     another error than "does not exist" gives that text. Only "not a
	//     directory" is measured.
	//   - Git answered another real directory than the held one, because the
	//     path leads to it since a step before the call: the create goes on in
	//     that directory (acceptedGitDir.movedTo).
	if gitAnswered && req.use.swapped("answered") {
		switch {
		case shadowStagesAdd && answeredElsewhere(req.use, gitDir):
			// No refusal here: the step of the add comes next.
		case shadowStagesAdd:
			return refuseAdd("git worktree add failed: " + listEntriesPrefix + realDirText(req.use.gitDir))
		default:
			if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return refuseAdd(gitDirAddWrap + err.Error())
			}
			if next := req.use.accepted.movedTo(gitDir, repo); next != nil {
				defer next.release()
				req.use.accepted = next
			}
		}
	}
	// A caller-supplied timeoutMs (4534d86) bounds the checkout, and the daemon
	// tests it after the add and after the copy step. At timeoutMs 0 both
	// contexts are the context of the request.
	baseCtx, callerCtx, cancelCtx := worktreeCreateCtx(req.stop, p.TimeoutMs)
	defer cancelCtx()
	if (req.use.shadow() || bool(p.ShadowDirOff)) && !staleMatched {
		pairBeforeAdd(req, gitDir, repo)
	}
	// The caller's deadline does not kill the add. The daemon waits until the 2
	// minute limit.
	// Measured against f6010b97 and 90fca6e6 on a macOS VM. Each failure frame quotes
	// git's stderr through worktreeGitText.
	//
	// A junction that was put at `.git` before the listing of the add: the
	// refusal of the config after `git worktree add failed: ` (cell X1c-05).
	if msg := req.use.swapRefusal("add", repo); msg != "" {
		return refuseAdd("git worktree add failed: " + msg)
	}
	// With "shadowDirOff":true, a registrations folder that is a link or a file
	// refuses the create before the add (offWorktreesRefusal). A create that
	// took the copy at a later gate has the test of worktreeAdd instead.
	if bool(p.ShadowDirOff) && !req.use.shadow() {
		if msg := offWorktreesRefusal(repo); msg != "" {
			return refuseAdd("git worktree add failed: " + msg)
		}
	}
	// incompleteAdd answers an add that git ended well and that the daemon did
	// not complete (addIncomplete), with the rollback of a failed checkout. On
	// 5fd08069 the leaf is gone and the new branch stays, with the text of the
	// branch step (cells X1c-10 and X1p-10).
	//
	// A refusal of the Windows staging step (addRefused) answers unsafe_path with
	// the same rollback. On 5fd08069 the leaf and the new branch are gone (cell
	// S2 of round task087-01, Windows VM).
	incompleteAdd := func(err error, branch string) (response, bool) {
		var incomplete *addIncomplete
		var refused *addRefused
		text, code := "", "worktree_add_failed"
		switch {
		case errors.As(err, &incomplete):
			text = completingAddPrefix + incomplete.text
		case errors.As(err, &refused):
			text, code = refused.text, "unsafe_path"
		default:
			return response{}, false
		}
		reg := testedRegistration{}
		if refused != nil {
			// The refusal comes before the staging folder is made, so the
			// rollback deletes no registration. It does not follow the
			// `.git` file of the leaf. On 5fd08069 the leaf and the branch go
			// (cell S2 of round task087-01, Windows VM).
			reg.noRegistration = true
		}
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, branch, checkpoint, reg)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      text + undo,
			ErrorCode:  code,
			BranchKept: kept,
		}), true
	}
	// claustrum lists the names in the registrations folder right before the
	// add and again after it, and on Windows after the staging move. The entry of the new worktree must
	// be the one new name (createdEntryOnly). The listing needs the git directory
	// that git answered. claustrum's own, not measured: when a listing fails with
	// another error than "does not exist", the create is refused with the text of
	// otherEntryRefusal and removes nothing. The second listing before the
	// fallback add is claustrum's own too (not measured).
	var entriesBefore []string
	entriesDir, entriesListed := "", false
	listEntriesBefore := func() {
		if (adminRecordChecked || shadowStagesAdd) && gitAnswered {
			entriesDir = gitDirRegistryDir(gitDir)
			entriesBefore, entriesListed = registrationNames(entriesDir)
		}
	}
	listEntriesBefore()
	addStderr, addErr := worktreeAdd(baseCtx, req.use, repo, p.WorktreePath, checkpoint, addArgs...)
	added = addErr == nil
	if resp, ok := incompleteAdd(addErr, createdBranch); ok {
		return resp
	}
	// An add whose repository folder went gets the rollback of a failed add,
	// with the text alone and no fallback add (from the code, not measured).
	if copyGone(addErr) {
		return refuseAdd(addErr.Error())
	}
	// A stopped attach add gets no fallback, so one add runs, as on 5fd08069
	// (cell T7 of round task010-01, Linux VM).
	if addErr != nil && attachBranch != "" && !isGitStop(addErr) {
		// A refused attach falls back to a new branch, unless the failed attach add
		// deleted the leaf or put a new directory in its place. Then the request
		// answers the attach add's own failure. Measured against f6010b97 and 90fca6e6
		// on a macOS VM, with the leaf deleted and with it replaced by a new empty
		// directory. A leaf left as it was gets the fallback. An add whose
		// worktrees folder could not be prepared gets no fallback either
		// (addNotPrepared): the frame has its text one time (cell E1 of round
		// tier0-11, Linux VM, for a link at `worktrees`).
		var notPrepared *addNotPrepared
		if verifyCreatedWorktree(p.WorktreePath, checkpoint) != "" || errors.As(addErr, &notPrepared) {
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
		listEntriesBefore()
		addStderr, addErr = worktreeAdd(baseCtx, req.use, repo, p.WorktreePath, checkpoint, fallbackArgs...)
		added = addErr == nil
		if resp, ok := incompleteAdd(addErr, p.BranchName); ok {
			return resp
		}
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
		//
		// An add that its time limit stopped gets this rollback too, with the stop
		// text. On 5fd08069 the leaf is gone, and no git call follows (cells T5 to
		// T7 of round task010-01. T5 ran on Linux, macOS and Windows VMs, T6 on
		// Linux and macOS VMs, and T7 on a Linux VM).
		undoFailedAdd(p.WorktreePath, checkpoint)
		return okResult(req.ID, worktreeResult{
			Success:   false,
			Error:     "git worktree add failed: " + worktreeGitText(addStderr, addErr),
			ErrorCode: "worktree_add_failed",
		})
	}
	// On Linux and macOS, a git directory that is no longer the held folder
	// right after a good `worktree add` refuses the create. Nothing is rolled
	// back, and no git call follows. 5fd08069 answers this text with
	// `unsafe_path` in two rows. In cell swb-wtadd of val-s3p2b2-10-linux2 and
	// cell b-postadd of val-s3p2b2-10-macos the link is put at `.git` after the
	// call ended, and the real git directory keeps the registration and the
	// branch. In cell b-precfg of val-s3p2b2-10-macos the link to a copy T is
	// put there while a wrapper holds the call before git runs, and git makes
	// the registration and the branch in T. How 5fd08069 finds that state is
	// not measured. On Windows the staged add has its own test.
	//
	// A real directory at the path of the git directory that passes the gate is
	// the exception: the create goes on in it (acceptedGitDir.replacedAtPath).
	// That is cell C6 of round tier0-24 (Linux VM), by the maintainer's decision
	// of 2026-10-14.
	if !shadowStagesAdd && req.use.swapped("added") {
		next := req.use.accepted.replacedAtPath(repo)
		if next == nil {
			return okResult(req.ID, worktreeResult{
				Success:   false,
				Error:     fmt.Sprintf("refusing to create worktree: %s was not populated by git worktree add", parentResolvedPath(p.WorktreePath)),
				ErrorCode: "unsafe_path",
			})
		}
		defer next.release()
		req.use.accepted = next
	}
	// The daemon replaces the `.git` file of a leaf that names the copy
	// (repointLeaf). It does that after the tests of the registration below,
	// before any other git call. Where those tests do not run, the step keeps
	// its place here (not measured).
	registrationTests := adminRecordChecked && gitAnswered
	if !registrationTests {
		req.use.repointLeaf(repo, p.WorktreePath, checkpoint)
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
	//
	// On Linux and macOS, and with an answer of git, the .git file of the leaf has
	// three tests of its own first (leafGitFileRefusal). A leaf with no .git file
	// is removed when it is empty, as after a failed add (cell V3a of round
	// tier0-10). Without an answer of git those tests do not run (claustrum's own,
	// not measured).
	refuseUnsafe := func(text string) response {
		return okResult(req.ID, worktreeResult{Success: false, Error: text, ErrorCode: "unsafe_path"})
	}
	var adminDir string
	if registrationTests {
		var text string
		var noFile bool
		if adminDir, text, noFile = leafGitFileRefusal(p.WorktreePath); text != "" {
			if noFile {
				undoFailedAdd(p.WorktreePath, checkpoint)
			}
			return refuseUnsafe(text)
		}
	} else {
		adminDir = worktreeAdminDir(p.WorktreePath)
	}
	indexDir := ""
	if adminDir != "" {
		indexDir = absoluteAdminDir(p.WorktreePath, adminDir)
	}
	var tested testedRegistration
	// newEntry is the one new name of the registrations folder since the listing
	// before the add, or "" when there is none or more than one. It is "" too
	// when a listing did not run, or when the two listings read two folders. The
	// folder of the second listing comes from a second read of the commondir
	// file of gitDir. If its path is not the path of the first listing, newEntry
	// is "" (claustrum's own, not measured). ownEntry says that the registration
	// of the tests has that name. created is the registration of a rollback
	// after a failed spelling call. For another name than that of the tests, it
	// takes the identity of the folder at newEntry right after the listing.
	entryTested, ownEntry, newEntry := false, false, ""
	var created testedRegistration
	if gitAnswered {
		var text string
		if indexDir, text = createdRegistrationRefusal(gitDir, p.WorktreePath, adminDir); text != "" {
			return refuseUnsafe(text)
		}
		tested = acceptRegistration(indexDir)
		tested.git = req.use.accepted
		defer tested.release()
		if adminRecordChecked && indexDir != "" {
			after, listed := registrationNames(filepath.Dir(indexDir))
			entryTested = true
			if entriesListed && listed && filepath.Dir(indexDir) == entriesDir {
				newEntry, _ = newRegistrationName(entriesBefore, after)
				ownEntry = createdEntryOnly(entriesBefore, after, indexDir)
			}
		}
		created = tested
		if newEntry != "" && !ownEntry {
			created = acceptRegistration(filepath.Join(entriesDir, newEntry))
			created.git = req.use.accepted
			defer created.release()
		}
	}
	// On Windows the registration of the staging step is the one that gets the
	// index and that a rollback removes, by its identity (stagedRegistration).
	// A create whose rev-parse gave no answer (gitAnswered false) has no
	// listing before the add, so it gets the refusal of otherEntryRefusal. That
	// is claustrum's own, not measured.
	if moved := req.use.stagedRegistration(); moved.path != "" {
		if text := stagedRecordRefusal(req.use.accepted, p.WorktreePath); text != "" {
			return refuseUnsafe(text)
		}
		after, listed := registrationNames(entriesDir)
		if !entriesListed || !listed || !createdEntryOnly(entriesBefore, after, moved.path) {
			return refuseUnsafe(otherEntryRefusal(p.WorktreePath))
		}
		indexDir, tested = moved.path, moved
	}
	// indexDir is the registration that gets the index (createdRegistrationRefusal, or
	// absoluteAdminDir if rev-parse gave no answer).
	// Its gitdir record names worktreePath after symlink resolution.
	// If it was read and names anything else, the create is refused, with no checkout
	// and no rollback (cells P-c and P-k to P-m). Before
	// the answer, 89cb6289 runs the pair of gitDirWorkTreeToplevel with baseRepo
	// as the work tree (row I07a, macOS VM, and cell P-c, Linux and macOS VMs). In
	// cells T2 and T10 (Linux and macOS VMs) the call log of 89cb6289 shows that
	// pair twice, with baseRepo as the resolved path. The call log of claustrum
	// shows it once, with baseRepo as sent. No frame differs.
	// This test comes before
	// the deadline test too: with a timeoutMs that expired during the add, 89cb6289
	// answers this refusal and keeps the leaf and the branch (cell P-f, Linux and macOS VMs).
	//
	// When the second call of the pair fails, 5fd08069 answers the text of
	// spellingRetryText and removes the leaf, the new registration and the new
	// branch (cell V5 of round tier0-10, Linux VM). The text has the name that
	// the .git file of the leaf gives. In claustrum the registration that goes is
	// the one new name of the registrations folder since the listing before the add
	// (newEntry). In cell P1 of round tier0-11 (Linux VM, 3 of 3 runs) the .git
	// file names an old entry w1 of another live worktree, and git made the entry
	// w11. 5fd08069 answers "(entry w1)" and removes the leaf, w11 and the new
	// branch. The entry w1, its worktree and its branch stay. The rollback is
	// that of a failed checkout (undoFailedCheckout), with its home guard, its
	// identity tests and the held git directory. For another name than that of
	// the tests, the identity is that of the folder at the name in the listing
	// after the add.
	//
	// Two conditions are claustrum's own, not measured. With no new name, or
	// with more than one, the create keeps the refusal, and nothing is removed.
	// And the call must fail by itself. A call that its time limit stopped keeps
	// the refusal too: a stop is not a refusal of git. The timeoutMs of the
	// caller does not bound that call. The undo text after
	// the frame is not measured either. What 5fd08069 takes from an answer of the
	// call is not measured: claustrum does not use it. With no answer of git to
	// rev-parse --absolute-git-dir the record compare is as before (not measured).
	if adminRecordMismatch(indexDir, p.WorktreePath, gitAnswered) {
		pairUse := req.use.atGitDirCommand()
		_, err := gitDirWorkTreeToplevelIn(req.stop.context(), gitDir, repo, pairUse.gitDirPin(repo), pairUse.gitDirFront())
		// A call whose repository folder went answers its text, with the same
		// rollback (from the code, not measured).
		if copyGone(err) {
			undo, kept := "", false
			if newEntry != "" {
				undo, kept = undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, created)
			}
			return okResult(req.ID, worktreeResult{Success: false, Error: err.Error() + undo,
				ErrorCode: "worktree_add_failed", BranchKept: kept})
		}
		if err != nil && !isGitStop(err) && newEntry != "" {
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, created)
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      spellingRetryText(indexDir) + undo,
				ErrorCode:  "worktree_add_failed",
				BranchKept: kept,
			})
		}
		return refuseUnsafe(adminRecordRefusal(p.WorktreePath))
	}
	// The entry of the tests must be the one new name of the registrations
	// folder since the listing before the add (createdEntryOnly). A refusal of
	// the tests above comes first (claustrum's order, not measured).
	if entryTested && !ownEntry {
		return refuseUnsafe(otherEntryRefusal(p.WorktreePath))
	}
	// The leaf must still be the folder that the create made, before any file is
	// checked out. With a new folder at the path that holds a copy of the .git
	// file, 5fd08069 answers this text, and the new folder holds .git only (cell V8
	// of round tier0-10, Linux VM). Nothing is removed. A leaf whose path leads
	// elsewhere or is no folder gets the same text (claustrum's own, not
	// measured). Without an answer of git to rev-parse --absolute-git-dir, and
	// on Windows, the test stays after the checkout alone, with its old frame
	// (not measured).
	if registrationTests {
		if verifyCreatedWorktree(p.WorktreePath, checkpoint) != "" {
			return refuseUnsafe(fmt.Sprintf("refusing to create worktree: %s is no longer the directory that was created for the worktree",
				parentResolvedPath(p.WorktreePath)))
		}
		req.use.repointLeaf(repo, p.WorktreePath, checkpoint)
	}
	// The read of the `config.worktree` file of the repository, and the text
	// that the new worktree gets from it (newWorktreeConfig). The text goes into
	// the registration below.
	entryConfig, hasEntryConfig, dropConfigDir, configCause, configWritten := req.newWorktreeConfig(repo, t)
	defer dropConfigDir()
	// A failure of that step fails the create with no checkout, and the undo of a
	// failed checkout runs (round task007-01, Linux and macOS VMs, and cell C1 on
	// a Windows VM). Its branch step reads the config of the repository again
	// first (branchGate). With a refusal there it keeps the branch with that
	// refusal as its cause, as 5fd08069 does in cells C9 and C11 (Linux and
	// macOS VMs).
	filesFailed := func(cause string) response {
		dropConfigDir()
		u := req.use
		u.branchGate = func() string {
			msg, _ := repoConfigGate(t, false, req.stop)
			return msg
		}
		undo, kept := undoFailedCheckout(u, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      completingAddPrefix + cause + undo,
			ErrorCode:  "worktree_add_failed",
			BranchKept: kept,
		})
	}
	if configCause != "" {
		return filesFailed(configCause)
	}
	// From here on the repository has the registration. A git directory that is
	// no longer the held folder fails the checkout with the refusal of its
	// config, and the rollback keeps the registration and the branch. With a
	// junction at `.git` after the first listing in the new worktree, 5fd08069
	// answers `git worktree add failed (checkout): this repository's git config
	// cannot be used: … a link …; and the undo could not finish for <leaf>: the
	// worktree registration and the branch remain; remove them by hand before
	// retrying (".git" is a link or a file …)`. The leaf is gone and the
	// registration stays (cells X1c-11 and X1p-11 of val-s3p2b2-7-windows,
	// Windows VM). claustrum tests at two steps before the listings of the
	// checkout: before the file of the registration and before the first
	// listing. Neither is a row of 5fd08069: that build makes no call between
	// its `worktree add` and that listing. Two more tests are in the checkout:
	// one on Windows, and one on every system (errGitDirChanged).
	// goneCheckout answers a checkout that stops for such a git directory, with
	// detail after the words of the checkout.
	goneCheckout := func(detail string) response {
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      "git worktree add failed (checkout): " + detail + undo,
			ErrorCode:  "worktree_add_failed",
			BranchKept: kept,
		})
	}
	swappedCheckout := func(step string) (response, bool) {
		if !req.use.swapped(step) {
			return response{}, false
		}
		return goneCheckout(repoConfigPrefix + gitDirLinkText(req.use.gitDir)), true
	}
	if resp, ok := swappedCheckout("registration"); ok {
		return resp
	}
	if hasEntryConfig {
		if cause := placeWorktreeConfig(req.use, indexDir, p.WorktreePath, tested, entryConfig, configWritten); cause != "" {
			return filesFailed(cause)
		}
	}
	dropConfigDir()
	// If the caller's deadline expired during the add, the request
	// answers timeout "before the checkout started" and rolls back. errorCode
	// "timeout" is reserved for the caller's own deadline.
	// Every rollback below appends an undo text when one of its steps fails.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired before the checkout started)", p.TimeoutMs)
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
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
		// The filter rule runs in the checkout (gitfilter.go). 5fd08069 refuses after
		// its read-tree call, and rolls back as after a failed checkout (cell F1,
		// Linux VM). The refusal of a name over the bound has the same end state (cell
		// D5), and 5fd08069 makes no read-tree call for it (Windows VM). The refusal
		// of a name with "=" (cell Q3, Linux VM) and claustrum's own refusal of a
		// filter that no pair can replace come before the read-tree call. In the
		// state shadow they come after its second listing and the cap
		// (hardenedGitCheckout). In the state fallback-git and with
		// "shadowDirOff":true the rule does not run (filterRule).
		if resp, ok := swappedCheckout("checkout"); ok {
			return resp
		}
		// The checkout takes the form of the off switch at its first listing and
		// keeps it to its end (fixedForm, claustrum's own, not measured: no cell
		// changes the hold inside the checkout). Under a live hold 5fd08069 puts
		// the index in a new folder `claude-ssh-index-<n>` of the temporary
		// folder and runs one listing before the read-tree call. With no hold it
		// puts the index in `.index-<n>` of the folder of the daemon and runs two
		// (cells C1 to C3 and C0 of round task105-01, Linux VM). The filter rule
		// follows that form too (claustrum's own, not measured). The index goes
		// into the registration through the held git directory, as before.
		cu, offNow := req.use.fixedForm(false)
		offNow = offNow || (!cu.shadow() && bool(p.ShadowDirOff))
		var rule *filterRule
		if !offNow && req.shadow.readsRepoConfig() {
			rule = &filterRule{}
			if cu.shadow() && cu.read != nil {
				rule.shadow, rule.own = true, cu.read.ownConfig
			}
		}
		rtStderr, rtDrained, rtErr, installErr := runWorktreeCheckout(callerCtx, cu, p.WorktreePath,
			checkoutWorkTree(p.WorktreePath, checkpoint.resolved), gitDir,
			indexDir, tested, checkoutRev, cu.gitDirPin(repo), rule)
		var filters filterPlan
		if rule != nil {
			filters = rule.plan
		}
		refusedName, filterRefused := filters.refusedFilter(rtStderr)
		var refusal checkoutRefusal
		switch {
		case errors.As(rtErr, &refusal):
			// The second listing of the checkout failed, the repository has too
			// many filters, or, in the state shadow, the plan has a refusal.
			// 5fd08069 runs no read-tree call there and rolls back. Cells C2 and
			// C10 of round task008-01 show it on Linux, macOS and Windows VMs.
			return goneCheckout(string(refusal))
		case errors.Is(rtErr, errGitDirChanged):
			return goneCheckout(repoConfigPrefix + gitDirLinkText(req.use.gitDir))
		case errors.Is(rtErr, errGitDirNotReal):
			return goneCheckout(realDirText(req.use.gitDir))
		case installErr != nil:
			// git exited 0, and the index did not reach the registration. On Linux and
			// macOS VMs 89cb6289 answers that as a failed checkout and rolls back (rows
			// A14, A14f and A14b, and cell X1). The text is indexInstallText. A placement that
			// fails after a drain overrun takes this arm too. That is claustrum's
			// choice (not measured).
			msg := "git worktree add failed (checkout): " + indexInstallText(rtStderr, installErr)
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
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
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "timeout",
				BranchKept: kept,
			})
		case rtErr != nil && filterRefused:
			// git failed at a path that uses a filter with the three pairs. The text
			// names that filter (cells A1 and N1 to N4, Linux VM, and cells A1, N1
			// and N2, Windows VM). The deadline arm above comes first. A deadline that
			// expires before git answers gives the timeout frame (cells Q13). The order
			// of the two arms after a filter failure is not measured.
			// One name has another answer with no settings copy: the filter lfs of
			// the outside config, when the repository names a program for git-lfs
			// (claustrum's own).
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			if refusedName == lfsFilterName && filters.lfsKey != "" {
				return okResult(req.ID, worktreeResult{
					Success:    false,
					Error:      "git worktree add failed (checkout): " + lfsProgramText(filters.lfsKey) + undo,
					ErrorCode:  "worktree_add_failed",
					BranchKept: kept,
				})
			}
			return okResult(req.ID, worktreeResult{
				Success:    false,
				Error:      repositoryOnlyFilterText(refusedName) + undo,
				ErrorCode:  "repository_only_filter",
				BranchKept: kept,
			})
		case rtErr != nil:
			// The checkout failed on its own (for example a tree object it cannot
			// read). The request fails with git's text, and the rollback removes the
			// new directory and its registration. The branch step then deletes the
			// branch this call created only when another ref reaches its tip. The
			// read-tree passes -c advice.graftFileDeprecated=false (gitHardenLight), so
			// git's graft-file hint: lines do not start the text. On f6010b97 the text
			// starts with the hint where git prints it. In attach mode
			// createdBranch is "", so the attached branch is kept, as measured against
			// f6010b97. With a settings copy a text that holds lfsFailedWords gets
			// lfsDownloadNote after it, as on 5fd08069 (round task081-01, Linux VM).
			// The note goes only in this reply, before any undo text (claustrum's
			// own, not measured beside a failed undo).
			msg := "git worktree add failed (checkout): " + worktreeGitText(rtStderr, rtErr)
			if rule != nil && rule.shadow && strings.Contains(msg, lfsFailedWords) {
				msg += lfsDownloadNote
			}
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
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
	populateWorktree(req.use, req.shadow, repo, p.WorktreePath)
	// The caller's deadline does not kill the copy step. The daemon lets the step
	// finish and then tests the deadline. If the deadline expired, the request answers
	// timeout "after the checkout finished" and rolls back. Measured against f6010b97
	// and 90fca6e6 on a macOS VM.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired after the checkout finished)", p.TimeoutMs)
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return okResult(req.ID, worktreeResult{
			Success:    false,
			Error:      msg + undo,
			ErrorCode:  "timeout",
			BranchKept: kept,
		})
	}
	return okResult(req.ID, worktreeResult{Success: true, Path: p.WorktreePath, SourceBranch: source, Branch: worktreeBranch})
}

// entryIsLink reports whether repo is a linked worktree whose `.git` file names
// an entry of the registrations folder that is a symbolic link, where
// entryLinkRefusedLate is set. The daemon's own GIT_DIR is not looked at.
//
// For such a baseRepo, with extensions.worktreeConfig set and a
// `config.worktree` file in the entry, 5fd08069 answers the trust refusal of
// the entry behind the link after `git worktree add failed: cannot locate the
// repository's git directory: `. B/.claude and B/.claude/worktrees exist after
// the reply (cell C7 of round tier0-24, Linux VM). It makes 16 git calls before
// that reply. claustrum runs no git call in a folder that the trust check
// refused, so it answers at the gate (makeRefusedLeafParents). The caller
// applies it where the settings copy can run: a server with a run folder, not
// in the state fallback-git, and no "shadowDirOff":true. The other states keep
// the earlier answer (not measured). Not measured either: the same link without
// that switch or that file. It gets the same answer.
func entryIsLink(repo string) bool {
	if !entryLinkRefusedLate || os.Getenv("GIT_DIR") != "" {
		return false
	}
	start, err := walkStart(repo)
	if err != nil {
		return false
	}
	if start, err = filepath.Abs(start); err != nil {
		return false
	}
	g, _, ok := findGitDir(start)
	if !ok || !isWorktreeEntry(g) {
		return false
	}
	fi, err := os.Lstat(g)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// makeRefusedLeafParents makes the parent folders of the new leaf of a create
// that entryIsLink refuses, as a create makes them before its add. It runs
// only without worktreeRoot, and only when the containment tests of the create
// pass. An error is not reported. claustrum's own, not measured: with
// worktreeRoot, or when a test fails, nothing is made.
func makeRefusedLeafParents(p *gitParams, repo string) {
	if p.WorktreeRoot != "" || p.WorktreePath == "" ||
		worktreePathRefusal(p.BaseRepo, p.WorktreePath, "create") != "" ||
		worktreeSymlinkRefusal(repo, p.WorktreePath, "create") != "" {
		return
	}
	if _, err := os.Lstat(p.WorktreePath); err == nil {
		return
	}
	_ = mkdirRepoWorktreeParents(repo, filepath.Dir(filepath.Clean(p.WorktreePath)))
}

// removeGate is the settings gate of a git.worktree_remove without worktreeRoot
// (settingsGateRefusal). After a bad answer of git about the repository, the
// gate runs one more time, and its second result is the answer. On 5fd08069 a
// remove makes that call two times for each bad answer, where the other
// methods make it one time (cells 38 and 39 of s3p2b-edges-linux2, Linux VM).
// With an ended call the reply comes after both calls, at 62.02 s (cell 38).
//
// A refused config runs the gate one more time too. On 5fd08069 a remove in a
// repository with format version 3 makes two reads of the config, where each
// other method makes one. With a syntax error it makes the read and its `git
// version` call two times (cells x9-v3-own and x9-syntax-own of
// val-s3p2b2-5-linux2, Linux VM, 3 of 3 runs. Rows E1 and E1b of
// val-s3p2b2-5-macos, macOS VM). A refusal of another file of the git directory
// takes the same way: it makes no git call (not measured).
//
// After a gate that passed, the later git commands follow the off switch
// (cells R1 and R2 of round task105-01, Linux VM). A later gate runs one time
// (claustrum's own, not measured).
func (req *request) removeGate(t gitDirTrust, off jsonTrueSwitch, repo string) (msg string, noRepo bool) {
	msg, noRepo = req.settingsGateRefusal(t, off, repo)
	if req.gitRefused || req.configRefused {
		msg, noRepo = req.settingsGateRefusal(t, off, repo)
	}
	if msg == "" && !noRepo {
		req.keepLiveForm(bool(off), t, repo)
	}
	return msg, noRepo
}

func gitWorktreeRemove(req *request) response {
	p, bad := bindGitParams(req, &removeViewParams{})
	if bad != nil {
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
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "remove", false); msg != "" {
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
		msg, topLevel := externalWorkTreeRefusal(req, repo, p.ShadowDirOff)
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
		// measured the same. After a failure of `worktree list -z`, the
		// daemon runs `worktree list --porcelain` with its listing. If that call fails
		// too, the removal is refused and nothing is deleted (89cb6289, row DG2s-g on a
		// Linux VM). See worktreeListForRemove.
		listed, msg := worktreeListForRemove(req.use, repo)
		if msg != "" {
			return refuse(msg)
		}
		// The loop and filesystem-root refusals, the repository behind a link, the
		// checkout tests and a <directory> level in a listed worktree come before the
		// remove reads the root. Nothing is deleted (5fd08069, cells C-S4 to C-S8 and
		// C-W23 of round tier0-20, Linux VM).
		if msg := locationLinkRefusal(p.WorktreeRoot, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "remove", true); msg != "" {
			return refuse(msg)
		}
		if msg, _ := worktreeRootCheckoutRefusal(p.WorktreeRoot, newRepoCheckouts(repo, p.BaseRepo, topLevel, listed), "remove", false); msg != "" {
			return refuse(msg)
		}
		if msg := repoFolderInWorktreeRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, listed, "remove"); msg != "" {
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
		if out, err := hardenedGitStdout(req.use, repo, true, "rev-parse", "--absolute-git-dir"); err == nil {
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
		t := requestGitDirTrust(repo, false)
		verdict := t.verdict
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
		// The daemon's own read of the repository's config comes before the listing
		// (repoConfigGate). The worktree stays (cells W7 and X1, Linux VM). The
		// path is worktreePath as sent, and it is not cut (cells X12). A worktree
		// folder that is gone gets the same refusal (removeGoneWorktree). With
		// worktreeRoot the read is in externalWorkTreeRefusal, with its own text.
		if msg, noRepo := req.removeGate(t, p.ShadowDirOff, repo); msg != "" {
			return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
		} else if noRepo {
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
			c := hostileConfigRefusal(req.use, repo, true)
			var checkErr error
			if !c.refused() {
				answered, checkErr = repositoryGitDir(req.use, repo, &c.listing)
			}
			// A call whose repository folder went answers its text after the words
			// of the lock check, as the text of `repoLimitError` does, with no
			// repeat (from the code, not measured).
			if c.gone {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + c.refusal)
			}
			if copyGone(checkErr) {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + checkErr.Error())
			}
			if c.refused() || checkErr != nil {
				repeatRepositoryCheck(req.use, repo)
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
	probe := newRegistrationProbe(req.use, answered, target.path)
	if p.WorktreeRoot == "" {
		commonDir = registrationGitDir(repo, answered)
		probe = newRegistrationProbe(req.use, answered, inRepoWorkTree(repo))
	}
	// A registrations folder that is a symbolic link refuses the remove, and
	// nothing is deleted (registrationsLinked). The test comes before the lock
	// tests: with a locked entry behind the link, and with worktreeRoot,
	// 5fd08069 answers this refusal too (cells L2 and L3 of round tier0-26,
	// Linux VM).
	if registrationsLinked(filepath.Join(commonDir, worktreesSubdir)) {
		return refuse(lockCheckRefusal(p.WorktreePath))
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
		repeatRepositoryCheck(req.use, repo)
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
	//
	// The entry, the branch step and the reply wait for the `worktree add` of a
	// create in the same git directory, and the tree does not (lockWorktreeAddStep).
	defer lockWorktreeAddStep(repo)()
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
	kept := runBranchStep(req.use, repo, p.BranchName).kept()
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
		// The daemon's own read of the repository's config refuses here too
		// (repoConfigGate), with the text of a worktree folder that exists. No
		// registration and no branch is deleted. 5fd08069 answers so on a Windows VM
		// (cells F1a, F1b, F2a, F2b, G1a to G1c, W6, W6x and X12), where 89cb6289
		// answers success. It deletes the branch with the files of 20 MiB and with
		// the absolute include (cells G1a to G1c), and keeps it with format version
		// `2`. Linux and macOS VMs measured the
		// same refusal in cells N1. Their states are format version `2`, a config of
		// 20 MiB and an info/exclude of 20 MiB. With worktreeRoot the read came
		// before this function (externalWorkTreeRefusal, cells N14).
		if msg, noRepo := req.removeGate(t, p.ShadowDirOff, repo); msg != "" {
			return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
		} else if noRepo {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		switch t.verdict {
		case gitDirNoRepo:
			// 89cb6289 runs the listing and the rev-parse here too, and the rev-parse
			// fails (probe row 16 on Linux, macOS and Windows VMs). claustrum does not
			// use the answer of the rev-parse. A listing that fails refuses, and nothing
			// is deleted (row A-N4 on Linux and macOS VMs, cell P3-rmN on a Windows VM).
			// Without the entry of noRepoPinned the answer of the listing is not read.
			c := hostileConfigRefusal(req.use, repo, true)
			// git 2.56 fails the listing of a `.git` file to a gone target that is no
			// entry with its gitfile text. 5fd08069 answers the error of its call about
			// the repository then (cell N04 of round tier0-27, Linux VM). With
			// "shadowDirOff":true it answers the listing refusal in the same frame
			// (cell N04OFF of round task039-03, Linux VM). A gone entry keeps the
			// refusal below (not measured).
			if c.gitFileText != "" && goneNonEntryGitFile(repo) {
				text := c.gitFileText
				if p.ShadowDirOff {
					text = c.refusal
				}
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + text)
			}
			if c.refusal != "" && t.noRepoPinned() {
				return lockCheck(c.refusal)
			}
			if !c.refused() {
				noRepositoryAt(req.use, repo, c.listing)
			}
			checkRegistration = false
		default:
			// A listing that answers "no repository" skips the registration. The branch
			// step then runs its own listing, which fails the same way, and keeps the
			// branch with no other call. 89cb6289 runs those two listings and nothing
			// else (rows L14a, L14b, L14d, L14e and N01 to N04 on Linux and macOS VMs,
			// row P7 on Windows).
			c := hostileConfigRefusal(req.use, repo, true)
			if c.refusal != "" {
				return lockCheck(c.refusal)
			}
			if !c.noRepo {
				var checkErr error
				answered, checkErr = repositoryGitDir(req.use, repo, &c.listing)
				checkRegistration = checkErr == nil
			} else {
				checkRegistration = false
			}
		}
	} else if msg, bad := daemonCountRefusal(); bad {
		return lockCheck(msg)
	}
	// The registration, the branch step and the reply wait for the `worktree add`
	// of a create in the same git directory (lockWorktreeAddStep). With no
	// worktree folder that is not measured.
	defer lockWorktreeAddStep(repo)()
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
			probe = newRegistrationProbe(req.use, answered, inRepoWorkTree(repo))
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
		// gone target). Without worktreeRoot no row measures it. A worktrees
		// directory that is a symbolic link is read and deleted in no place
		// (openRegistrationsFolder), and the remove goes on. With a locked entry
		// behind the link, 5fd08069 answers success and deletes the branch, and
		// the registration stays (cell L1 of round tier0-26, Linux VM).
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
	kept := runBranchStep(req.use, repo, p.BranchName).kept()
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
	return adminRecordNames(adminDir, b, worktreePath), nil
}

// adminRecordNames reports whether b, the text of the `gitdir` record of
// adminDir, names the `.git` file of worktreePath (worktreeAdminBelongsTo).
func adminRecordNames(adminDir string, b []byte, worktreePath string) bool {
	record := strings.TrimSpace(string(b))
	if !filepath.IsAbs(record) {
		record = filepath.Join(adminDir, record)
	}
	// sameCanonicalPath, not ==: on Windows git writes this record with forward slashes,
	// and once the worktree is deleted neither side can be resolved, so the two
	// spellings differ only in slash direction. Measured against f6010b97 on a
	// Windows 11 VM, where the reference deletes the entry.
	return sameCanonicalPath(canonicalPathOfGone(record),
		canonicalPathOfGone(filepath.Join(worktreePath, ".git")))
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
