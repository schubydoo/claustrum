package git

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
	"unicode/utf16"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/pathcanon"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// Handle answers a request of the five git methods with the daemon state in d.
func Handle(d *Deps, r *rpc.Request) rpc.Response {
	req := &gitCall{Request: r}
	var fn func(*gitCall) rpc.Response
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
		return rpc.UnknownMethod(req.Request)
	}
	if bad := rpc.NeedParams(req.Request); bad != nil {
		return *bad
	}
	req.shadow = d.Shadow
	// The daemon-wide window of gitWideLimit counts this request while it runs.
	if offSwitchSet(req.Params) {
		d.OffRequests.Add(1)
		defer d.OffRequests.Add(-1)
	}
	// While a request with "shadowDirOff":true runs, every git request that
	// starts, and each later git command of a request, run as with the member
	// (measured on Linux). The count is read live (gitCall.offLive).
	req.offHold = d.OffRequests
	if !limitedGitMethod(req.Method) {
		return fn(req)
	}
	req.stop = &gitStop{wide: d.OffRequests}
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
	// the repository's filter and succeeds (measured on Linux). It also turns off the
	// daemon's own read of the repository's config on the five git methods
	// (repoConfigGate). Measurements show that on all five methods. It turns off the
	// settings copy for the request too (ShadowHost.control). Only the JSON value
	// true sets it (rpc.JSONTrueSwitch). A request whose first git command starts while
	// another one with it runs gets it too (bindGitParams). The five methods read the
	// switch again at later commands (gitUse.atCommand).
	ShadowDirOff rpc.JSONTrueSwitch `json:"shadowDirOff,omitempty"`
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
	cmd := gitCommandContext(ctx, full...)
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
	out, err := gitCommand(full...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

// worktreeCreateCtx builds the two contexts of git.worktree_create.
//
// base carries the time limits of the request (gitStop.context). The add runs
// under base only, so the caller's timeoutMs never ends the add. A held add
// gets the stop at 120 s, also with a timeoutMs of 5000 (measured on Linux
// and macOS).
//
// caller is base plus the caller's timeoutMs (4534d86 parity). The read-tree
// checkout runs under caller, so the caller's deadline kills the checkout. The
// daemon also tests caller after the add and after the copy step. When timeoutMs
// is 0 or less, caller is base itself. The deadline counts from arrival. The stop of the settle call that follows from it
// is measured on Linux (683cc6a9). The other three tests of timeoutMs, before,
// during and after the checkout, count from arrival too (claustrum's own, not
// measured). A deadline that passed already gives a caller that is done.
//
// errCallerTimeoutMs is the cause stamped on the caller's deadline
// (callerTimeoutFired).
var errCallerTimeoutMs = errors.New("git.worktree_create caller timeoutMs deadline")

// callerDeadline arms the caller's deadline on base. It is a variable so that a
// test can also fire the deadline at a marker of its git stub.
var callerDeadline = func(base context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(base, d, errCallerTimeoutMs)
}

func worktreeCreateCtx(s *gitStop, timeoutMs int, arrival time.Time) (base, caller context.Context, cancel context.CancelFunc) {
	base = s.context()
	if timeoutMs <= 0 {
		return base, base, func() {}
	}
	ctx, c := callerDeadline(base, time.Duration(timeoutMs)*time.Millisecond-time.Since(arrival))
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
func copyGoneResult(req *gitCall, err error) rpc.Response {
	return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: err.Error(), ErrorCode: "worktree_add_failed"})
}

func gitInfo(req *gitCall) rpc.Response {
	p, bad := bindGitParams(req, &infoViewParams{})
	if bad != nil {
		return *bad
	}
	// The excludesFile the reference reads at git.info is now cached
	// and applied to every git op via hardenedProfileArgs/globalExcludesPath, rather than
	// probed here and discarded. The read comes first, so it also runs before a trust
	// refusal, as on f6010b97 and 89cb6289 (every trust refusal row).
	globalExcludesPath()
	// On Windows a path behind a chain of 32 or more links is refused next, as in
	// git.status (measured on Windows, statusLinkChainRefusal).
	if p.Path != "" {
		if msg := statusLinkChainRefusal(p.Path); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
		// On Windows a path through a link to a network or device path is refused
		// before any git call on it (measured on Windows, networkLinkRefusal). Its
		// place after the link chain test is claustrum's own, not measured.
		if msg := networkLinkRefusal(p.Path); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
	}
	// notRepo is the "no repository" answer. It carries the settingsControl member
	// that the request has at that moment (ShadowHost.replyControl): none before a
	// kept start test, as for a HEAD that is no ref as the first request (measured
	// on Linux).
	notRepo := func() rpc.Response {
		return rpc.OKResult(req.ID, rpc.NotRepoReply{SettingsControl: req.shadow.replyControl(req.offLive)})
	}
	// The git-directory trust check runs on path (gitdirtrust.go). The check of the
	// daemon's own GIT_CONFIG_COUNT comes after a trust refusal and before the "no
	// repository" answer (docs/protocol/git-rules-calls.md → "Where each method answers the
	// refusal").
	t := readGitDirTrust(p.Path, true)
	// With "shadowDirOff":true a daemon GIT_DIR keeps its verdict on Windows
	// (gitDirTrustFor, 87375cc7, measured on Windows).
	offDaemon := bool(p.ShadowDirOff) && daemonGitDirSkipped()
	if offDaemon {
		if d := daemonGitDirVerdict(p.Path); !d.unsearchable {
			t = d
		}
	}
	if t.verdict == gitDirRefused {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		// A `.git` file to a regular file answers "no repository" (gitDirTrust.fileGitDir).
		if t.fileGitDir {
			return notRepo()
		}
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// A path that does not exist gets the "no repository" answer, with no start test and no git call beyond
	// the excludes read of a first request, except below a repository on a share (askForMissingOnShare). It first takes the steps of a folder with no repository up to
	// the start of the config listing (missingPathListing). That start fails on the missing folder. These
	// steps are claustrum's own and are not measured. The place of this test after the count refusal is not
	// measured. On 5fd08069 such a git.info as the first git request of a daemon makes 1 call, the excludes
	// read, and its reply has no settingsControl member. The next request on a repository runs the start
	// test. A later git.info on such a path makes 0 calls (measured on Windows, 2 of 2 runs, and in one
	// more group of 15 runs). A Linux test has the same counts for a missing path, a path with a missing
	// parent and a dangling link. An empty path makes 3 calls there, and 2 later (measured). A macOS makes
	// 1 and 0 calls for a missing path (measured). A path that is a file is not such a path: both builds
	// run a `git version` call there and answer the hooks refusal (measured).
	if p.Path != "" {
		if _, err := os.Stat(p.Path); errors.Is(err, fs.ErrNotExist) {
			_ = missingPathListing(p.Path)
			// On Windows a missing path below a repository on a share asks git
			// about that repository first (askForMissingOnShare).
			if a, _ := req.askForMissingOnShare(p.Path, bool(p.ShadowDirOff)); a.text != "" {
				return rpc.ErrResult(req.ID, rpc.CodeInternal, a.text)
			}
			return notRepo()
		}
	}
	if t.verdict == gitDirNoRepo {
		// A `.git` file to a missing path runs the start test, and the reply has
		// the member. 5fd08069 does so as the first git request of a daemon
		// (measured on Linux, macOS and Windows). A missing entry under `worktrees`
		// of an existing git directory runs no test (measured). One in a missing
		// repository runs it (measured on Linux). Not measured: the other methods
		// there other than a create (gitWorktreeCreateLocked), a failed test there,
		// and a host with no git. claustrum runs no test in those other methods, and
		// none with no git. A failed test answers its text.
		if t.goneGitFile && gitLookupError() == nil {
			if msg := req.shadow.gate(bool(p.ShadowDirOff)); msg != "" {
				return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
			}
			// In the state shadow the call about the repository runs in the folder
			// that holds `.git`, in place of the listing. c8e2f083 runs it, then
			// its second run with GIT_DIR=<.probe>, which exits 0, and answers "no
			// repository" (measured on Linux). A failed second run answers its
			// error. A second run that exits 0 gets the third run
			// (configFreeProbe). Both are claustrum's own here, not measured.
			if !bool(p.ShadowDirOff) && req.shadow.control(false) == controlShadow {
				if a := req.shadow.askGit(t, p.Path); a.text != "" {
					return rpc.ErrResult(req.ID, rpc.CodeInternal, a.text)
				}
				return notRepo()
			}
		}
		// A listing that fails here refuses (measured on Linux, macOS and Windows).
		if msg := noRepoInfoCalls(t, req.stop, p.Path, bool(p.ShadowDirOff)); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
		return notRepo()
	}
	// On Windows GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment and
	// then the alternates file are checked before the next git call
	// (alternateEnvRefusal, alternatesRefusal). The order of the two is claustrum's
	// own, not measured.
	if msg := alternateEnvRefusal(bool(p.ShadowDirOff)); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// On Windows a folder whose stat fails with the error of startNameRefuses
	// gets the hooks refusal with its chdir error next, before the start test.
	// 87375cc7 makes the excludes read only as the first request of a daemon,
	// and none after a git.info on a repository (measured on Windows). The place
	// after the link tests, the trust check, the count and the refusal of
	// GIT_ALTERNATE_OBJECT_DIRECTORIES is claustrum's own, not measured. The trust
	// check cannot resolve such a folder and leaves it to git (gitDirTrustFor), so
	// no step before it makes a git call (from the code).
	if p.Path != "" {
		if _, err := os.Stat(p.Path); startNameRefuses(err) {
			var pe *fs.PathError
			if errors.As(err, &pe) {
				return rpc.ErrResult(req.ID, rpc.CodeInternal, hooksRefusalPrefix+(&fs.PathError{Op: "chdir", Path: p.Path, Err: pe.Err}).Error())
			}
		}
	}
	if msg := alternatesRefusal(t.pinCommonDir, false); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
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
	//     git.info on the repository makes 8 calls (measured on Linux).
	//   - Outside any repository: no start test. As the first git request,
	//     5fd08069 makes 2 calls, the excludes read and `version`. The reply has
	//     no settingsControl member, and no folder `git-shadow` is made. The
	//     next git.info on a repository runs the start test (measured on macOS,
	//     2 of 2 runs).
	//
	// The calls after the gate keep their form: the request has no copy for
	// them. A measurement has both rows on Linux and macOS, and a refused
	// config of the repository of the file (measured on Linux). On Windows
	// both builds answer the hooks refusal for such a file, with `fork/exec
	// <git.exe>: The directory name is invalid.` (measured, 2 runs). Not
	// measured: a folder whose git directory the trust check refuses.
	//
	// On Linux and macOS a FIFO or a device file takes the same steps. As the
	// first git.info of a daemon on a FIFO or on /dev/null outside any
	// repository, b3081b51 answers with no member (measured on Linux). macOS,
	// such a file inside a repository and Windows are not measured.
	gateTrust, gateDir, gate := t, p.Path, true
	inFile := false
	if fi, err := os.Stat(p.Path); err == nil && (fi.Mode().IsRegular() || (runtime.GOOS != "windows" && !fi.IsDir())) && t.verdict == gitDirTrusted && t.pinCommonDir == "" {
		ft := requestGitDirTrust(filepath.Dir(p.Path), true)
		inFile = ft.verdict == gitDirTrusted && ft.pinCommonDir != ""
		gateTrust, gateDir, gate = ft, filepath.Dir(p.Path), inFile
	}
	// With no git in the PATH of the daemon, 5fd08069 answers "no repository"
	// with no member, runs no test and logs no line about git. Its folder holds
	// the lock file only (measured on Linux, 3 of 3 runs). git.list_branches takes
	// the same step (gitListBranches, 4c111e86). git.status takes it on Linux and
	// macOS. On Windows it runs a start test and keeps no failure (statusNoGitRunsTest).
	// A request with "shadowDirOff":true is not measured either, and it keeps its answer. The
	// settingsControl member follows the state of the daemon: a daemon that
	// passed its test before it lost git is not measured.
	if gate && !bool(p.ShadowDirOff) && gitLookupError() != nil {
		req.shadow.folderWithNoTest()
		return notRepo()
	}
	if gate {
		if msg, noRepo := req.settingsGateRefusal(gateTrust, p.ShadowDirOff, gateDir); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		} else if noRepo {
			return notRepo()
		}
	}
	if inFile {
		req.use = gitUse{stop: req.stop}
	} else if gate {
		req.keepLive(bool(p.ShadowDirOff), gateTrust, gateDir)
	}
	req.use.daemonVerdict = offDaemon
	req.listEntryConfig(t)
	// If the repo's config cannot be enumerated (e.g. an `include.path` with no value), the
	// reference cannot pin its config-defined hooks off and refuses with -32603
	// rather than running git. Measured against 7d193f89.
	c := hostileConfigRefusal(req.use, p.Path, true)
	if c.refusal != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, c.refusal)
	}
	if c.noRepo {
		return notRepo()
	}
	// A call whose repository folder went answers its text (from the code, not
	// measured). The later calls of git.info keep their answers on a failure.
	if _, err := hardenedGitRunPre(req.use, p.Path, true, &c.listing, nil, "rev-parse", "--git-dir"); copyGone(err) {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	} else if err != nil {
		return notRepo()
	}
	// The root is the folder where the walk of the trust check found the repository
	// (gitWalkRoot). 89cb6289 runs no `rev-parse --show-toplevel` here on Linux and
	// macOS. A walk that found a git directory with no work tree (a bare repository, a
	// path inside .git), or nothing, answers the bare shape (measured). On Windows git
	// is asked to confirm the root (gitInfoRoot).
	walkRoot, walkGitDir := gitWalkRoot(p.Path)
	if walkRoot == "" {
		return notRepo()
	}
	// The Windows root pair takes the form of the off switch at this moment, as
	// each later command does (measured on Windows). On Linux and macOS the pair
	// runs no git call, so a pending gate runs here, right before the first
	// command of gitRepoSlug.
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
	// never git's error text. Measured side by side against 90fca6e6 and f6010b97 on
	// Linux: a HEAD git fails to resolve ("ref: refs/heads/main" or 40 hex digits,
	// each followed by junk) answers without `branch` on both.
	branch, ok := gitUnderLimits(req.use, p.Path, true, "branch", "--show-current")
	switch {
	case !ok:
		branch = ""
	case branch == "":
		// A `rev-parse --short HEAD` that fails gives no `branch` member either.
		// On Windows that call exits 128 in a files repository with
		// `extensions.refStorage = reftable` and format version 1, and the reply
		// of 5fd08069 has no `branch` (measured).
		if sha, ok := gitUnderLimits(req.use, p.Path, true, "rev-parse", "--short", "HEAD"); ok {
			branch = "detached:" + sha
		}
	}
	// After a plain request, a git.info with "sessionFoldersOff":true and no
	// "shadowDirOff":true keeps the settingsControl member (4c111e86, measured on
	// Linux, macOS and Windows). Before that, its start test is not kept, and the
	// reply has no member (4c111e86, measured on Linux; replyControl).
	control := req.shadow.replyControl(req.offLive)
	return rpc.OKResult(req.ID, rpc.GitInfoReply{
		IsRepo:        true,
		Repo:          infoRepoName(top),
		Branch:        branch,
		Root:          top,
		RepoSlug:      slug,
		DefaultBranch: defBranch,

		SettingsControl: control,
	})
}

// keepLive gives the later git commands of a git.info or a git.list_branches the
// off switch of the moment (liveSwitch). off is the hold at the gate of the
// request, and t and dir are the trust verdict and the folder of that gate.
func (req *gitCall) keepLive(off bool, t gitDirTrust, dir string) {
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
func (req *gitCall) keepLiveForm(off bool, t gitDirTrust, dir string) {
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
// (measured on Linux). The create then holds its git directory as a create
// whose first gate had the copy does: the folder is read before the gate and
// held after it (acceptGitDir), and held takes it for the release at the
// reply. The request takes the copy, the read of the config and that hold as
// its own use, so every later step acts through the held folder. A git
// directory that is no longer that folder (heldFolderKind) refuses the later
// gate, and the form stays without a copy (claustrum's own, not measured).
// After the add the later gate changes only the form, as in keepLiveForm
// (measured).
func (req *gitCall) keepLiveCreate(off bool, t gitDirTrust, repo string, added *bool, held **acceptedGitDir) {
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
			before = gitDirIdentity(t.holdFolderFor(t.pinCommonDir))
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
			h := acceptGitDir(t.holdFolderFor(req.use.gitDir), before)
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
func (req *gitCall) followSwitch(off bool, gate func(l *liveSwitch, ask bool) (gitUse, bool)) {
	if !req.shadow.active() {
		return
	}
	l := &liveSwitch{off: req.offLive, pending: off && !req.offOwn, stop: req.stop}
	l.gate = func(ask bool) (gitUse, bool) { return gate(l, ask) }
	l.perCall = func() bool { return sessiondirs.CheckOff() && req.shadow.noKeptState() }
	req.use.live = l
}

// infoRepoName is the `repo` member of git.info: the base name of the root. A name that
// starts with "-" or "+" leaves the member out. f6010b97 and 89cb6289 do so on Linux
// and macOS (measured). Every other measured name kept it. The general rule behind
// those two rows is not measured.
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
	url, ok := gitUnderLimits(u, dir, true, "remote", "get-url", "origin")
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
// 89cb6289 do so on Linux and macOS (measured: a dangling origin/HEAD, one that
// names a blob, and origin/-x). The call has the heavy profile, as on 5fd08069
// (measured on Linux with no "shadowDirOff" member, and with it on Linux, macOS and
// Windows). Not measured: other invalid names, and a symbolic-ref answer that does
// not start with refs/remotes/origin/. claustrum verifies that answer the same way.
func gitDefaultBranch(u gitUse, dir string) string {
	ref, ok := gitUnderLimits(u, dir, true, "symbolic-ref", "refs/remotes/origin/HEAD")
	if !ok {
		return ""
	}
	name := strings.TrimPrefix(ref, "refs/remotes/origin/")
	if strings.HasPrefix(name, "-") {
		return ""
	}
	if _, ok := gitUnderLimits(u, dir, true, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name+"^{commit}"); !ok {
		return ""
	}
	return name
}

// statusNoGitRunsTest is true where git.status with no git on PATH also runs a
// start test. Each system answers isRepo:false there (4c111e86, measured on
// Linux, macOS and Windows). On Windows 4c111e86 then kept the failure, and
// 87375cc7 keeps none: after a git appears, the next request runs its own test
// and the calls of the status (measured on Windows, noGitStarts in gate). On
// Linux and macOS a later request runs its own test (measured).
var statusNoGitRunsTest = runtime.GOOS == "windows"

// statusOpenTest is statusPathOpenFails. Tests replace it to reach the refusal
// on every system.
var statusOpenTest = statusPathOpenFails

func gitStatus(req *gitCall) rpc.Response {
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
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "baseRepo is required")
	}
	// The gate of 89cb6289 (gitstatus.go, docs/protocol/git-status.md). Every failure of it
	// answers the full status shape with isRepo:false, not the bare rpc.NotRepoReply of
	// git.info.
	notRepo := rpc.OKResult(req.ID, rpc.GitStatusReply{})
	// A baseRepo that does not resolve, or that lies in a managed worktrees tree,
	// answers before any git call and before the check of the daemon's own
	// GIT_CONFIG_COUNT (measured).
	//
	// On Windows a baseRepo behind a chain of 32 or more links is refused, after the
	// excludes read. Chains of 64 links and more get the text too, and the system does
	// not resolve them (measured on Windows). Its place first is claustrum's own, not
	// measured (statusLinkChainRefusal).
	if msg := statusLinkChainRefusal(p.BaseRepo); msg != "" {
		globalExcludesPath()
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// On Windows a baseRepo through a link to a network or device path is refused
	// before any git call on it, also where the system cannot resolve it (measured
	// on Windows, networkLinkRefusal). The text names baseRepo, not path. The row
	// sends both through the link, so the test of baseRepo alone and its place after
	// the link chain test are claustrum's own, not measured.
	//
	// While no excludes read is stored, a junction on a share as the last name of
	// baseRepo makes no excludes read here. The next git.info makes it (4c111e86, measured on Windows). A symbolic link or a
	// link above the last name keeps the read (claustrum's own, not measured). So does
	// each other refusal of this test (claustrum's own, not measured).
	if msg, lastJunction := networkLinkRefusalAt(p.BaseRepo); msg != "" {
		if !lastJunction {
			globalExcludesPath()
		}
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// On Windows a failed start test that is still kept answers first, also for a
	// baseRepo through a junction that does not resolve. 4c111e86 answered the kept
	// refusal of the no-git request there (measured on Windows). 87375cc7 keeps no
	// such failure (gate). Its place after the link refusals and a kept failure of
	// another cause are claustrum's own.
	if statusNoGitRunsTest && !bool(p.ShadowDirOff) {
		if kept := req.shadow.keptFailure(); kept != "" {
			globalExcludesPath()
			return rpc.ErrResult(req.ID, rpc.CodeInternal, kept)
		}
	}
	// A baseRepo whose walk fails, such as a missing name, answers isRepo false with no
	// git call. See statusBaseWalkFails.
	if statusBaseWalkFails(p.BaseRepo) || statusBaseInManagedTree(p.BaseRepo) {
		return notRepo
	}
	// The read of the user's excludes comes next, so it also runs before a trust
	// refusal (measured on Linux).
	globalExcludesPath()
	// The git-directory trust check runs on baseRepo, not on path (gitdirtrust.go).
	// The count check comes after a trust refusal and before the "no repository"
	// answer.
	t := readGitDirTrust(p.BaseRepo, false)
	if t.verdict == gitDirRefused {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		// A `.git` file to a regular file answers "no repository" (gitDirTrust.fileGitDir).
		if t.fileGitDir {
			return notRepo
		}
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	if t.verdict == gitDirNoRepo {
		if msg := statusGitFileNowhere(t, req.stop, p.BaseRepo, bool(p.ShadowDirOff)); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
		if msg := statusNoRepoCalls(req.stop, p.BaseRepo, t.noGit); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
		return notRepo
	}
	// A baseRepo that passes the walk and cannot be opened answers here, with no further
	// git call. On Windows that is a dangling junction (measured).
	if _, err := os.Stat(p.BaseRepo); err != nil {
		return notRepo
	}
	// On Windows the object sources are checked before the next git call
	// (statusSourceRefusal).
	if msg := statusSourceRefusal(t.pinCommonDir, bool(p.ShadowDirOff)); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// With no git on the PATH of the daemon, a linked worktree answers isRepo:false
	// (statusNoGitRunsTest). Elsewhere than Windows no start test runs, so no failure
	// is kept, and as in git.info the folder of the daemon is made (not measured). A
	// baseRepo that is no folder and the off switch are not measured: they keep the gate.
	if fi, err := os.Stat(p.BaseRepo); err == nil && fi.IsDir() && !bool(p.ShadowDirOff) && gitLookupError() != nil {
		if statusNoGitRunsTest {
			_ = req.shadow.gate(false)
		} else {
			req.shadow.folderWithNoTest()
		}
		return notRepo
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate).
	//
	// On Linux and macOS a baseRepo that is a FIFO or a device file outside any
	// repository gets no gate, as in git.info. As the first git request of a
	// daemon on a FIFO or on /dev/null, b3081b51 makes 2 calls: the excludes read
	// and `version` (measured on Linux). A regular file there and macOS are
	// claustrum's own, not measured.
	//
	// A regular file inside a repository gets the gate with the trust of its folder.
	// So the gate asks git about that repository (askGit) and reads its config. With
	// `path` and baseRepo a file, 4c111e86 makes 14 calls in the first request, and a
	// failing `rev-parse --git-dir` there answers the -32603 text of askGit (measured
	// on Linux). A 15 s hold of that call changes no reply (measured on Linux, no
	// test here). macOS is claustrum's own, and Windows keeps the trust of the
	// file (not measured). gateTrust also takes the pinned git directory of the
	// folder to syncRepo and keepLiveForm. So the gate makes the git-shadow copy
	// of that repository and `.probe` for a file baseRepo, as 4c111e86 does
	// (measured on Linux).
	//
	// 4c111e86 answers "no repository" for a file link into another repository
	// (measured on Linux). macOS is claustrum's own. A regular file keeps the
	// "disagree" text of askGit.
	//
	// For a file link the folder of its target is judged, not the folder of the
	// link. A link in a repository to a file outside any repository gets no gate,
	// and a link from outside into a repository gets it, as on 4c111e86 (measured
	// on Linux). macOS is claustrum's own.
	gateTrust, noGate, outLink := t, false, false
	if fi, err := os.Stat(p.BaseRepo); err == nil && runtime.GOOS != "windows" && !fi.IsDir() &&
		t.verdict == gitDirTrusted && t.pinCommonDir == "" {
		if folder := readGitDirTrust(filepath.Dir(resolvedFile(p.BaseRepo)), false); folder.verdict == gitDirTrusted && folder.pinCommonDir != "" {
			gateTrust, outLink = folder, linkOutOfFolder(p.BaseRepo)
		} else {
			noGate = true
		}
	}
	if noGate {
		req.use = gitUse{stop: req.stop}
	} else if msg, noRepo := req.settingsGateRefusal(gateTrust, p.ShadowDirOff, p.BaseRepo); msg != "" {
		if outLink && req.gitDisagreed {
			return notRepo
		}
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	} else if noRepo {
		return notRepo
	} else {
		// The later git commands follow the off switch (measured on Linux).
		req.keepLiveForm(bool(p.ShadowDirOff), gateTrust, p.BaseRepo)
	}
	// A repo whose config cannot be enumerated is refused with -32603 (measured). The listing runs in
	// baseRepo as sent and carries the heavy profile (call 2 of the measured call log).
	c := hostileConfigRefusal(req.use, p.BaseRepo, true)
	if c.refusal != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, c.refusal)
	} else if c.noRepo {
		return notRepo
	}
	// Call 3 of the measured call log. Its answer is the common directory, in the spelling that git gives
	// it. A failure answers isRepo:false (measured).
	out, err := hardenedGitRunPre(req.use, p.BaseRepo, true, &c.listing, nil, "rev-parse", "--absolute-git-dir")
	if copyGone(err) {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	}
	if err != nil || out == "" {
		return notRepo
	}
	common := filepath.Clean(strings.TrimRight(out, "\r"))
	// For a linked worktree as baseRepo git prints its registration entry. The request
	// goes on in the git directory of the main repository, which the trust check
	// pinned. be1c1ca5 and b3081b51 do so for that worktree and for another linked
	// worktree as path, also with the off switch (measured on Linux). macOS and
	// Windows are claustrum's own, not measured.
	common = statusCommonOfEntry(t, common)
	// On Windows the request goes on with the git directory that git printed,
	// when it is another spelling of the same folder (usePrintedGitDir).
	if statusFollowsPrintedGitDir {
		if msg := req.usePrintedGitDir(t, common); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
	}
	// No `worktrees` folder in the common directory: no further git call
	// (measured).
	if fi, err := os.Stat(filepath.Join(common, "worktrees")); err != nil || !fi.IsDir() {
		return notRepo
	}
	sp, ok := statusPathOf(p.Path, p.BaseRepo)
	if !ok {
		return notRepo
	}
	// On Windows a `path` that cannot be opened as a folder answers here, with no
	// call of statusWorkTreeProbe (statusPathOpenFails).
	if statusOpenTest(filepath.Clean(p.Path)) {
		return notRepo
	}
	probeUse := req.use.atGitDirCommand()
	top, ok, err := statusWorkTreeProbe(req.stop, common, sp.probeTree, statusCommonPin(probeUse, common), probeUse.gitDirFront())
	if err != nil {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
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
	// then fails here, because git gets it as sent (measured).
	changes, err := statusChanges(req.use, p.Path, common, entry)
	if errors.Is(err, errStatusNoFolder) {
		return notRepo
	}
	if err != nil {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	}
	return rpc.OKResult(req.ID, rpc.GitStatusReply{IsRepo: true, Clean: len(changes) == 0, Changes: changes})
}

// resolvedFile is p with its symbolic links resolved, or p when they do not resolve.
func resolvedFile(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// linkOutOfFolder reports whether p is a symbolic link whose resolved target is
// in another folder than the resolved folder of p.
func linkOutOfFolder(p string) bool {
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	folder, err := filepath.EvalSymlinks(filepath.Dir(p))
	return err == nil && filepath.Dir(target) != folder
}

// samePath compares two paths after lexical cleaning. Callers that compare against
// git's output canonicalize their operands first.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func gitListBranches(req *gitCall) rpc.Response {
	p, bad := bindGitParams(req, &infoViewParams{})
	if bad != nil {
		return *bad
	}
	// On Windows a \\?\UNC\ path, a path on a share that cannot be found or
	// that denies every account, and a directory link on a share are refused
	// (measured on Windows, unlookablePathRefusal). No git call runs (from the
	// code). Its place first is claustrum's own, not measured.
	if msg := unlookablePathRefusal(p.Path); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// On Windows a path that the network-path check cannot look at is refused next
	// (measured on Windows, pathLookRefusal). Its place here is claustrum's own, not
	// measured.
	if msg := pathLookRefusal(p.Path); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// A path through a link to a network or device path is refused with no git call
	// (87375cc7, measured on Windows, linkHopRefusal).
	if p.Path != "" {
		if msg := linkHopRefusal(p.Path); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
	}
	// A path inside a managed worktrees tree, and a non-empty path that does not
	// resolve, answer the bare isRepo:false shape (branches:[]) before any git call
	// and before the check of the daemon's own GIT_CONFIG_COUNT. On Windows the
	// resolve walks the path as sent (linkWalkFails), so a junction before the last
	// name passes, as on b3081b51 (measured on Windows). A ".." drops the name before
	// it untested, so b3081b51 lists <T>\missing\.. and <T>\t.txt\.. (measured on Windows).
	// git.list_branches ignores a baseRepo member, as 5fd08069 does (measured on
	// Linux). The managed-worktrees test also runs on the daemon's working
	// directory.
	if repoInsideMarkedTree(p.Path) || repoInsideMarkedTree(p.repoDir()) ||
		(p.Path != "" && linkWalkFails(p.Path, true)) {
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
	}
	// The git-directory trust check runs on path (gitdirtrust.go). The check of the
	// daemon's own GIT_CONFIG_COUNT comes after a trust refusal and before the "no
	// repository" answer (docs/protocol/git-rules-calls.md → "Where each method answers the
	// refusal").
	t := readGitDirTrust(p.Path, true)
	if t.verdict == gitDirRefused {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, t.refusal)
	}
	if msg, bad := daemonCountRefusal(); bad {
		// A `.git` file to a regular file answers "no repository" (gitDirTrust.fileGitDir).
		if t.fileGitDir {
			return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
		}
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	if t.verdict == gitDirNoRepo {
		// A listing that fails here refuses (measured on Linux, macOS and
		// Windows). One more call follows a listing that passed (noRepoInfoCalls).
		if msg := noRepoInfoCalls(t, req.stop, p.Path, bool(p.ShadowDirOff)); msg != "" {
			return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
		}
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
	}
	// On Windows GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment
	// refuses before any git call (alternateEnvRefusal). Its place after the trust
	// check and the count check, and before the no-git step, is claustrum's own,
	// not measured.
	if msg := alternateEnvRefusal(bool(p.ShadowDirOff)); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	}
	// With no git in the PATH of the daemon, a request with no "shadowDirOff":true
	// answers the "no repository" shape, runs no start test and makes the folder of
	// the daemon, as git.info does (4c111e86, measured on Linux and macOS, the folder
	// on Linux). Windows is not measured.
	if !bool(p.ShadowDirOff) && gitLookupError() != nil {
		req.shadow.folderWithNoTest()
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate). The answer has the error object of git.info (measured).
	if msg, noRepo := req.settingsGateRefusal(t, p.ShadowDirOff, p.Path); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, msg)
	} else if noRepo {
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
	}
	req.keepLive(bool(p.ShadowDirOff), t, p.Path)
	// A repo whose config cannot be enumerated is refused with -32603 (7d193f89),
	// measured.
	c := hostileConfigRefusal(req.use, p.Path, true)
	if c.refusal != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, c.refusal)
	}
	if c.noRepo {
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
	}
	// 5fd08069 runs this under the heavy profile with the config precursor
	// (measured on Linux). The repo gate is `rev-parse --git-dir` (true for a bare repo and from inside a
	// `.git` directory, unlike git.info's --is-inside-work-tree). The reference returns the full branches
	// shape (branches:[]) on a non-repo, not git.info's bare rpc.NotRepoReply. The refusal check above was its
	// precursor.
	if _, err := hardenedGitRunPre(req.use, p.Path, true, &c.listing, nil, "rev-parse", "--git-dir"); copyGone(err) {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	} else if err != nil {
		return rpc.OKResult(req.ID, rpc.BranchesReply{Branches: []string{}})
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
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	}
	branches := []string{}
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			branches = append(branches, t)
		}
	}
	return rpc.OKResult(req.ID, rpc.BranchesReply{IsRepo: true, Branches: branches})
}

// pairBeforeAdd runs the two calls of 5fd08069 before the add of a create in
// the state shadow or with "shadowDirOff":true, when the step before the add
// found no entry that names the new leaf. They run when the folder of the
// registrations of gitDir is a real folder, empty or not (measured on Linux, macOS and Windows). With
// the member 5fd08069 runs them in a repository with a linked worktree (measured). An entry that
// names the leaf gives no pair (measured). In the state shadow one such entry that the stale step
// removed gives the pair (c9125eb8 and 4c111e86, measured on Linux). So does one such entry that stays for its
// `locked` file (4c111e86, measured on macOS). On 5fd08069 a missing folder gives none. b3081b51 and
// c9125eb8 run the pair in the first create of a repository with no such folder (measured on Linux,
// macOS and Windows, in the state shadow), so there a missing folder gives the pair (missingRuns).
// With the member a missing folder still gives none: no row of those builds has it. The state
// fallback-git gets no pair
// (claustrum's own, not measured). For a linked baseRepo that folder is the one of the main repository
// (gitDirRegistryDir, not measured). The calls come after `rev-parse --absolute-git-dir` and before
// the listing of the add (measured on macOS and Windows). With a missing folder
// they come after the listing of the add and right before the add on Linux and
// macOS (c9125eb8 and b3081b51, measured on Linux and macOS). The caller then
// gets them back as afterListing, for worktreeAdd. Windows keeps the place
// before the listing (claustrum's own, not measured). A refusal before the
// add then runs no pair (claustrum's own, not measured): the listing of the
// add stays the last call of the refusals of 5fd08069 after a swap.
//
// The test does not follow a link. A symbolic link to a real folder there
// gives no pair, also when that folder holds a registration (measured on
// Linux). A broken link and a file give none either (not measured). On
// Windows a file or a junction there gives the pair, but not with the member
// (87375cc7, measured on Windows, and 4c111e86 for the file). A directory
// symbolic link there gets the same (claustrum's own, not measured).
//
// The calls are gitDirWorkTreeToplevel in the common git directory of gitDir.
// For a linked baseRepo that is the `.git` of the main repository, not the
// folder of the linked worktree (4c111e86, measured on Linux and macOS). The
// work tree is workTree. That is repo, or with a worktreeRoot the worktreePath (c9125eb8 and
// 4c111e86, measured on Linux). After a removed stale entry it is the parent
// of gitDir (4c111e86, measured on Linux with a baseRepo through a link). A worktreePath with a final slash or a link
// keeps its spelling (claustrum's own, not measured). Their answers are not used, and a failure changes nothing (measured on
// Linux). The timeoutMs of the caller does not end them. With the second call
// held 10 s and a timeoutMs of 3000, 5fd08069 sends no signal, then runs the
// add and answers the timeout before the checkout, with the rollback
// (measured on Linux). The limits of the two calls are those of their kind
// (gitstop.go). No cell measured a limit there: that is claustrum's own.
//
// member is the hold of the off switch at the bind (bindGitParams). Under the
// hold of another request it is claustrum's own, not measured (pairSSHUse).
//
// late puts the pair after the listing of the add, as a missing folder does.
func pairBeforeAdd(req *gitCall, gitDir, repo, workTree string, missingRuns, member, late bool) (afterListing func()) {
	registrations := gitDirRegistryDir(gitDir)
	fi, err := os.Lstat(registrations)
	missing := missingRuns && errors.Is(err, fs.ErrNotExist)
	nonFolderRuns := stagedAddWindowsSteps && !member && err == nil
	if !missing && !nonFolderRuns && (err != nil || !fi.IsDir()) {
		return nil
	}
	run := func() {
		u := pairSSHUse(req, member).atGitDirCommand()
		_, _ = gitDirWorkTreeToplevel(req.stop, u.sshOff(), filepath.Dir(registrations), workTree, u.gitDirPin(repo), u.gitDirFront())
	}
	if (missing || late) && !stagedAddWindowsSteps {
		return run
	}
	run()
	return nil
}

// resolvedPairTree is the --work-tree of the pair of a create on Windows. It is repo with
// its junctions and short names resolved (87375cc7, measured on Windows). After a
// junction move at `rev-parse --absolute-git-dir` it is the leaf below the top of the
// held git directory (measured on Windows, one row). A held folder that is not named
// `.git` keeps repo (claustrum's own, not measured).
func resolvedPairTree(held *acceptedGitDir, repo, leaf string) string {
	if !held.junctionMoved() {
		return pathcanon.FinalDir(repo)
	}
	rel, err := filepath.Rel(repo, filepath.Clean(leaf))
	if err != nil || !filepath.IsLocal(rel) || filepath.Base(held.path) != ".git" {
		return repo
	}
	return filepath.Join(filepath.Dir(held.path), rel)
}

// pairSSHUse is the use of the absolute-git-dir pair and the show-toplevel
// pair of a create. With "shadowDirOff":true, when the calls name no copy and
// the daemon is not in the state fallback-git, both pairs get
// GIT_SSH_COMMAND=false, whatever the daemon has (c9125eb8 and b3081b51,
// measured on Linux). macOS and Windows follow Linux
// (claustrum's own, not measured). In every other state it is req.use.
func pairSSHUse(req *gitCall, member bool) gitUse {
	u := req.use
	if member && !u.shadow() && req.shadow.readsRepoConfig() {
		u.sshKept = nil
	}
	return u
}

func gitWorktreeCreate(req *gitCall) rpc.Response {
	p, bad := bindGitParams(req, &createViewParams{})
	if bad != nil {
		return *bad
	}
	if p.BranchName == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "branchName is required")
	}
	repo := p.repoDir()
	req.use.sshKept = req.offLive
	// The folder of the checkout goes after the write of the reply, also in the
	// temporary folder (rpc.Response.AfterWrite). 4c111e86 removes it soon after
	// the reply (measured on Linux). A panic before the reply removes
	// it at once.
	req.use.atReply = &req.atReply
	handed := false
	defer func() {
		if !handed {
			req.runAtReply()
		}
	}()
	resp := withWorktreeCreateLock(repo, func(hold *createHold) rpc.Response {
		// The start of the deadline of timeoutMs (worktreeCreateCtx), before any
		// git call. A wait for the create lock at the add counts (claustrum's
		// own, not measured).
		arrival := time.Now()
		return gitWorktreeCreateLocked(req, &p, repo, arrival, hold)
	})
	if len(req.atReply) > 0 {
		resp.AfterWrite = req.runAtReply
	}
	handed = true
	return resp
}

// hold is the hold of the create lock: the add takes it, and each rollback
// takes it again (withWorktreeCreateLock).
func gitWorktreeCreateLocked(req *gitCall, p *gitParams, repo string, arrival time.Time, hold *createHold) rpc.Response {
	// On Windows a baseRepo through a link to a network or device path is refused
	// first, with no git call, and the text is the error of a failed add (measured
	// on Windows, networkLinkRefusal). No folder and no branch is made (measured on
	// Windows).
	if msg := networkLinkRefusal(repo); msg != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	// 7d193f89 refuses a baseRepo that sits inside a managed worktrees tree as an
	// invalid trust root, before the repo check. Measured against 7d193f89. A
	// session worktree must be created from a real top-level repo, never from
	// inside another session's worktree tree. A baseRepo that fails claustrum's own
	// trust-root test gets the same refusal (baseRepoWalkFails). It comes before
	// the count check (`f6010b97`, measured on Windows). A baseRepo that does not
	// exist skips the marker test, answers not_a_repo and makes nothing. That is
	// b3081b51, measured on Linux and macOS, and on Windows for a missing path below
	// a marker. Another ErrNotExist, such as <T>/missing/.., is claustrum's own, not measured.
	if _, err := os.Stat(repo); (!errors.Is(err, fs.ErrNotExist) && repoInsideMarkedTree(repo)) || baseRepoWalkFails(repo) {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     managedWorktreesRefusal,
			ErrorCode: "nested_base_repo",
		})
	}
	// The git-directory trust check runs on baseRepo before anything is created
	// (gitdirtrust.go). A refusal creates no worktree directory, no entry and no
	// branch. When the daemon's own environment carries GIT_COMMON_DIR, the refusal
	// text is wrapped as a failed add. The check of the daemon's own GIT_CONFIG_COUNT
	// comes after a trust refusal and before the "no repository" answer. A git
	// directory that the daemon cannot search counts as no repository first
	// (readGitDirTrust).
	t := readGitDirTrust(repo, false)
	// The git directory as it is before the gate (acceptedGitDir).
	var gitDirBefore os.FileInfo
	if gitDirMustBeReal && t.verdict == gitDirTrusted && t.pinCommonDir != "" {
		gitDirBefore = gitDirIdentity(t.holdFolderFor(t.pinCommonDir))
	}
	if t.verdict == gitDirRefused {
		// The excludes read of a first request runs before the refusal. 4c111e86 makes
		// that one call before a commondir that is no small plain file (measured on
		// macOS and Windows). Linux and the other trust refusals are claustrum's own.
		globalExcludesPath()
		msg := t.refusal
		if daemonCommonDirSet() {
			msg = gitDirAddWrap + msg
		} else if !bool(p.ShadowDirOff) && req.shadow.active() && req.shadow.readsRepoConfig() && entryIsLink(repo) {
			msg = gitDirAddWrap + msg
			makeRefusedLeafParents(p, repo)
		}
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	// With no git on PATH, 4c111e86 answers not_a_repo after the marker test and
	// before the count check, also with a worktreeRoot. Nothing is made (measured
	// on Linux and macOS, and on Windows for a missing baseRepo). On Windows an
	// existing baseRepo gets it too (claustrum's own, not measured). The order
	// against a trust refusal is not measured. 87375cc7 answers the code
	// git_not_found there, with the same text, also with a git of the login-shell
	// PATH that it does not use (measured on Linux and macOS). On Windows it is
	// measured for a missing baseRepo.
	if gitLookupError() != nil {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: "not a git repository", ErrorCode: "git_not_found"})
	}
	if msg, bad := daemonCountRefusal(); bad {
		// A `.git` file to a regular file answers "no repository" (gitDirTrust.fileGitDir).
		if t.fileGitDir {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: "not a git repository", ErrorCode: "not_a_repo"})
		}
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	if t.verdict == gitDirNoRepo {
		// A `.git` file to a missing path runs the start test, and in the state
		// shadow the call about the repository, as in gitInfo. After the reply
		// 4c111e86 has the folder of the daemon with its lock and `.probe` there
		// (measured on Linux and Windows, macOS claustrum's own). On Linux its calls
		// are those of such a git.info, with no listing (measured with an older git).
		// A failed call answers its text. Another answer with no error, other than
		// "not a git repository", keeps the listing below (claustrum's own, not
		// measured).
		if t.goneGitFile {
			if msg := req.shadow.gate(bool(p.ShadowDirOff)); msg != "" {
				return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
			}
			if !bool(p.ShadowDirOff) && req.shadow.control(false) == controlShadow {
				if a := req.shadow.askGit(t, repo); a.text != "" {
					return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: a.text, ErrorCode: "worktree_add_failed"})
				} else if a.notARepo {
					return notARepoResult(req.ID, repo)
				}
			}
		}
		// A listing that fails here refuses, with and without worktreeRoot, and nothing
		// is created (measured on Linux, macOS and Windows). One more call follows a
		// listing that passed (noRepoCreateCalls).
		if msg := noRepoCreateCalls(t, req.stop, !bool(p.ShadowDirOff) && req.shadow.active(), bool(p.ShadowDirOff), repo); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
		}
		return notARepoResult(req.ID, repo)
	}
	// On Windows GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment and
	// the alternates file are checked before any git call, and nothing is
	// created (alternateEnvRefusal, alternatesRefusal).
	if msg := alternateEnvRefusal(bool(p.ShadowDirOff)); msg != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	if msg := alternatesRefusal(t.pinCommonDir, false); msg != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	}
	// A baseRepo that does not exist answers not_a_repo here, with no start test
	// and no state line, except below a repository on a share (askForMissingOnShare). 4c111e86 does so on Linux, macOS and Windows, and on Linux
	// it deletes nothing (measured). On Windows the first such request of a daemon
	// makes one git call and a later one none (measured). The lack of a `git version`
	// on Linux and macOS, and a request with "shadowDirOff":true, are claustrum's own.
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		// On Windows a missing baseRepo below a repository on a share asks git
		// about that repository first (askForMissingOnShare).
		if a, _ := req.askForMissingOnShare(repo, bool(p.ShadowDirOff)); a.text != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: a.text, ErrorCode: "worktree_add_failed"})
		}
		return notARepoResult(req.ID, repo)
	}
	// The daemon's own read of the repository's config comes before the listing
	// (repoConfigGate). Nothing is created (measured on Linux).
	if msg, noRepo := req.settingsGateRefusal(t, p.ShadowDirOff, repo); msg != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	} else if noRepo {
		// A `.git` folder on a network share that git does not use (shareGitDir).
		if g := shareGitDir(repo); g != "" && req.gitRefused {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: fmt.Sprintf(unusableGitText, g), ErrorCode: "worktree_add_failed"})
		}
		return notARepoResult(req.ID, repo)
	}
	// The create holds the git directory that its gate accepted until it answers
	// (acceptedGitDir). A git directory that changed during the gate is not
	// measured: it gets the refusal of a config behind a link (measurements on
	// Windows have that text for a change after the gate).
	if gitDirMustBeReal && req.use.shadow() {
		held := acceptGitDir(t.holdFolderFor(req.use.gitDir), gitDirBefore)
		if held == nil {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false,
				Error: repoConfigPrefix + gitDirLinkText(req.use.gitDir), ErrorCode: "worktree_add_failed"})
		}
		defer held.release()
		held.holdFor(repo, t)
		req.use.accepted = held
	}
	// A linked baseRepo: the calls in it get the items of the `config.worktree`
	// file of its entry, as on git.info. 5fd08069 has `-c core.eol=lf` from that
	// file on the rev-parse calls and on the `worktree add` (measured on macOS).
	req.listEntryConfig(t)
	// The later git commands follow the off switch (measured on Linux).
	// added is true after a good add.
	added := false
	var laterHeld *acceptedGitDir
	defer func() { laterHeld.release() }()
	req.keepLiveCreate(bool(p.ShadowDirOff), t, repo, &added, &laterHeld)
	// A repo whose config cannot be enumerated is refused before git runs — the
	// reference surfaces the same "config-defined hooks could not be pinned off"
	// detail under errorCode worktree_add_failed. Measured.
	c := hostileConfigRefusal(req.use, repo, false)
	if c.refusal != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	// is, and nothing is made (measured on Linux).
	inRepo := false
	if !c.noRepo {
		var err error
		if inRepo, err = isRepo(req.use, repo, c.listing); copyGone(err) {
			return copyGoneResult(req, err)
		}
	}
	if !inRepo || req.use.noWorkTree(repo) {
		if err := req.stop.halted(); err != nil {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: err.Error(), ErrorCode: "worktree_add_failed"})
		}
		return notARepoResult(req.ID, repo)
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
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalSpellingRefusal(p.WorktreeRoot, p.WorktreePath, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// f6010b97 refuses a relative baseRepo, an absent one included, or one with a
		// ".." component, with the texts of the worktreePath check. The text names
		// baseRepo as sent. It comes after the repo check, and nothing is created
		// (measured on Linux and macOS). Its order against the other checks of this
		// branch is not measured. It sits where git.worktree_remove has it.
		if msg := sessionFolderSpellingRefusal(p.BaseRepo, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalShapeRefusal(p.WorktreeRoot, p.WorktreePath, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "create", false); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A root in the repository is refused before the root-chain step. The first
		// test compares the cleaned paths and resolves no symlink (measured). Then
		// claustrum makes the calls that f6010b97 and 89cb6289 make before their next
		// refusal. They are a light `rev-parse --show-toplevel`, a heavy `rev-parse
		// --absolute-git-dir` and a light `worktree list --porcelain -z`. Each runs in
		// baseRepo with its listing. With the repo check that is 9 calls (measured on
		// Linux and macOS). claustrum does not use the answer of the absolute-git-dir
		// call.
		if msg := worktreeRootInRepoRefusal(p.WorktreeRoot, p.BaseRepo, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// A failed show-toplevel stops the create, and so does a `worktree list` that
		// fails with -z and then without it. Nothing is made (`5fd08069`, measured on
		// Linux). A show-toplevel that its time limit stopped gets the same frame, with
		// the stop text (measured on Linux).
		topLevel, err := repoTopLevelCall(req.use, repo, nil)
		if copyGone(err) {
			return copyGoneResult(req, err)
		}
		if err != nil {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, ErrorCode: "mkdir_failed",
				Error: parentStepPrefix + "cannot determine the repository's work tree: " + err.Error()})
		}
		_ = repositoryCheckError(req.use, repo, nil)
		listed, err := worktreeListTwice(req.use, repo)
		if copyGone(err) {
			return copyGoneResult(req, err)
		}
		if err != nil {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, ErrorCode: "mkdir_failed",
				Error: parentStepPrefix + "cannot list the repository's worktrees: " + err.Error()})
		}
		// Not measured: this test before the ancestor walk. A failing folder above a
		// loop gets the loop text.
		if msg := locationLinkRefusal(p.WorktreeRoot, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := repoFolderHoldsRepoRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, repo, "create", true); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		checkouts := newRepoCheckouts(repo, p.BaseRepo, topLevel, listed)
		// A worktreeRoot is refused below a directory owned by a user other than you
		// or uid 0. It is also refused below a directory writable by a shared group or
		// by every user, without the sticky bit. The refusal comes after the 9 calls,
		// with no further git call (measured on Linux and macOS). In a measurement,
		// both references send it where the checkout text also applies. It comes
		// before the root-chain step (measured) and the tests of the root itself. The
		// walk also runs the checkout tests and the .git test on each directory, so
		// the first failing directory from the top decides.
		if msg, code := worktreeRootAncestorRefusal(p.WorktreeRoot, checkouts); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: code})
		}
		// A listed worktree whose folder is gone gets that folder, empty, before the
		// refusal (`5fd08069`, measured on Linux). Only the folder itself is made,
		// and only below a folder that exists (not measured).
		if msg, gone := worktreeRootCheckoutRefusal(p.WorktreeRoot, checkouts, "create", true); msg != "" {
			makeGoneWorktreeFolder(gone)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// The root-chain step comes before the owner and write refusals of the root.
		// The <directory> symlink refusal comes after them, then the <directory> step.
		// That is the measured order. Not measured: the session tests before the
		// owner and write tests of the root, and the write tests of the <directory>
		// level before its .git test.
		dir, msg, code := externalChainCheck(p.WorktreeRoot, p.WorktreePath)
		if msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: code})
		}
		externalDir = dir
		if msg := sessionLocationRefusal(p.WorktreeRoot); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeRootShareRefusal(root); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		// The <directory> level gets the owner and write tests of the root. The refusal
		// comes before the step that drops an old registration of the leaf, so that
		// registration stays (`5fd08069`, measured on Linux). Its owner test is not
		// measured.
		if msg := worktreeRootShareRefusal(filepath.Dir(filepath.Clean(p.WorktreePath))); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := repoFolderInWorktreeRefusal(p.WorktreeRoot, p.WorktreePath, p.BaseRepo, listed, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg, code := externalDirLevelCheck(externalDir); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: code})
		}
		if msg := externalWorktreeDirNotEmptyRefusal(p.WorktreePath); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
	} else {
		// worktree-location containment, added by the reference in 7d193f89: the
		// session folder must be a fresh directory strictly inside the repo. Empty is
		// not a relative-path refusal — the reference fails it as a non-directory at
		// the parent-creation step, with its own worktreePath echoed (measured).
		if p.WorktreePath == "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:   false,
				Error:     fmt.Sprintf("failed to create parent directory: %q does not name a directory", p.WorktreePath),
				ErrorCode: "mkdir_failed",
			})
		}
		// The text names baseRepo as sent, so an absent one reads "" (`f6010b97`,
		// measured on Linux and macOS). filepath.Rel reads "" as ".", so the test is
		// the same as on repo.
		if msg := worktreePathRefusal(p.BaseRepo, p.WorktreePath, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "unsafe_path"})
		}
		if msg := worktreeSymlinkRefusal(repo, p.WorktreePath, "create"); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "symlinked_component"})
		}
	}
	// On Windows a junction between the repo and the leaf fails the parent step, and
	// nothing is created (measured on Windows). It comes before the test of a leaf
	// that exists behind the junction (87375cc7, measured on Windows). It runs before
	// the stale registration drop below, so nothing is touched either.
	if p.WorktreeRoot == "" {
		if msg := junctionParentRefusal(repo, p.WorktreePath); msg != "" {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: msg, ErrorCode: "mkdir_failed"})
		}
	}
	if _, err := os.Lstat(p.WorktreePath); err == nil {
		// On Linux and macOS the refusal names the cleaned path, with the symlinks
		// of its parent folder resolved and its last name kept (parentResolvedPath:
		// measured without and with a worktreeRoot, Linux and macOS). f6010b97 and
		// 90fca6e6 quote "R/cp/w1" for a worktreePath sent as "R/cp/w1/" with a
		// worktreeRoot (measured on Linux and macOS). On Windows the path with a
		// worktreeRoot is the cleaned path, and the in-repo path is spelled with the
		// on-disk letter case of each component that exists (existingPathSpelling).
		// The path is in backticks (be1c1ca5 and b3081b51, measured on Linux without a
		// worktreeRoot).
		// With a worktreeRoot, and on macOS and Windows, that is claustrum's own (not measured).
		existing := externalPathSpelling(p.WorktreePath)
		if p.WorktreeRoot == "" {
			// A folder above the leaf through a junction is named by its real path
			// (4c111e86, measured on Windows).
			existing = realParentPath(existingPathSpelling(p.WorktreePath))
		}
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     fmt.Sprintf("refusing to create worktree: %s already exists, and a new worktree is only ever created in a fresh directory", tickPath(existing)),
			ErrorCode: "unsafe_path",
		})
	}
	// The target is confirmed missing above, so a worktree registration still
	// naming it is stale (on Windows not always, D27). Drop
	// such a registration if it is the only one and is not locked
	// (removeStaleWorktreeEntry). Windows runs the step later, after
	// `rev-parse --absolute-git-dir` (staleStepAfterGitDir).
	// On Linux and macOS a create that holds its git directory runs the step
	// through the held folder (gitdirheld.go). Windows runs it by the path.
	staleStep := func() (bool, bool, bool) {
		// The stale step goes by the path, which now leads to the second repository.
		// No row deletes an entry there, so the step does not run.
		if req.use.accepted.junctionMoved() {
			return false, false, false
		}
		if createStepHook != nil && req.use.accepted != nil {
			createStepHook("stale")
		}
		return removeStaleWorktreeEntry(repo, p.WorktreePath, req.use.accepted)
	}
	staleMatched, staleRemoved, staleLocked := false, false, false
	if !staleStepAfterGitDir {
		staleMatched, staleRemoved, staleLocked = staleStep()
	}
	// The reference makes the parent before adding — this is what lets a nested
	// session path such as <repo>/.claude/worktrees/<id> succeed on a fresh
	// repo. The parent comes from the cleaned path. With a trailing slash,
	// filepath.Dir of the raw path is the leaf itself, and the leaf mkdir below
	// then failed with "file exists". The references create that worktree and
	// echo the path as sent. Measured on Linux and macOS. Every frame below
	// therefore keeps the raw p.WorktreePath. The parent makers set the modes and
	// the error texts per OS.
	leafParent := filepath.Dir(filepath.Clean(p.WorktreePath))
	var parentErr error
	if p.WorktreeRoot != "" {
		parentErr = mkdirExternalWorktreeParents(externalDir)
	} else {
		parentErr = mkdirRepoWorktreeParents(repo, leafParent)
	}
	// For an external worktreeRoot, tag the <directory> level as holding managed
	// session worktrees before git runs. This is the same marker
	// repoInsideMarkedTree looks for, so a later create whose baseRepo sits
	// under here is refused as a nested repo. A marker that cannot be made stops
	// the create before the leaf is made.
	if parentErr == nil && p.WorktreeRoot != "" {
		parentErr = placeWorktreesMarker(externalDir)
	}
	if parentErr != nil {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     fmt.Sprintf("failed to create parent directory: %v", parentErr),
			ErrorCode: "mkdir_failed",
		})
	}
	// 7d193f89 also creates the worktree directory ITSELF before `git worktree add`
	// (git adds into the pre-made empty dir). An unwritable/foreign-owned parent fails HERE as
	// `failed to create worktree directory: mkdirat <leaf>: <errno>` with errorCode
	// mkdir_failed — where claustrum used to reach git and return worktree_add_failed.
	// mkdirWorktreeLeaf reproduces the `mkdirat <leaf>` wording byte-for-byte
	// (unix). Measured against 7d193f89.
	if err := mkdirWorktreeLeaf(p.WorktreePath); err != nil {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     fmt.Sprintf("failed to create worktree directory: %v", err),
			ErrorCode: "mkdir_failed",
		})
	}
	// Record the leaf's identity now, to confirm below that `git worktree add`
	// populated this same directory and nothing swapped it during the add. The
	// checkpoint holds the leaf and its parent open until the create answers.
	checkpoint := checkpointCreatedWorktree(p.WorktreePath)
	checkpoint.lock = hold
	defer checkpoint.release()
	// The steps below test the held git directory (acceptedGitDir). The texts
	// are those of 5fd08069 on Windows, with a junction at `repo\.git` to a copy
	// of the git directory (measured). refuseAdd answers one of them and removes
	// the empty leaf: the rows have no folder of the new worktree at the reply.
	refuseAdd := func(text string) rpc.Response {
		undoFailedAdd(p.WorktreePath, checkpoint)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	}
	// A junction that was put there after the first or the second git call of
	// the create: the refusal of the config, with no words before it
	// (measured).
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
		if _, ok := gitUnderLimits(req.use, repo, false, "show-ref", "--verify", "--quiet", "refs/heads/"+p.ExistingBranch); ok {
			attachBranch = p.ExistingBranch
		}
	}
	if startCommit == "" {
		// The fallback echoes the current branch. On a detached HEAD abbrev-ref
		// prints "HEAD", and the member is omitted (measured against f6010b97 and
		// 90fca6e6). On an unborn HEAD abbrev-ref fails, the member is omitted, and
		// the add below fails (see TestGitWorktreeCreateEmptyRepo).
		source = ""
		if s, ok := gitUnderLimits(req.use, repo, false, "rev-parse", "--abbrev-ref", "HEAD"); ok && s != "HEAD" {
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
	// measured against f6010b97 and 90fca6e6 on Windows. For a linked-worktree
	// baseRepo that is its own admin dir. The references ask git for it with
	// rev-parse --absolute-git-dir, on the heavy profile, before the add.
	//
	// A junction that was put at `.git` before that call: the refusal of the
	// config, with the words of a git directory that cannot be located
	// (measured).
	if msg := req.use.swapRefusal("gitdir", repo); msg != "" {
		return refuseAdd(gitDirAddWrap + msg)
	}
	gitDir, _, gitAnswered, gitDirErr := worktreeBaseGitDir(pairSSHUse(req, bool(p.ShadowDirOff)), repo)
	// An answer that names another git directory than the held one, while the
	// path still names the held folder, gets one more call. A right second answer
	// goes on, and the create succeeds. A wrong one stops the create with the
	// "disagree" text, and the leaf does not stay. That is c9125eb8, measured on
	// Linux and Windows. macOS, a failed second call and a baseRepo spelled
	// otherwise than its work tree are claustrum's own, not measured. So is the
	// form of an answer with forward slashes on Windows: it follows the probe rule
	// of b3081b51 (the Windows row printed backslashes).
	if gitDirErr == nil && gitAnswered && req.use.accepted.answersOther(gitDir) {
		var printed string
		// The member never reaches this call: only a use that names the copy
		// holds a git directory (from the code).
		gitDir, printed, gitAnswered, gitDirErr = worktreeBaseGitDir(pairSSHUse(req, bool(p.ShadowDirOff)), repo)
		if gitDirErr == nil && gitAnswered && req.use.accepted.answersOther(gitDir) {
			shown := printedProbePath(printed, gitDir, string(filepath.Separator), filepath.IsAbs(filepath.FromSlash(printed)) && printedPathAsIs)
			return refuseAdd(gitDirAddWrap + disagreeText(repo, shown, req.use.accepted.foundGitDir()))
		}
	}
	// Where gitDirMustBeReal is set, a failed call stops the create in the
	// state shadow. 5fd08069 then makes no other git call and answers `git
	// worktree add failed: cannot locate the repository's git directory: exit
	// status 128`. That is measured on Windows: the call exits 128 after
	// `.git` became a junction to a folder that is no git directory. The text
	// after the colon is the exec error. On Linux 5fd08069 answers the same
	// text (measured). On macOS 4c111e86 stops the same way for a call that
	// exits 1 (measured). A call that a signal ended is not measured. With "shadowDirOff":true a
	// call that exits 1 with no answer stops the create the same way, and no
	// leaf stays (4c111e86, measured on Linux, macOS and Windows). Another
	// failure there, and the other states, keep the old path: the create goes
	// on with the directory of repoGitDir (not measured).
	if gitDirMustBeReal && req.use.shadow() && copyGone(gitDirErr) {
		return refuseAdd(gitDirErr.Error())
	}
	var gitDirExit *exec.ExitError
	offExit1 := bool(p.ShadowDirOff) && errors.As(gitDirErr, &gitDirExit) && gitDirExit.ExitCode() == 1
	if gitDirMustBeReal && (req.use.shadow() || offExit1) && gitDirErr != nil {
		return refuseAdd(gitDirAddWrap + gitDirErr.Error())
	}
	// A create of a plain repository makes <repo>/.git a session folder
	// (b3081b51, measured on Linux, macOS and Windows). The record stays after
	// git.worktree_remove. c9125eb8 and 4c111e86 write it before the add, so a
	// failed add keeps it (measured on Linux and macOS). A failed record logs its
	// line and the create goes on (measured on Linux and macOS). 4c111e86 writes
	// it right after the answer of `rev-parse --absolute-git-dir`, before the next
	// git call, so each later refusal keeps it (measured on Linux and macOS). For a
	// linked worktree the record names the main `.git` (measured on Linux and
	// macOS). The record after the second call of a wrong answer, no record after
	// a failed call that stops the create, the folder of the git answer when no pin is known, no
	// record with the off switch and Windows are claustrum's own, not measured.
	//
	// On Linux and macOS a path that changed right after an answer that named the
	// held folder can move the hold at the add (acceptedGitDir.mayAskAgain). The
	// record then waits for the second answer and names its folder (measured on
	// Linux). A record that still waits at the reply names the pin (claustrum's
	// own, not measured). The order on Windows is not measured.
	answeredSwap := gitAnswered && req.use.swapped("answered")
	recorded := t.pinCommonDir
	if recorded == "" {
		recorded = gitDir
	}
	recordWaits := answeredSwap && !stagedAddWindowsSteps && req.use.shadow() && req.use.accepted.mayAskAgain(gitDir, repo)
	saveWaitingRecord := func() {
		if !recordWaits {
			return
		}
		recordWaits = false
		if !sessiondirs.OffSwitchSet(req.Params) {
			_ = sessiondirs.RememberCreate(recorded)
		}
	}
	if recordWaits {
		defer saveWaitingRecord()
	} else if !sessiondirs.OffSwitchSet(req.Params) {
		_ = sessiondirs.RememberCreate(recorded)
	}
	// After a call that answered, the path of the git directory must still name
	// the held folder. On Windows the create is refused here (measured: a
	// junction that was put at `.git` after the call). The text names the git
	// directory of the request. b3081b51 starts it with `cannot add a
	// worktree: ` (measured on Windows), where 5fd08069 has `git worktree add
	// failed: `. A symbolic link there is not measured on b3081b51: it gets
	// the same text. Two states get no refusal at this step on
	// Windows (answeredElsewhere). On Linux and macOS 5fd08069 makes more calls
	// before it refuses (measured on macOS): the refusal is at the step of the
	// add (swapRefusal, worktreeAdd).
	//
	// On Linux and macOS two rows of 5fd08069 come first:
	//
	//   - A `.git` that became a file with `gitdir: <X>` after the call: the
	//     create stops with `git worktree add failed: cannot locate the
	//     repository's git directory: lstat <repo>/.git/commondir: not a
	//     directory`, and that call is its last (measured on
	//     Linux and macOS). An
	//     lstat of `commondir` in the answered directory that fails with
	//     another error than "does not exist" gives that text. Only "not a
	//     directory" is measured.
	//   - Git answered another real directory than the held one, because the
	//     path leads to it since a step before the call: the create goes on in
	//     that directory (acceptedGitDir.movedTo).
	if answeredSwap {
		switch {
		case req.use.accepted.noteJunctionMove(repo):
			// The step of the add comes next, with the second repository in GIT_DIR.
		case stagedAddWindowsSteps && answeredElsewhere(req.use, gitDir):
			// No refusal here: the step of the add comes next.
		case stagedAddWindowsSteps:
			return refuseAdd("cannot add a worktree: " + listEntriesPrefix + realDirText(req.use.gitDir))
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
	// On Windows the stale step runs here, before the listing of the add. A
	// refusal between the mkdir of the leaf and this point leaves the stale
	// entry in place (not measured).
	if staleStepAfterGitDir {
		staleMatched, staleRemoved, staleLocked = staleStep()
	}
	// A caller-supplied timeoutMs (4534d86) bounds the checkout, and the daemon
	// tests it after the add and after the copy step. At timeoutMs 0 both
	// contexts are the context of the request. The deadline counts from arrival.
	baseCtx, callerCtx, cancelCtx := worktreeCreateCtx(req.stop, p.TimeoutMs, arrival)
	defer cancelCtx()
	var afterAddListing func()
	var pairFor func(gitDir string) func()
	// In the state shadow a stale entry that the step removed does not stop the
	// pair. c9125eb8 and 4c111e86 then run it after the listing of the add, right
	// before the add (measured on Linux). macOS follows Linux (not measured).
	// Windows keeps the old rule (not measured).
	staleGone := staleRemoved && req.use.shadow() && !stagedAddWindowsSteps
	// In the state shadow one stale entry that stays for its `locked` file gives
	// the pair too. 4c111e86 runs the listing of the add and the pair, and then
	// it answers the locked refusal below (measured on macOS). Linux follows macOS
	// (not measured). With the member it gives no pair (measured on macOS). A
	// daemon GIT_COMMON_DIR skips the refusal below, so it gives no pair either
	// (claustrum's own, not measured).
	staleLockedPair := staleLocked && req.use.shadow() && !daemonCommonDirSet()
	if (req.use.shadow() || bool(p.ShadowDirOff)) && (!staleMatched || staleGone || staleLockedPair) {
		// A request with "shadowDirOff":true gets no shadow use (settingsGate), so
		// req.use.shadow() is false there (from the code).
		pairFor = func(gitDir string) func() {
			pairTree := repo
			if p.WorktreeRoot != "" {
				pairTree = p.WorktreePath
			} else if staleGone && filepath.Base(gitDir) == ".git" {
				// After a removed stale entry 4c111e86 gives the pair the resolved top
				// of the repository, the parent of the git directory, also for a
				// baseRepo through a link (measured on Linux). The other pairs keep
				// repo, and the test of the name .git is claustrum's own (not measured).
				pairTree = filepath.Dir(gitDir)
			} else if stagedAddWindowsSteps {
				pairTree = resolvedPairTree(req.use.accepted, repo, p.WorktreePath)
			}
			return pairBeforeAdd(req, gitDir, repo, pairTree, req.use.shadow(), bool(p.ShadowDirOff), staleGone || staleLockedPair)
		}
		afterAddListing = pairFor(gitDir)
	}
	// The caller's deadline does not kill the add. The daemon waits until the 2
	// minute limit.
	// Measured against f6010b97 and 90fca6e6 on macOS. Each failure frame quotes
	// git's stderr through worktreeGitText.
	//
	// A junction that was put at `.git` before the listing of the add: the
	// refusal of the config after `git worktree add failed: ` (measured).
	if msg := req.use.swapRefusal("add", repo); msg != "" {
		return refuseAdd("git worktree add failed: " + msg)
	}
	// A path that changed right after the answer: after the two calls of that
	// gate 4c111e86 asks git again, and the create goes on in the folder of the
	// second answer (measured on Linux, for a link to a copy). The record comes
	// right after that call. The pair then names that folder. A second call that
	// gives no hold keeps the refusal of the add.
	if recordWaits && !req.use.accepted.stands() {
		again, next := req.use.accepted.askedAgain(pairSSHUse(req, bool(p.ShadowDirOff)), repo)
		if next != nil {
			defer next.release()
			req.use.accepted = next
			gitDir, recorded = again, again
			if pairFor != nil {
				afterAddListing = pairFor(gitDir)
			}
		}
	}
	saveWaitingRecord()
	// With "shadowDirOff":true, a registrations folder that is a link or a file
	// refuses the add after its listing (offWorktreesRefusal in worktreeAdd). A
	// create that took the copy at a later gate has the test of a hold instead.
	offMember := bool(p.ShadowDirOff) && !req.use.shadow()
	// incompleteAdd answers an add that git ended well and that the daemon did
	// not complete (addIncomplete), with the rollback of a failed checkout. On
	// 5fd08069 the leaf is gone and the new branch stays, with the text of the
	// branch step (measured).
	//
	// A refusal of the staging step (addRefused) answers unsafe_path with the
	// same rollback. On 5fd08069 the leaf and the new branch are gone
	// (measured on Windows), and so are they on be1c1ca5 after a `.git`
	// planted in the leaf (measured on Linux). A refusal with failedAdd gets
	// the text and the code of addIncomplete instead.
	incompleteAdd := func(err error, branch string) (rpc.Response, bool) {
		var incomplete *addIncomplete
		var refused *addRefused
		text, code := "", "worktree_add_failed"
		switch {
		case errors.As(err, &incomplete):
			text = completingAddPrefix + incomplete.text
		case errors.As(err, &refused) && refused.failedAdd:
			text = completingAddPrefix + refused.text
		case errors.As(err, &refused):
			text, code = refused.text, "unsafe_path"
		default:
			return rpc.Response{}, false
		}
		// After a junction move no rollback runs: its branch step goes by the path,
		// which leads to the second repository (claustrum's own, not measured).
		if req.use.accepted.junctionMoved() {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: text, ErrorCode: code}), true
		}
		reg := testedRegistration{}
		if incomplete != nil && incomplete.keepRegistration {
			// A `locked` file that cannot be removed keeps the moved
			// registration. be1c1ca5 keeps it whole on Linux and macOS, and the
			// leaf and the new branch go (the maintainer's choice for every
			// system: on Windows be1c1ca5 keeps `locked` alone).
			reg.noRegistration = true
		}
		if refused != nil {
			// The refusal comes before the staging folder is made, or the
			// move removed the moved registration again, so the rollback
			// deletes no registration. It does not follow the `.git` file of
			// the leaf. On 5fd08069 the leaf and the branch go (measured on
			// Windows).
			reg.noRegistration = true
		}
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, branch, checkpoint, reg)
		if refused != nil && refused.leafDeleted {
			// A leaf deleted right after a staged add: an empty folder at the
			// path goes (4c111e86, measured on Linux and macOS), after the
			// branch step (measured on Linux). claustrum removes it by one rmdir.
			// With nothing at the path, 4c111e86 adds the leaf text of a failed
			// rmdir (measured on Linux). claustrum's rmdir finds nothing there (from the code).
			part := goneLeafUndoPart(p.WorktreePath)
			undoMovedAwayLeaf(p.WorktreePath, checkpoint)
			undo = withLeafUndoPart(p.WorktreePath, part, undo)
		}
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	//
	// Both listings run under the create lock: the first right after the add
	// takes it (takeAddLocks), and the second before the create ends its hold
	// (endAddHold). So the add of a second create, which waits for that hold,
	// adds no name between them (claustrum's own, not measured).
	var entriesBefore []string
	entriesDir, entriesListed := "", false
	hold.beforeAdd = func() {
		entriesDir, entriesListed = "", false
		if (adminRecordChecked || stagedAddWindowsSteps) && gitAnswered {
			entriesDir = gitDirRegistryDir(gitDir)
			entriesBefore, entriesListed = registrationNames(entriesDir)
		}
	}
	// A leaf that an entry of the registrations folder still names is refused
	// before the add, with the rollback of a failed add. No git call makes a
	// branch then. be1c1ca5 answers so, measured on Linux, macOS and Windows.
	// The stale step has run before this test. With a daemon GIT_COMMON_DIR the
	// test does not run, and git decides (claustrum's own, not measured). With "shadowDirOff":true a
	// locked entry gets no refusal here, and git decides (registeredLeafRefusal).
	//
	// On Windows a junction at `<git dir>\worktrees` of the held folder refuses
	// the create first, and the registrations behind it are not read. b3081b51
	// answers this text after such a junction came right after `rev-parse
	// --absolute-git-dir` (measured on Windows). With a junction that was there
	// before the request, 87375cc7 gives the same text after the listing of the
	// add and the pair (measured on Windows). An attach add runs its second
	// listing, as with a file there, and a directory symbolic link gets the same
	// (claustrum's own, not measured). A request with no settings copy keeps the
	// test of worktreeAdd.
	if stagedAddWindowsSteps && req.use.shadow() {
		if msg := req.use.worktreesLinkRefusal(); msg != "" {
			offLinkListing(baseCtx, req.use, repo)
			if attachBranch != "" {
				offLinkListing(baseCtx, req.use, repo)
			}
			return refuseAdd("cannot add a worktree: " + msg)
		}
	}
	if !daemonCommonDirSet() {
		regDir := verifyGitDir(repo)
		if gitAnswered {
			regDir = filepath.Dir(gitDirRegistryDir(gitDir))
		}
		if msg := registeredLeafRefusal(regDir, p.WorktreePath, bool(p.ShadowDirOff)); msg != "" {
			if staleLockedPair {
				addListingThenPair(baseCtx, req.use, repo, afterAddListing)
			}
			return refuseAdd(msg)
		}
	}
	// On macOS an attach add runs one more listing in the form of the add before
	// the listing of the add (attachAddListsTwice).
	if attachAddListsTwice && attachBranch != "" {
		offLinkListing(baseCtx, req.use, repo)
	}
	addStderr, addErr := worktreeAdd(baseCtx, req.use, repo, p.WorktreePath, checkpoint, offMember, afterAddListing, addArgs...)
	added = addErr == nil
	if resp, ok := incompleteAdd(addErr, createdBranch); ok {
		return resp
	}
	// A staged add whose leaf another folder replaced right after the add is
	// refused with the text of a replaced leaf (leafMovedAfterAdd). 4c111e86
	// keeps the registration and the new branch, and it removes the empty
	// folder at the path (measured on Linux and macOS). claustrum removes only an
	// empty folder there, with the guards of a failed add (undoMovedAwayLeaf),
	// never as a tree (the maintainer's decision of 2026-11-18). A folder with an
	// entry, a file and a link stay (claustrum's own, not measured).
	movedAwayReply := func(err error) (rpc.Response, bool) {
		var movedAway *leafMovedAfterAdd
		if !errors.As(err, &movedAway) {
			return rpc.Response{}, false
		}
		undoMovedAwayLeaf(p.WorktreePath, checkpoint)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: leafReplacedText(parentResolvedPath(p.WorktreePath)), ErrorCode: "unsafe_path"}), true
	}
	if resp, ok := movedAwayReply(addErr); ok {
		return resp
	}
	// An add whose repository folder went gets the rollback of a failed add,
	// with the text alone and no fallback add (from the code, not measured).
	// So does an add whose folder `.s<n>` cannot be made on Linux and macOS
	// (addOwnFiles, be1c1ca5 on Linux). So does an add whose git directory was
	// swapped after its listing (addSwappedAfterListing, 4c111e86 on Linux).
	// The attach mode is claustrum's own, not measured.
	var ownFiles *addOwnFiles
	var swappedLate *addSwappedAfterListing
	if copyGone(addErr) || errors.As(addErr, &ownFiles) || errors.As(addErr, &swappedLate) {
		return refuseAdd(addErr.Error())
	}
	// On Windows an entry at `<git dir>\worktrees` that is no folder starts the
	// text with `cannot add a worktree: `, after the pair, and an attach add runs
	// its second listing first (addNotPrepared.ownWords, 87375cc7, measured on
	// Windows for a file).
	var ownWords *addNotPrepared
	if errors.As(addErr, &ownWords) && ownWords.ownWords {
		if attachBranch != "" {
			offLinkListing(baseCtx, req.use, repo)
		}
		return refuseAdd("cannot add a worktree: " + ownWords.text)
	}
	// A stopped attach add gets no fallback, so one add runs, as on 5fd08069
	// (measured on Linux).
	if addErr != nil && attachBranch != "" && !isGitStop(addErr) {
		// A refused attach falls back to a new branch, unless the failed attach add
		// deleted the leaf or put a new directory in its place. Then the request
		// answers the attach add's own failure. Measured against f6010b97 and 90fca6e6
		// on macOS, with the leaf deleted and with it replaced by a new empty
		// directory. A leaf left as it was gets the fallback. An add whose worktrees
		// folder could not be prepared gets no fallback either (addNotPrepared): the
		// frame has its text one time (measured on Linux, for a link at `worktrees`).
		var notPrepared *addNotPrepared
		if errors.As(addErr, &notPrepared) || checkNewLeafIdentity(p.WorktreePath, checkpoint) != "" {
			// In attach mode 5fd08069 runs two listings after `rev-parse
			// --absolute-git-dir` and answers the text one time (measured on
			// Linux and macOS). claustrum runs the second one here, or on macOS
			// before the add (attachAddListsTwice). Under a hold in attach mode
			// the second listing is not measured.
			if notPrepared != nil && notPrepared.offLink && !attachAddListsTwice {
				offLinkListing(baseCtx, req.use, repo)
			}
			undoFailedAdd(p.WorktreePath, checkpoint)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
		// Measured against f6010b97 and 90fca6e6 on macOS.
		attachStderr, attachErr := addStderr, addErr
		fallbackArgs := []string{"worktree", "add", "--no-track", "--no-checkout", "-b", p.BranchName, p.WorktreePath}
		if startCommit != "" {
			fallbackArgs = append(fallbackArgs, startCommit)
		}
		addStderr, addErr = worktreeAdd(baseCtx, req.use, repo, p.WorktreePath, checkpoint, offMember, nil, fallbackArgs...)
		added = addErr == nil
		if resp, ok := incompleteAdd(addErr, p.BranchName); ok {
			return resp
		}
		if resp, ok := movedAwayReply(addErr); ok {
			return resp
		}
		if addErr != nil {
			undoFailedAdd(p.WorktreePath, checkpoint)
			// The note quotes the name of the branch and cuts it at 100 bytes,
			// with no mark of the cut. b3081b51 does so where be1c1ca5 writes the
			// name with no quotes (measured on Linux, macOS and Windows). The Go
			// quotes are claustrum's own for a name with a quote or a byte that
			// prints no letter (not measured).
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success: false,
				Error: fmt.Sprintf("git worktree add failed: %s (attaching to the existing branch %q was refused first: %s)",
					worktreeGitText(addStderr, addErr), branchNoteName(attachBranch), worktreeGitText(attachStderr, attachErr)),
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
		// empty, or on Linux and macOS a file or a link in its place (undoFailedAdd). A branch that existed before the call is kept.
		// Measured against f6010b97 and 90fca6e6 on macOS.
		//
		// An add that its time limit stopped gets this rollback too, with the stop
		// text. On 5fd08069 the leaf is gone, and no git call follows (measured. One
		// case ran on Linux, macOS and Windows, one on Linux and macOS, and one on
		// Linux). A staged path in the text of git becomes "the new folder"
		// (stagedAddText). The two texts of an attach add above keep the path (not measured).
		undoFailedAdd(p.WorktreePath, checkpoint)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     "git worktree add failed: " + stagedAddText(addStderr, addErr, p.WorktreePath),
			ErrorCode: "worktree_add_failed",
		})
	}
	// A junction that moved to a second repository before the add: the refusal of
	// 4c111e86 and 87375cc7, with no checkout and no rollback (noteJunctionMove).
	if req.use.accepted.junctionMoved() {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     leafMovedText(p.WorktreePath),
			ErrorCode: "unsafe_path",
		})
	}
	// On Linux and macOS, a git directory that is no longer the held folder
	// right after a good `worktree add` refuses the create. Nothing is rolled
	// back, and no git call follows. 5fd08069 answers this text with
	// `unsafe_path` in two rows. In one measured case on Linux and macOS the
	// link is put at `.git` after the call ended, and the real git directory
	// keeps the registration and the branch. In another case on macOS the
	// link to a copy T is put there while a wrapper holds the call before git
	// runs, and git makes the registration and the branch in T. How 5fd08069
	// finds that state is not measured. A staged add has its own test
	// (addStage.finish), so on Linux and macOS this test runs only for an add
	// that names no copy.
	//
	// A real directory at the path of the git directory that passes the gate is
	// the exception: the create goes on in it (acceptedGitDir.replacedAtPath).
	// That is measured on Linux, by the maintainer's decision of 2026-10-14.
	if !stagedAddWindowsSteps && req.use.swapped("added") {
		next := req.use.accepted.replacedAtPath(repo)
		if next == nil {
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	// expired during the add, 89cb6289 answers this refusal (measured on macOS).
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
	// is removed when it is empty, as after a failed add (measured). Without an
	// answer of git those tests do not run (claustrum's own, not measured).
	refuseUnsafe := func(text string) rpc.Response {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: text, ErrorCode: "unsafe_path"})
	}
	// On Windows a create with "shadowDirOff":true tests the leaf here, before
	// any other git call (offAddRefusal). A refusal rolls nothing back: the
	// leaf, the registration and the branch stay (4c111e86, measured on
	// Windows). Without an answer of git the test does not run
	// (Windows: claustrum's own, not measured).
	if windowsAddTests && bool(p.ShadowDirOff) && gitAnswered {
		if text := offAddRefusal(p.WorktreePath, gitDirRegistryDir(gitDir), checkpoint); text != "" {
			return refuseUnsafe(text)
		}
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
		// With "shadowDirOff":true a link in place of the git directory that git
		// answered gets the text of a gone `.git` file, with no checkout. The leaf,
		// the registration and the branch stay (4c111e86, measured on Linux and
		// macOS). A link made after the answer of git and before the add does not
		// stop the add, and this test refuses it (claustrum's own, not measured).
		if bool(p.ShadowDirOff) && gitDirIsLinkNow(gitDir) {
			return refuseUnsafe(gitFileGoneText(parentResolvedPath(p.WorktreePath)))
		}
	} else {
		adminDir = linkedGitDirOf(p.WorktreePath)
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
	// The registration of the staging step is the one that gets the index and
	// that a rollback removes, by its identity (stagedRegistration).
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
	// The tests that read the registrations folder after the add are done: a
	// second create of the repository may add now (worktreeCreateLocks).
	hold.endAddHold()
	// indexDir is the registration that gets the index (createdRegistrationRefusal, or
	// absoluteAdminDir if rev-parse gave no answer). Its gitdir record names worktreePath
	// after symlink resolution. If it was read and names anything else, the create is
	// refused, with no checkout and no rollback (measured). Before the answer, 89cb6289 runs
	// the pair of gitDirWorkTreeToplevel with baseRepo as the work tree (measured on Linux
	// and macOS). In a measurement on Linux and macOS the call log of 89cb6289 shows that
	// pair twice, with baseRepo as the resolved path. The call log of claustrum shows it
	// once, with baseRepo as sent. No frame differs. This test comes before the deadline test
	// too: with a timeoutMs that expired during the add, 89cb6289 answers this refusal and
	// keeps the leaf and the branch (measured on Linux and macOS).
	//
	// When the second call of the pair fails, 5fd08069 answers the text of
	// spellingRetryText and removes the leaf, the new registration and the new
	// branch (measured on Linux). The text has the name that the .git file of the
	// leaf gives. In claustrum the registration that goes is the one new name of
	// the registrations folder since the listing before the add (newEntry). In a
	// measurement on Linux (3 of 3 runs) the .git file names an old entry w1 of
	// another live worktree, and git made the entry w11. 5fd08069 answers "(entry
	// w1)" and removes the leaf, w11 and the new branch. The entry w1, its worktree
	// and its branch stay. The rollback is that of a failed checkout
	// (undoFailedCheckout), with its home guard, its identity tests and the held
	// git directory. For another name than that of the tests, the identity is that
	// of the folder at the name in the listing after the add.
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
		_, err := gitDirWorkTreeToplevelIn(req.stop.context(), pairUse.sshOff(), gitDir, repo, pairUse.gitDirPin(repo), pairUse.gitDirFront())
		// A call whose repository folder went answers its text, with the same
		// rollback (from the code, not measured).
		if copyGone(err) {
			undo, kept := "", false
			if newEntry != "" {
				undo, kept = undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, created)
			}
			return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: false, Error: err.Error() + undo,
				ErrorCode: "worktree_add_failed", BranchKept: kept})
		}
		if err != nil && !isGitStop(err) && newEntry != "" {
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, created)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	// file, 5fd08069 answers this text, and the new folder holds .git only
	// (measured on Linux). Nothing is removed. A leaf whose path leads elsewhere or
	// is no folder gets the same text (claustrum's own, not measured). Without an
	// answer of git to rev-parse --absolute-git-dir, and on Windows, the test stays
	// after the checkout alone.
	//
	// A leaf can be deleted and replaced by a new empty folder right before the
	// add. git then writes its .git file into the new folder. The create answers
	// the text of a gone .git file, with the path in backticks. The rollback of a
	// failed add runs, and the new folder stays because it is not empty. The registration and the branch
	// stay. c9125eb8 and 4c111e86 do so (measured on Linux with shadowDirOff).
	// claustrum reads the .git entry through the held leaf (heldLeafLacksGitFile).
	// How the references read it is not measured, and neither is macOS.
	if registrationTests {
		if checkNewLeafIdentity(p.WorktreePath, checkpoint) != "" {
			if heldLeafLacksGitFile(checkpoint) {
				undoFailedAdd(p.WorktreePath, checkpoint)
				return refuseUnsafe("refusing to create worktree: " + tickPath(parentResolvedPath(p.WorktreePath)) + " has no .git file")
			}
			return refuseUnsafe(leafReplacedText(parentResolvedPath(p.WorktreePath)))
		}
		req.use.repointLeaf(repo, p.WorktreePath, checkpoint)
	}
	// The read of the `config.worktree` file of the repository, and the text
	// that the new worktree gets from it (newWorktreeConfig). The text goes into
	// the registration below.
	entryConfig, hasEntryConfig, dropConfigDir, configCause, configWritten := req.newWorktreeConfig(repo, t)
	defer dropConfigDir()
	// A failure of that step fails the create with no checkout, and the undo of a
	// failed checkout runs (measured on Linux, macOS and Windows). Its branch
	// step reads the config of the repository again first (branchGate). With a
	// refusal there it keeps the branch with that refusal as its cause, as
	// 5fd08069 does (measured on Linux and macOS).
	filesFailed := func(cause string) rpc.Response {
		dropConfigDir()
		u := req.use
		u.branchGate = func() string {
			msg, _ := repoConfigGate(t, false, req.stop)
			return msg
		}
		undo, kept := undoFailedCheckout(u, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	// registration stays (measured on Windows). claustrum tests at two steps
	// before the listings of the checkout: before the file of the registration
	// and before the first listing. Neither is a row of 5fd08069: that build
	// makes no call between its `worktree add` and that listing. Two more tests
	// are in the checkout: one on Windows, and one on every system
	// (errGitDirChanged). goneCheckout answers a checkout that stops for such a
	// git directory, with detail after the words of the checkout.
	goneCheckout := func(detail string) rpc.Response {
		hold.retake()
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:    false,
			Error:      "git worktree add failed (checkout): " + detail + undo,
			ErrorCode:  "worktree_add_failed",
			BranchKept: kept,
		})
	}
	swappedCheckout := func(step string) (rpc.Response, bool) {
		if !req.use.swapped(step) {
			return rpc.Response{}, false
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
	// If the caller's deadline expired by the end of the add, the request
	// answers timeout "before the checkout started" and rolls back. errorCode
	// "timeout" is reserved for the caller's own deadline.
	// Every rollback below appends an undo text when one of its steps fails.
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired before the checkout started)", p.TimeoutMs)
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
	// 90fca6e6 on Windows. A checkout that fails also fails the request. See the last arm of
	// the switch.
	var checkUse *gitUse
	var keepTook time.Duration
	var checkCommit string
	var checkAsk func() string
	if indexDir != "" {
		// The filter rule runs in the checkout (gitfilter.go). 5fd08069 refuses after
		// its read-tree call, and rolls back as after a failed checkout (measured on
		// Linux). The refusal of a name over the bound has the same end state
		// (measured), and 5fd08069 makes no read-tree call for it (measured on
		// Windows). The refusal of a name with "=" (measured on Linux) and claustrum's
		// own refusal of a filter that no pair can replace come before the read-tree
		// call. In the state shadow they come after its second listing and the cap
		// (hardenedGitCheckout). In the state fallback-git and with
		// "shadowDirOff":true the rule does not run (filterRule).
		if resp, ok := swappedCheckout("checkout"); ok {
			return resp
		}
		// The checkout takes the form of the off switch at its first listing and
		// keeps it to its end (fixedForm, claustrum's own). A known gap: after a
		// hold that ended right after the read-tree call, 4c111e86 gave the
		// listing and the call of the settle step the copy (measured on macOS),
		// where claustrum keeps the form of the hold. Under a live hold the index
		// goes in a new folder `claude-ssh-checkout-<n>` of the temporary folder
		// (4c111e86, Linux and macOS, the name also on Windows. c9125eb8 used
		// `claude-ssh-index-<n>`).
		// 5fd08069 runs one listing there before the read-tree call. With no hold it
		// puts the index in `.index-<n>` of the folder of the daemon and runs two
		// (measured on Linux). The filter rule follows that form too (claustrum's
		// own, not measured). The index goes into the registration through the
		// held git directory, as before.
		// On macOS a later gate here asks git about the repository after the start
		// test and before the read of the config (createGateAsksAfterAdd), for a
		// checkout of a branch. A commit id keeps the call of its content check
		// after the read (claustrum's own, not measured).
		req.askInGate = createGateAsksAfterAdd && !isObjectID(checkoutRev)
		cu, offNow := req.use.fixedForm(false)
		req.askInGate = false
		offNow = offNow || (!cu.shadow() && bool(p.ShadowDirOff))
		// In the state shadow the read-tree call names the commit id of the
		// branch. b3081b51 does so where be1c1ca5 names refs/heads/<branch>
		// (measured on Linux and macOS). The id comes from one `rev-parse
		// --verify --quiet <ref>^{commit}`. b3081b51 runs that call in a create
		// on Windows (measured). Its place and its `-c` items are claustrum's
		// own. A live hold with no member, the member and the state
		// fallback-git are below. A call
		// that fails leaves the ref, and then no content check runs
		// (claustrum's own, not measured).
		//
		// The call asks git no new question about the repository (recheck).
		// Skipping recheck there is claustrum's own (not measured). The content
		// check rechecks (checkWorktreeContent below).
		quiet := cu
		quiet.noRecheck = true
		// After the add of a commit id from sourceBranch the content check asks
		// git about the repository instead, before its batch (checkAsk).
		// With a worktreeRoot it reads the kept answer of the ask below.
		// b3081b51 and c9125eb8 make that call there on macOS, and c9125eb8 on
		// Linux (measured). A wrong answer on every call fails the create with
		// worktree_verify_repository after 6 calls, and the branch step keeps
		// the branch with the same text (measured on macOS).
		if cu.shadow() && !offNow && isObjectID(checkoutRev) {
			checkAsk = func() string { return cu.askAfterAdd(repo) }
			// With a worktreeRoot git is asked right after the add, and the
			// location calls run again (recheckLocation). That place is
			// claustrum's own. 4c111e86 and c9125eb8 make the same 45 calls
			// with one ask (the counts measured on Linux).
			// The answer is not used here. The content check gets a good one
			// again as a kept answer. A bad answer is not kept, so the content
			// check asks again, and only a second bad answer refuses
			// (claustrum's own, not measured).
			if p.WorktreeRoot != "" {
				_ = cu.askAfterAdd(repo)
				recheckLocation(quiet, repo)
			}
		}
		if cu.shadow() && !offNow && !isObjectID(checkoutRev) {
			// On Linux and macOS git is asked about the repository first
			// (askAfterAdd). A bad answer fails the create with the rollback of a
			// failed checkout, and its branch step asks again. c9125eb8 answers
			// `git worktree add failed: branch "<b>" could not be resolved to a
			// commit (<text>)`, and it keeps the branch with that text as the
			// cause (measured on macOS). The branch of an attach add is
			// claustrum's own, not measured. On Windows git is asked at the
			// same place, and the request goes on (createRechecksAfterAdd).
			if createRechecksAfterAdd {
				cu.recheck(repo)
			}
			if text := cu.askAfterAdd(repo); text != "" {
				u := req.use
				u.branchGate = func() string { return cu.askAfterAdd(repo) }
				undo, kept := undoFailedCheckout(u, repo, p.WorktreePath, createdBranch, checkpoint, tested)
				return rpc.OKResult(req.ID, rpc.WorktreeReply{
					Success:    false,
					Error:      fmt.Sprintf("git worktree add failed: branch %q could not be resolved to a commit (%s)", worktreeBranch, text) + undo,
					ErrorCode:  "worktree_add_failed",
					BranchKept: kept,
				})
			}
			// With a worktreeRoot the location calls run again here
			// (recheckLocation).
			if p.WorktreeRoot != "" {
				recheckLocation(quiet, repo)
			}
			if id := resolveCommit(quiet, repo, checkoutRev); id != "" {
				checkoutRev = id
			}
		}
		// With the member the read-tree call names the commit id too, from the
		// same call, with no question about the repository first (c9125eb8 and
		// b3081b51, measured on Linux). On macOS and Windows it is claustrum's
		// own, not measured. Under a hold of another request that is live at
		// the checkout, a create with no member makes the commit call in the
		// form of the hold and the settle step too (4c111e86, measured on macOS
		// for a hold that ends after the read-tree call). A hold still live at
		// the settle call, Linux and Windows are claustrum's own, not measured.
		// The settle step keeps the form of the hold, where 4c111e86 gives it
		// the copy (the known gap above). In the state fallback-git a create
		// without the member makes the commit call and the settle step too
		// (c9125eb8 and 4c111e86, measured on Linux, claustrum's own on macOS and
		// Windows). With the member it makes neither (claustrum's own, not
		// measured).
		member := bool(p.ShadowDirOff) && !cu.shadow() && req.shadow.readsRepoConfig()
		heldNoMember := offNow && !bool(p.ShadowDirOff) && req.shadow.readsRepoConfig()
		byCommit := member || heldNoMember || (!bool(p.ShadowDirOff) && !req.shadow.readsRepoConfig())
		if byCommit && !isObjectID(checkoutRev) {
			if id := resolveCommit(quiet, repo, checkoutRev); id != "" {
				checkoutRev = id
			}
		}
		if cu.shadow() && !offNow && isObjectID(checkoutRev) {
			checkUse, checkCommit = &quiet, checkoutRev
		}
		var rule *filterRule
		if !offNow && req.shadow.readsRepoConfig() {
			rule = &filterRule{}
			if cu.shadow() && cu.read != nil {
				rule.shadow, rule.own = true, cu.read.ownConfig
			}
		}
		var rtStderr string
		var rtDrained bool
		var rtErr, installErr error
		// A second create of the repository goes on while this one checks out.
		// Each rollback below takes the lock again first (withWorktreeCreateLock).
		hold.release()
		rtStderr, rtDrained, keepTook, rtErr, installErr = runWorktreeCheckout(callerCtx, cu, checkoutFolder(p.WorktreePath, checkpoint.resolved),
			checkoutWorkTree(p.WorktreePath, checkpoint.resolved), gitDir,
			indexDir, repo, tested, checkoutRev, cu.gitDirPin(repo), rule, cu.shadow() || byCommit, p.TimeoutMs, arrival)
		var filters filterPlan
		if rule != nil {
			filters = rule.plan
		}
		refusedName, filterRefused := filters.refusedFilter(rtStderr)
		var refusal checkoutRefusal
		var room *lfsRoomStop
		switch {
		case errors.As(rtErr, &refusal):
			// The second listing of the checkout failed, the repository has too
			// many filters, or, in the state shadow, the plan has a refusal.
			// 5fd08069 runs no read-tree call there and rolls back. Measurements
			// show it on Linux, macOS and Windows.
			return goneCheckout(string(refusal))
		case errors.Is(rtErr, errGitDirChanged):
			return goneCheckout(repoConfigPrefix + gitDirLinkText(req.use.gitDir))
		case errors.Is(rtErr, errGitDirNotReal):
			return goneCheckout(realDirText(req.use.gitDir))
		case installErr != nil:
			// git exited 0, and the index did not reach the registration. On Linux and
			// macOS 89cb6289 answers that as a failed checkout and rolls back (measured). The
			// text is indexInstallText. A placement that fails after a drain overrun takes
			// this arm too. That is claustrum's choice (not measured).
			msg := "git worktree add failed (checkout): " + indexInstallText(rtStderr, installErr)
			hold.retake()
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "worktree_add_failed",
				BranchKept: kept,
			})
		case rtDrained:
			// git exited 0, but a checkout descendant held the daemon's output pipe
			// past the ~5s drain cap. The checkout counts as finished, so the request goes
			// on to the copy step. The deadline test after the copy step then answers
			// timeout "after the checkout finished" and rolls back. If the deadline has
			// not expired, the request succeeds. Measured against f6010b97 and
			// 90fca6e6 on macOS.
		case errors.As(rtErr, &room):
			// The watch stopped a download that left under a tenth of the free
			// space at the start on the disk of the private LFS store
			// (lfsroom.go). The rollback of a failed checkout runs and the
			// private folder goes, both before the reply (4c111e86, measured on
			// Linux). The place of this arm before the deadline arm and the
			// filter arms is claustrum's own, not measured.
			hold.retake()
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			room.removeStore()
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      lfsRoomText(room) + undo,
				ErrorCode:  "worktree_lfs_no_space",
				BranchKept: kept,
			})
		case rtErr != nil && p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) && !errors.As(rtErr, new(checkoutFailedAlone)):
			// The caller's deadline killed the checkout. The text after "during the
			// checkout): " is the killed git's stderr made by worktreeGitText, or the
			// exec error, for example "signal: killed", when that text is empty.
			// Measured against f6010b97 and 90fca6e6 on macOS. A git that failed by
			// itself before the deadline takes the failure arm below, also when the
			// deadline expires during the drain of its pipes (b3081b51, measured on
			// Linux and macOS).
			//
			// A deadline after a long copy of the seed answers
			// worktree_lfs_not_kept (notKeptText). So does a deadline after Git
			// LFS downloads began, with a link at the store of the repository or
			// too little room for a file that the keep left out. It reads the
			// private store before the rollback.
			if text, ok := notKeptText(p.TimeoutMs, rule, cu.gitDir); ok {
				hold.retake()
				undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
				return rpc.OKResult(req.ID, rpc.WorktreeReply{
					Success:    false,
					Error:      text + undo,
					ErrorCode:  "worktree_lfs_not_kept",
					BranchKept: kept,
				})
			}
			msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired during the checkout): %s",
				p.TimeoutMs, worktreeGitText(rtStderr, rtErr))
			hold.retake()
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  "timeout",
				BranchKept: kept,
			})
		case rtErr != nil && strings.Contains(rtStderr, sshDoorMark()):
			// The ssh door refused a Git LFS request and wrote its mark to
			// stderr (gitsshdoor.go). be1c1ca5 answers the fixed text (measured
			// on Linux and macOS). Its place before the filter arm is
			// claustrum's own.
			hold.retake()
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      "git worktree add failed (checkout): " + sshDoorRefusedText + undo,
				ErrorCode:  "worktree_add_failed",
				BranchKept: kept,
			})
		case rtErr != nil && filterRefused:
			// git failed at a path that uses a filter with the three pairs. The text
			// names that filter (measured on Linux and Windows). The deadline arm above
			// comes first. A deadline that expires before git answers gives the timeout
			// frame (measured). After a git that failed by itself, this arm wins over it
			// (claustrum's own, not measured). One name has another answer with no
			// settings copy: the filter lfs of the outside config, when the repository
			// names a program for git-lfs (claustrum's own).
			hold.retake()
			undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			if refusedName == lfsFilterName && filters.lfsKey != "" {
				return rpc.OKResult(req.ID, rpc.WorktreeReply{
					Success:    false,
					Error:      "git worktree add failed (checkout): " + lfsProgramText(filters.lfsKey) + undo,
					ErrorCode:  "worktree_add_failed",
					BranchKept: kept,
				})
			}
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
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
			// f6010b97. With a settings copy a text that holds one of lfsNoteWords
			// gets a note after it (checkoutNote). The note goes only in this
			// reply, before any undo text (claustrum's own, not measured beside
			// a failed undo). A write below the private LFS store on a full disk
			// or a quota puts its own start before the text, with no note
			// (storeFullStart). With the private LFS store the text ends before
			// the log line of git-lfs (withoutLFSLog).
			text := worktreeGitText(withoutLFSLog(rtStderr, rule), rtErr)
			msg := "git worktree add failed (checkout): " + text
			code := "worktree_add_failed"
			if start, c := storeFullStart(rtStderr, rule); c != "" {
				msg, code = start+msg, c
			} else {
				msg += checkoutNote(rtStderr, text, rule)
			}
			hold.retake()
			undoUse := req.use
			// Only a read-tree call that ran and exited with a failure can reach a second
			// repository. A stopped listing, a failed folder and a git that did not start
			// keep it unchanged (claustrum's own, not measured).
			if errors.As(rtErr, new(*exec.ExitError)) {
				undoUse.afterJunctionMove = func() (gitUse, bool) {
					ok := req.movedBranchGate(p, repo)
					return req.use, ok
				}
			}
			undo, kept := undoFailedCheckout(undoUse, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			// A git-lfs that the git of the login-shell PATH does not find: the
			// same undo, then git_lfs_not_found with the text of no git and one
			// log line (lfsNotFoundForShellPathGit). The undo text after it is
			// claustrum's own, not measured.
			if code == "worktree_add_failed" && lfsNotFoundForShellPathGit(rtStderr) {
				logLFSNotFound(msg)
				return rpc.OKResult(req.ID, rpc.WorktreeReply{
					Success:    false,
					Error:      "not a git repository" + undo,
					ErrorCode:  "git_lfs_not_found",
					BranchKept: kept,
				})
			}
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      msg + undo,
				ErrorCode:  code,
				BranchKept: kept,
			})
		}
		// After a good checkout on Linux and macOS, a leaf with no `.git` entry
		// refuses the create. The leaf goes (removeCreatedLeaf). The registration
		// and the branch stay. be1c1ca5 answers this text with unsafe_path when a
		// wrapper deletes the `.git` file at the read-tree call (measured on
		// Linux). On Windows 4c111e86 gives the same frame and removes nothing:
		// the leaf keeps the files of the checkout (measured with and without
		// "shadowDirOff":true). Not measured: macOS (it follows Linux),
		// a `.git` entry of another kind, and a leaf that cannot
		// be removed (the text stays alone). The order before the copy step and
		// the deadline test is claustrum's own, not measured: a good checkout
		// whose timeoutMs expired gets this answer, and on Linux and macOS only the leaf goes. A
		// folder that replaced the leaf gets it too, and the identity guard keeps
		// that folder.
		// A junction in baseRepo moved right before a good read-tree call, also to a
		// folder with no git directory. 87375cc7 answers this text and rolls nothing
		// back. The leaf, the index, the branch and the entry stay in the first
		// repository (measured on Windows).
		if req.use.accepted.behindMovedJunction(repo) {
			return refuseUnsafe(leafMovedText(p.WorktreePath))
		}
		if leafGitFileGone(p.WorktreePath) {
			if !windowsAddTests {
				hold.retake()
				removeCreatedLeaf(p.WorktreePath, checkpoint)
			}
			return refuseUnsafe(gitFileGoneText(p.WorktreePath))
		}
	}
	// Post-condition: the add must have populated the directory claustrum created, not
	// one swapped in during the add. A copy of the leaf put in its place at the
	// read-tree call gets the text of a replaced leaf with unsafe_path (4c111e86,
	// measured on Linux and macOS, with and without "shadowDirOff":true). On
	// Windows with "shadowDirOff":true, offAddRefusal answers the same frame
	// earlier for a leaf copied right after the add (4c111e86, measured). A leaf
	// that leads elsewhere or is no folder gets the same frame (not measured).
	if checkNewLeafIdentity(p.WorktreePath, checkpoint) != "" {
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:   false,
			Error:     leafReplacedText(p.WorktreePath),
			ErrorCode: "unsafe_path",
		})
	}
	// After a good checkout b3081b51 compares the config files of the new
	// worktree with the commit (worktreecontent.go). A refusal is a result with
	// its code, and the rollback of a failed checkout runs (measured on Linux,
	// macOS and Windows). The check runs in the state shadow only. The place
	// before the copy step, and no check with "shadowDirOff":true or in the
	// state fallback-git, are claustrum's own (not measured).
	checkStopped := false
	if checkUse != nil {
		// With a worktreeRoot the location calls run again before the check
		// (recheckLocation).
		if p.WorktreeRoot != "" {
			recheckLocation(*checkUse, repo)
		}
		// At a step of the check that sees the timeoutMs deadline, the check stops and the deadline
		// test below answers (contentCheck.pastDeadline). The LFS keep time does not count
		// (keepWithinDeadline, claustrum's own).
		var checkDeadline context.Context
		if p.TimeoutMs > 0 {
			checkDeadline = callerCtx
		}
		// Each call of the check asks git about the repository again before its
		// listing when the kept answer is older than probeKeep (recheck). On
		// 87375cc7 and 4c111e86 a listing held 5 s or more gave one more call,
		// and a listing held 4.5 s (87375cc7) or 4 s (4c111e86) gave none
		// (measured on Linux). macOS and Windows are claustrum's own, not measured.
		check := *checkUse
		check.noRecheck = false
		r, stopped := checkWorktreeContent(check, repo, p.WorktreePath, checkCommit, checkAsk, checkDeadline, keepTook)
		checkStopped = stopped
		if r != nil {
			u := req.use
			if r.asked {
				u.branchGate = checkAsk
			}
			hold.retake()
			undo, kept := undoFailedCheckout(u, repo, p.WorktreePath, createdBranch, checkpoint, tested)
			return rpc.OKResult(req.ID, rpc.WorktreeReply{
				Success:    false,
				Error:      contentRefusalStart + r.hint + contentUndoText(undo),
				ErrorCode:  r.ErrorCode,
				BranchKept: kept,
			})
		}
	}
	// `git worktree add` checks out tracked files only, so the reference seeds the new
	// worktree in two passes: the untracked files that .worktreeinclude names AND git
	// also ignores (copyWorktreeIncludes), and the git-ignored files under .claude/,
	// which need no manifest entry at all (copyDotClaude in worktreeclaude.go).
	//
	// An earlier version of this comment said 7d193f89 had dropped the .claude/ pass.
	// It had not. Measured against 19f30c46 and 90fca6e6 on Linux; the old reading
	// came from a probe repo whose .claude/ was untracked rather than git-ignored, for
	// which the pass lists nothing.
	//
	// Best-effort: the worktree exists, so a copy failure does not fail the request.
	// After a content check that saw the deadline the copy step runs
	// only the config listing of its `.claude/` pass (dotClaudeListingOnly).
	if checkStopped {
		dotClaudeListingOnly(req.use, repo)
	} else {
		populateWorktree(req.use, req.shadow, repo, p.WorktreePath)
	}
	// The caller's deadline does not kill the copy step. The daemon lets the step
	// finish and then tests the deadline. If the deadline expired, the request answers
	// timeout "after the checkout finished" and rolls back. Measured against f6010b97
	// and 90fca6e6 on macOS. The time of the LFS keep does not count
	// (keepWithinDeadline).
	if p.TimeoutMs > 0 && callerTimeoutFired(callerCtx) && !keepWithinDeadline(callerCtx, keepTook, time.Now()) {
		msg := fmt.Sprintf("git worktree add timed out after %dms (deadline expired after the checkout finished)", p.TimeoutMs)
		hold.retake()
		undo, kept := undoFailedCheckout(req.use, repo, p.WorktreePath, createdBranch, checkpoint, tested)
		return rpc.OKResult(req.ID, rpc.WorktreeReply{
			Success:    false,
			Error:      msg + undo,
			ErrorCode:  "timeout",
			BranchKept: kept,
		})
	}
	return rpc.OKResult(req.ID, rpc.WorktreeReply{Success: true, Path: p.WorktreePath, SourceBranch: source, Branch: worktreeBranch})
}

// keepWithinDeadline reports whether the deadline of ctx passed at now only
// because of the LFS keep, which took keepTook. A keep whose copy ran past the
// deadline, kept or stopped, gives success on 4c111e86 (measured on Linux:
// timeoutMs 6500 and 7500, a 900 MiB object). The rule that subtracts the time
// of the keep is claustrum's own, not measured.
func keepWithinDeadline(ctx context.Context, keepTook time.Duration, now time.Time) bool {
	dl, ok := ctx.Deadline()
	return ok && keepTook > 0 && now.Sub(dl) <= keepTook
}

// branchNoteName is name cut at 100 bytes, for the note of a refused attach.
func branchNoteName(name string) string {
	if len(name) > 100 {
		return name[:100]
	}
	return name
}

// entryIsLink reports whether repo is a linked worktree whose `.git` file names
// an entry of the registrations folder that is a symbolic link, where
// entryLinkRefusedLate is set. The daemon's own GIT_DIR is not looked at.
//
// For such a baseRepo, with extensions.worktreeConfig set and a
// `config.worktree` file in the entry, 5fd08069 answers the trust refusal of
// the entry behind the link after `git worktree add failed: cannot locate the
// repository's git directory: `. B/.claude and B/.claude/worktrees exist after
// the reply (measured on Linux). It makes 16 git calls before that reply.
// claustrum runs no git call in a folder that the trust check refused, so it
// answers at the gate (makeRefusedLeafParents). The caller applies it where the
// settings copy can run: a server with a run folder, not in the state
// fallback-git, and no "shadowDirOff":true. The other states keep the earlier
// answer (not measured). Not measured either: the same link without that switch
// or that file. It gets the same answer.
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
// methods make it one time (measured on Linux). With an ended call the reply
// comes after both calls, at 62.02 s (measured).
//
// A refused config runs the gate one more time too. On 5fd08069 a remove in a
// repository with format version 3 makes two reads of the config, where each
// other method makes one. With a syntax error it makes the read and its `git
// version` call two times (measured on Linux, 3 of 3 runs, and on macOS). A
// refusal of another file of the git directory takes the same way: it makes
// no git call (not measured). On Windows 87375cc7 and 4c111e86 make the read
// and its `git version` call one time for a syntax error
// (removeRereadsRefusedConfig, measured on Windows).
//
// After a gate that passed, the later git commands follow the off switch
// (measured on Linux). A later gate runs one time (claustrum's own, not
// measured).
//
// When the second gate got the "disagree" answer, c9125eb8 asks git 4 more
// times. The reply is the same text, after 8 calls in all, and nothing is
// deleted (measured on macOS, in a remove right after a failed create).
// claustrum makes the call about the repository two more times there (askGit,
// 2 calls each), and it does not use the answers. These parts are claustrum's
// own, not measured: Linux and Windows, an answer with no output, a later
// answer that is good, and the other bad answers, which get no more calls.
func (req *gitCall) removeGate(t gitDirTrust, off rpc.JSONTrueSwitch, repo string) (msg string, noRepo bool) {
	msg, noRepo = req.settingsGateRefusal(t, off, repo)
	if req.gitRefused || (req.configRefused && removeRereadsRefusedConfig) {
		msg, noRepo = req.settingsGateRefusal(t, off, repo)
	}
	if req.gitDisagreed {
		req.shadow.askGit(t, repo)
		req.shadow.askGit(t, repo)
	}
	if msg == "" && !noRepo {
		req.keepLiveForm(bool(off), t, repo)
	}
	return msg, noRepo
}

func gitWorktreeRemove(req *gitCall) rpc.Response {
	p, bad := bindGitParams(req, &removeViewParams{})
	if bad != nil {
		return *bad
	}
	repo := p.repoDir()
	req.use.sshKept = req.offLive
	return underRepoMutex(repo, func() rpc.Response {
		return gitWorktreeRemoveLocked(req, &p, repo)
	})
}

// removeRefusal gives the reply of a refused git.worktree_remove of req: success
// false, with msg as its error.
func removeRefusal(req *gitCall) func(msg string) rpc.Response {
	return func(msg string) rpc.Response {
		return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: false, Error: msg})
	}
}

func gitWorktreeRemoveLocked(req *gitCall, p *gitParams, repo string) rpc.Response {
	defer req.closeRemoveHold()
	refuse := removeRefusal(req)
	// On Windows a \\?\UNC\ baseRepo, a baseRepo on a share that cannot be found or
	// that denies every account, and a directory link on a share are refused
	// (measured on Windows, unlookablePathRefusal). No git call and no
	// delete run (from the code). Its place first and the test of baseRepo alone
	// are claustrum's own, not measured: the rows send worktreePath below baseRepo.
	if msg := unlookablePathRefusal(repo); msg != "" {
		return refuse(msg)
	}
	// On Windows a baseRepo with a name, or a .git objects folder, that the
	// network-path check cannot look at is refused next, after the lock-check words
	// (measured on Windows for the .git objects folder, pathLookRefusal). The walk of
	// the names is claustrum's own, not measured. No git call and no delete run (from
	// the code). Its place here is claustrum's own, not measured.
	if msg := pathLookRefusal(repo); msg != "" {
		return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
	}
	// A baseRepo through a link that leaves the local drives, or a link that cannot
	// be read, is refused with no git call and no delete (linkHopRefusal). 4c111e86
	// and 87375cc7 answer so (measured on Windows). Its place after the two tests
	// above is claustrum's own, not measured.
	if msg := linkHopRefusal(repo); msg != "" {
		return refuse(msg)
	}
	// 7d193f89 refuses a baseRepo inside a managed worktrees tree as an invalid trust
	// root (no errorCode on remove). A baseRepo that does not exist skips the check.
	// The reference was measured on macOS (`f6010b97`, measured).
	if _, err := os.Stat(repo); !errors.Is(err, fs.ErrNotExist) && repoInsideMarkedTree(repo) {
		return refuse(managedWorktreesRefusal)
	}
	// A baseRepo that fails claustrum's own trust-root test gets the same refusal
	// (baseRepoWalkFails). It comes before the worktreePath checks, the Windows
	// worktreeRoot refusal and the count check. f6010b97 showed that order
	// (measured on Windows).
	if baseRepoWalkFails(repo) {
		return refuse(managedWorktreesRefusal)
	}
	var target removeTarget
	var err error
	if p.WorktreeRoot != "" {
		// With a worktreeRoot the worktree lives outside the repo. The external checks
		// replace the in-repo containment. Measured against 7d193f89 and f6010b97.
		// An empty worktreePath is judged as a relative path here (`f6010b97`,
		// measured on Linux and macOS).
		if msg := externalWorktreeUnsupportedRefusal(p.WorktreeRoot, "remove"); msg != "" {
			return refuse(msg)
		}
		if msg := worktreeExternalSpellingRefusal(p.WorktreeRoot, p.WorktreePath, "remove"); msg != "" {
			return refuse(msg)
		}
		// f6010b97 then refuses a relative baseRepo, an absent one included, or one with
		// a ".." component, with the same texts. The text names baseRepo as sent. It
		// comes before the shape check and runs no git (measured on Linux and macOS).
		// The ".." test is by whole component (measured on Linux and macOS).
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
		// same on Linux and macOS. Measurements get the shape text first. A case with
		// no leaf and one with a symlinked <directory> get this refusal. Part of the
		// rows ran on macOS too.
		if msg := worktreeRootInRepoRefusal(p.WorktreeRoot, p.BaseRepo, "remove"); msg != "" {
			return refuse(msg)
		}
		// The work-tree check comes before the dir-symlink check. A base that the check
		// refuses gets the work-tree refusal even when the directory level is a symlink.
		// Measured side by side against f6010b97 on Linux (with a symlinked directory
		// level).
		msg, topLevel := externalWorkTreeRefusal(req, repo, p.ShadowDirOff)
		if msg != "" {
			return refuse(msg)
		}
		// A root that the daemon cannot open or search answers its own error later,
		// and not this refusal (measured on Linux and macOS).
		dirSymlinkChecked := !externalRootDenied(p.WorktreePath)
		if dirSymlinkChecked {
			if msg := worktreeExternalDirSymlinkRefusal(p.WorktreePath, "remove"); msg != "" {
				return refuse(msg)
			}
		}
		// 89cb6289 makes two more git calls here: a light `worktree list --porcelain -z`,
		// then a heavy `rev-parse --absolute-git-dir`. Each runs in baseRepo with its
		// listing. The second one does not run for a worktreeRoot below a folder of mode 000:
		// that request answers its lstat error after 7 calls (measured on Linux). claustrum
		// runs the second call after it located the worktree folder. f6010b97 made the same
		// two calls (measured). Right after the first call, the 7th git call, f6010b97 and
		// 89cb6289 refuse a root that leads into a checkout of the repository. Then they
		// refuse a root that does not exist or does not resolve. Nothing is deleted (measured
		// on Linux and macOS). Further measurements on Linux and macOS show the same. After a
		// failure of `worktree list -z`, the daemon runs `worktree list --porcelain` with its
		// listing. If that call fails too, the removal is refused and nothing is deleted
		// (`89cb6289`, measured on Linux). See worktreeListForRemove.
		listed, msg := worktreeListForRemove(req.use, repo)
		if msg != "" {
			return refuse(msg)
		}
		// The loop and filesystem-root refusals, the repository behind a link, the
		// checkout tests and a <directory> level in a listed worktree come before the
		// remove reads the root. Nothing is deleted (`5fd08069`, measured on Linux).
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
		// reads "" (`f6010b97`, measured on Linux, macOS and Windows). filepath.Rel
		// reads "" as ".", so the test is the same as on repo.
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
		// reference's answers also on macOS. On Linux and macOS a regular file
		// passes (statInsideDir).
		//
		// On Linux and macOS the look is at the first component of worktreePath below
		// baseRepo. For a repository of mode 0600, 89cb6289 answers "statat wt" for
		// <repo>/wt (measured on Linux and macOS), "statat a" for <repo>/a/wt and
		// <repo>/a/b/c/wt (measured on Linux) and "statat .claude" for a path under
		// .claude (measured on Linux). Mode 0400 answers the same (measured on Linux).
		// Windows is not measured, and keeps the look at .claude.
		first := ".claude"
		if runtime.GOOS != "windows" {
			if rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath)); err == nil {
				first, _, _ = strings.Cut(rel, string(filepath.Separator))
			}
		}
		if err := statInsideDir(repo, first); err != nil {
			return refuse("failed to remove worktree: " + err.Error())
		}
		req.pinBeforeLocate(repo)
		if createStepHook != nil {
			createStepHook("remove pinned")
		}
		target, err = locateInRepoWorktree(repo, p.WorktreePath)
		// A junction that moved around the locate leaves no way to tell which
		// repository the open folder is in, so nothing is deleted (claustrum's own,
		// not measured).
		if err == nil && (req.movedSincePin(repo) || req.openedOtherBase(target)) {
			target.close()
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		if createStepHook != nil {
			createStepHook("remove located")
		}
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
		req.behindJunction = target.behindJunction
		return removeGoneWorktree(req, p, repo, target.path)
	}
	// A leaf that is a symbolic link or not a directory is refused, and nothing is
	// deleted. In the repo no git runs first. Before, a symbolic link to a sibling
	// worktree made the removal delete that sibling, its entry and its branch
	// (measured).
	fi, err := target.parent.Lstat(target.leaf)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return removeGoneWorktree(req, p, repo, target.path)
	case err != nil:
		return refuse(worktreeRemoveText(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return refuse(worktreeRemoveText(refuseWorktree("`%s` is a symbolic link, not a worktree directory", target.path)))
	case !fi.IsDir():
		return refuse(worktreeRemoveText(refuseWorktree("%s", leafNotDirText(runtime.GOOS, target.path, fi.Mode()))))
	}
	leafDir, err := target.parent.OpenRoot(target.leaf)
	if err != nil {
		return refuse(worktreeRemoveText(err))
	}
	if err := checkLeafIdentity(target.parent, target.leaf, leafDir, target.path); err != nil {
		_ = leafDir.Close()
		return refuse(worktreeRemoveText(err))
	}
	if msg := worktreeLeafHomeRefusal(leafDir, p.WorktreePath); msg != "" {
		_ = leafDir.Close()
		return refuse(msg)
	}
	// The `.git` file is read before the git calls below. In the repo no git has run
	// yet. With worktreeRoot the external checks above already ran git. A leaf swapped
	// during the git calls below is not detected: the removal then deletes the new directory and drops the
	// entry that the old `.git` file named (measured).
	gitDir, dotGitErr := worktreeGitFileTarget(leafDir, target.path)
	_ = leafDir.Close()
	// namesGitDir is true when the `.git` file names a git dir.
	namesGitDir := dotGitErr == nil
	// dropEntry is false for a baseRepo that does not exist as sent. 89cb6289 then
	// deletes the worktree folder and keeps its entry, and its one git call is the read
	// of the user excludes. Measurements send <T>/missing/.. on Linux and macOS, and
	// <T>/missing/.., <F>/missing/../T and <T>/dl/.. on Linux. The lock check below
	// still reads <T>/.git/worktrees for that baseRepo, and a locked worktree is
	// refused. 89cb6289 deletes it (measured on Linux and macOS). That refusal is
	// divergence D22.
	dropEntry := true
	// relisted is true when the first plain listing failed and a second one ran
	// (removeListingCheck).
	relisted := false
	_, statErr := os.Stat(repo)
	repoMissing := errors.Is(statErr, fs.ErrNotExist)
	if p.WorktreeRoot == "" {
		// If baseRepo holds no repository, or its git directory fails the trust check
		// (gitdirtrust.go), the registrations cannot be examined. Nothing is deleted.
		// Measured side by side against f6010b97 on Linux, macOS and Windows (measured on
		// macOS). A failed listing follows removeListingCheck. For a baseRepo that holds no
		// repository, 89cb6289 runs the listing and the rev-parse twice before that answer
		// (measured on Linux, macOS and Windows).
		t := requestGitDirTrust(repo, false)
		verdict := t.verdict
		if verdict == gitDirRefused {
			// The one git call of 89cb6289 is the read of the user excludes (measured
			// on Linux).
			globalExcludesPath()
			// A stray commondir in a git directory that fails the git-directory test
			// answers its text after a colon (`b3081b51`, measured on Linux and macOS
			// with a bad HEAD and a dangling commondir link). `89cb6289` answered the
			// lock-check text there on Linux. Windows is claustrum's own, not
			// measured. A stray commondir whose content is not ".", "./" or "./." answers
			// so too, with a bad HEAD and a good one (`b3081b51`, measured on Linux,
			// macOS and Windows). On Windows a stray commondir that is not a small plain
			// file answers so too (removeNamesStrayFile). Every other refusal keeps the
			// lock-check text (not measured on b3081b51).
			if strings.HasSuffix(t.refusal, strayShapeTail) || strings.HasSuffix(t.refusal, strayContentTail) ||
				(removeNamesStrayFile && strings.HasSuffix(t.refusal, strayFileTail)) {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + t.refusal)
			}
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// A refused daemon GIT_CONFIG_COUNT gets the same answer. hostileConfigRefusal
		// below does not refuse a baseRepo that does not stat, such as <T>/missing/..,
		// so the count is checked here. f6010b97 answers so (measured on Linux and
		// macOS).
		if _, bad := daemonCountRefusal(); bad {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// On Windows GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment and
		// the alternates file are checked before any git call and any delete
		// (alternateEnvRefusal, alternatesRefusal). The worktree, its entry and its
		// branch stay.
		if verdict == gitDirTrusted {
			if msg := alternateEnvRefusal(bool(p.ShadowDirOff)); msg != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
			}
			if msg := alternatesRefusal(t.pinCommonDir, false); msg != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
			}
		}
		// The daemon's own read of the repository's config comes before the listing
		// (repoConfigGate). The worktree stays (measured on Linux). The path is
		// worktreePath as sent, and it is not cut (measured). A worktree folder
		// that is gone gets the same refusal (removeGoneWorktree). With
		// worktreeRoot the read is in externalWorkTreeRefusal, with its own text.
		if msg, noRepo := req.removeGate(t, p.ShadowDirOff, repo); msg != "" || req.goneCopyText != "" {
			// A failed copy for a missing daemon GIT_DIR deletes the worktree folder
			// with its files and answers success. With no `.git` entry in baseRepo
			// `b3081b51` does so on Linux and macOS (measured). With an unlocked
			// `.git` folder it does so too (`b3081b51`, measured on Linux). The guards
			// above ran first. A failed delete is not measured. Windows sets no
			// goneCopyText.
			if req.goneCopyText != "" && noRepo && dotGitAbsentOrFolder(repo) {
				// A lock in the `.git` folder of baseRepo that names the worktree keeps
				// the D22 refusal (lockedInBaseRepo). The registrations of the missing
				// GIT_DIR are not read, so the commonDir is empty.
				named := ""
				if namesGitDir {
					named = gitDir
				}
				if msg := baseRepoLockRefusal(repo, "", named, target.path, p.WorktreePath,
					newWorktreePathSet(target.path, p.WorktreePath), lockRefusalText); msg != "" {
					return refuse(msg)
				}
				if err := deleteWorktreeDir(target.parent, target.leaf, target.path); err != nil {
					return refuse(worktreeRemoveText(err))
				}
				return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: !skippedBranchName(p.BranchName)})
			}
			// A `.git` in baseRepo that is a link, a file or an entry that the lstat
			// cannot read keeps the refusal of the failed copy (claustrum's own, not
			// measured).
			return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg + req.goneCopyText)
		} else if noRepo {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// The listing carries the heavy profile of the rev-parse that follows it, as on
		// f6010b97 (measured on Linux, macOS and Windows). When the check fails, f6010b97
		// runs it once more before it answers: a second listing, and a second rev-parse
		// when that listing passes. That repeat was measured on Linux and macOS (a
		// corrupt config, and a git directory that git cannot use). The answer is the
		// same whatever the second check finds. A first listing that git ran and that
		// failed is the exception: removeListingCheck runs it again, as 5fd08069 does.
		//
		// A listing that answers "no repository" (hostileConfigRefusal) gets the same
		// answer here. With no git on PATH, 89cb6289 answers so too (measured on Linux).
		// With the folder present, a first listing that says "not a git repository" is
		// listed again (measured, see removeListingCheck).
		if repoMissing {
			// With no git on PATH the answer is the lock-check refusal, and nothing is
			// deleted. 89cb6289 answers so for <T>/missing/.. and for <T>/dl/.. with a
			// dangling link `dl` (measured on Linux and macOS). No git call starts.
			if gitLookupError() != nil {
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			dropEntry = false
		} else {
			var c configVerdict
			var final bool
			c, relisted, final = removeListingCheck(req.use, repo, verdict == gitDirNoRepo)
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
			// A failed listing that is the answer gives the text of the failed listing
			// after the words of the lock check, as on 5fd08069 (measured, see
			// removeListingCheck). One that says "no repository" gets the lock-check
			// refusal with no third listing (claustrum's own, not measured).
			if final && c.refusal != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + c.refusal)
			}
			if final && c.refused() {
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			// Any other failed check runs once more, as on f6010b97 (measured on Linux
			// and macOS) and of 89cb6289. 5fd08069 repeats too, except in two other states
			// (measured on Linux).
			if c.refused() || checkErr != nil {
				repeatRepositoryCheck(req.use, repo)
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			// An answer that names another git directory gets one more call
			// (gitDirAgain). A second such answer fails the check. c9125eb8 then
			// asks 2 more times and refuses with the "disagree" text after 4 calls.
			// Nothing is deleted (measured on Linux and Windows). claustrum asks the
			// check once more. Its answer is not used, and the text is that of the
			// second call (claustrum's own, not measured). A failed second call answers as a failed check (not
			// measured).
			var disagree string
			answered, disagree, checkErr = gitDirAgain(req.use, repo, t, answered)
			if checkErr != nil {
				repeatRepositoryCheck(req.use, repo)
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			if disagree != "" {
				if again, err := repositoryGitDir(req.use, repo, nil); err == nil {
					_, _, _ = gitDirAgain(req.use, repo, t, again)
				}
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + disagree)
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
	junctionMoved := false
	if p.WorktreeRoot == "" {
		commonDir = registrationGitDir(repo, answered)
		probe = newRegistrationProbe(req.use, answered, inRepoWorkTree(repo))
		// A junction in baseRepo that now leads to another repository does not move
		// the registration: 4c111e86 and 87375cc7 delete the entry of the first
		// repository and keep that of the second (measured on Windows).
		commonDir, junctionMoved = req.pinnedCommonDir(repo, commonDir)
		// A move with no git directory of the first repository at its pin has no
		// place to read the locks, so nothing is deleted (claustrum's own, not measured).
		if junctionMoved && !req.pinStands() {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// After a second listing that passed, 5fd08069 makes the pair of the probe
		// before it answers (measured on Linux and macOS). The order of the pair
		// and the deletes is not measured. claustrum does not use the answer.
		if relisted {
			probe.run()
		}
	}
	// A registrations folder that is a symbolic link refuses the remove, and
	// nothing is deleted (registrationsLinked). The test comes before the lock
	// tests: with a locked entry behind the link, and with worktreeRoot,
	// 5fd08069 answers this refusal too (measured on Linux). On Windows a junction
	// there is refused too (removeRegistrationsLinked).
	if removeRegistrationsLinked(filepath.Join(commonDir, worktreesSubdir)) {
		return refuse(lockCheckRefusal(p.WorktreePath))
	}
	sp := newWorktreePathSet(target.path, p.WorktreePath)
	// A worktree that is locked in the `.git` folder of baseRepo is refused, also when
	// the entries are read from another git directory (D22, lockedInBaseRepo). That
	// holds with worktreeRoot too (measured on Linux).
	named := ""
	if namesGitDir {
		named = gitDir
	}
	if msg := baseRepoLockRefusal(repo, commonDir, named, target.path, p.WorktreePath, sp,
		lockRefusalText); msg != "" {
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
	// A `.git` file that names a git dir which is not a verified entry: 89cb6289 runs the
	// listing and the rev-parse once more (repeatRepositoryCheck). A `.git` file that names no
	// git dir, or no `.git` file, gets no such calls (measured). Not measured: a baseRepo that
	// does not exist. claustrum makes no call there.
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
		// is refused and left in place. A locked registration by path comes first
		// (measured).
		//
		// With a refused daemon GIT_CONFIG_COUNT, externalWorkTreeRefusal has already
		// refused a baseRepo that exists. For a missing absolute baseRepo without a
		// ".." component, the answer is a lock-check refusal that names the repository
		// (docs/protocol/git-rules-calls.md → "Where each method answers the refusal").
		if _, bad := daemonCountRefusal(); bad {
			return refuse(unreadableRepoLockCheckRefusal(p.WorktreePath, repo))
		}
		// With no git on PATH the answer is the same refusal. Only a baseRepo that does
		// not exist comes this far then: externalWorkTreeRefusal refuses every other
		// one. 89cb6289 and f6010b97 answer so on Linux for a leaf that holds a file
		// (measured) and for an empty leaf. With git the empty leaf is removed below
		// (measured). A leaf whose `.git` file names an entry of the missing repository
		// gets this refusal too (measured). With git that leaf is deleted below, as on
		// both references (measured).
		if gitLookupError() != nil {
			return refuse(unreadableRepoLockCheckRefusal(p.WorktreePath, repo))
		}
		probe.pairIfWorktreesDir(commonDir)
		lock, readable, _ := worktreeLockedByPath(commonDir, sp)
		if !readable {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		if lock != notLocked {
			return refuse(lockRefusalText(p.WorktreePath, lock))
		}
		lockChecked = true
		if target.parent.Remove(target.leaf) == nil {
			return removeGoneWorktree(req, p, repo, target.path)
		}
		if !entryAlreadyGone(gitDir, commonDir) {
			var r *worktreeRefusal
			if errors.As(dotGitErr, &r) {
				// For a missing baseRepo the folder with no `.git` entry is in backticks
				// (`b3081b51`, measured on Linux and macOS). For a baseRepo that exists the path stays
				// plain (claustrum's own, not measured).
				reason := r.text
				if repoMissing && reason == noGitFileText(target.path) {
					reason = noGitFileText(tickPath(target.path))
				}
				return refuse(fmt.Sprintf("refusing to remove worktree: %s is not a worktree of %s (%s), "+
					"so it is left in place; remove it by hand if it is a leftover", p.WorktreePath, repo, reason))
			}
			// One input is measured, with git on PATH (measured on Linux). It is a missing
			// baseRepo and a `.git` file that names an entry of another missing
			// repository. 89cb6289 sends this frame there. Its detail is the hooks refusal
			// with the chdir error of baseRepo, and its one git call is the read of the
			// user excludes. For a baseRepo that exists, the detail is claustrum's text
			// from before, the error of the open (measured).
			detail := dotGitErr.Error()
			if repoMissing {
				detail = hooksRefusalPrefix + (&fs.PathError{Op: "chdir", Path: repo, Err: errors.Unwrap(statErr)}).Error()
			}
			return refuse(fmt.Sprintf("failed to remove worktree: could not verify that %s is a "+
				"worktree of %s (%s); retry", filepath.Clean(p.WorktreePath), repo, detail))
		}
	}
	// The lock check. A verified entry is locked when it carries a `locked` marker of
	// any kind, a dangling symbolic link included (measured). Without a verified
	// entry every registration that names the worktree is checked (measured).
	if entry != "" {
		if lock := verifiedEntryLocked(commonDir, entry); lock != notLocked {
			return refuse(lockRefusalText(p.WorktreePath, lock))
		}
	} else if !lockChecked {
		probe.pairIfWorktreesDir(commonDir)
		lock, readable, n := worktreeLockedByPath(commonDir, sp)
		if !readable {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		if lock != notLocked {
			return refuse(lockRefusalText(p.WorktreePath, lock))
		}
		matches = n
	}
	if err := deleteWorktreeDir(target.parent, target.leaf, target.path); err != nil {
		return refuse(worktreeRemoveText(err))
	}
	// The entry goes after the tree. A verified entry that cannot be deleted is
	// reported (`f6010b97`, measured). The branch step still runs (`89cb6289`,
	// measured). Without a verified entry, the one entry whose record names the
	// worktree is deleted. A failure of that delete is not reported (`f6010b97`,
	// measured). If no entry names the folder, the call log of 89cb6289 shows the
	// pair of registrationProbe once more (measured, also on Windows). With one
	// entry or two it does not (measured). claustrum runs that pair after the
	// delete of the tree.
	//
	// The entry, the branch step and the reply wait for the `worktree add` of a
	// create in the same git directory, and the tree does not (lockWorktreeAddStep).
	defer lockWorktreeAddStep(repo)()
	if createStepHook != nil {
		createStepHook("remove entry step")
	}
	// A `.git` that is no longer the held folder keeps every registration, and no
	// branch step runs. 87375cc7 answers this text with "branchKept":true when a
	// junction to a second repository took the place of `.git` during the tree delete
	// (measured on Windows).
	//
	// With "shadowDirOff":true 87375cc7 answers the same text with no "branchKept". Its
	// branch step runs by the path, and the ref of the same name goes in the second
	// repository, also for a locked worktree there. Its entry, its leaf and its lock
	// stay (measured on Windows).
	held := req.holdsGitDir(commonDir)
	pending := ""
	offSwapped := false
	if dropEntry && held && req.holdLeftItsPath() {
		text := fmt.Sprintf("removed the worktree but could not drop its registration (%s)", realDirText(req.removeHoldPath))
		if !p.ShadowDirOff {
			// 87375cc7 asks git about the repository once more here (measured on Windows).
			// The answer changes nothing.
			if t := requestGitDirTrust(repo, false); req.shadow != nil && req.shadow.asksGit(t, false) {
				req.shadow.askGit(t, repo)
			}
			return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: false, BranchKept: !skippedBranchName(p.BranchName), Error: text})
		}
		pending, offSwapped = text, true
	}
	if createStepHook != nil {
		createStepHook("remove entry drop")
	}
	// With no held git directory and a git directory spelled below baseRepo, a move of
	// baseRepo since the pin keeps the entry (claustrum's own, not measured). Another git
	// directory keeps the delete of before (gitDirBelowBase).
	var moved func() bool
	if gitDirBelowBase(repo, commonDir) {
		moved = func() bool { return req.movedSincePin(repo) }
	}
	switch {
	case !dropEntry || offSwapped:
	case entry != "":
		drop := func() error { return dropWorktreeEntry(commonDir, entry, moved) }
		if held {
			drop = func() error { return dropHeldEntry(req.removeHold, entry) }
		}
		if err := drop(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			pending = fmt.Sprintf("removed the worktree but could not drop its registration (%v)", err)
		}
	default:
		if matches == 0 {
			probe.pairIfWorktreesDir(commonDir)
		}
		if held {
			dropHeldEntryByPath(req.removeHold, commonDir, sp)
		} else {
			dropWorktreeEntryByPath(commonDir, sp, moved)
		}
	}
	// The branch step then runs in the second repository, and it deletes the ref of
	// the same name there (4c111e86 and 87375cc7, measured on Windows, by the
	// maintainer's decision of 2026-11-23). A second repository that the gate refuses
	// gets no branch step (claustrum's own, not measured).
	if createStepHook != nil {
		createStepHook("remove branch step")
	}
	// With a daemon GIT_COMMON_DIR and a link put at `.git`, 87375cc7 refuses the branch
	// step and keeps the ref of the second repository (measured on Windows). With
	// "shadowDirOff":true that state is not measured, and it gets the same refusal.
	if req.dotGitBecameLink(repo) {
		text := fmt.Sprintf("removed the worktree, but left its branch %q in place: %s", p.BranchName,
			repoConfigPrefix+gitDirLinkText(filepath.Join(repo, ".git")))
		if pending != "" {
			text = pending
		}
		return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: false, Error: text, BranchKept: !skippedBranchName(p.BranchName)})
	}
	// A `.git` that left the held folder after the entry went gets no branch step, so
	// no ref of another repository goes (claustrum's own, not measured).
	if !offSwapped && req.removeHold != nil && req.holdLeftItsPath() {
		return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: pending == "", Error: pending, BranchKept: !skippedBranchName(p.BranchName)})
	}
	if junctionMoved || req.movedSincePin(repo) {
		// No branch step ran, so the reply has the member of a kept branch, as a
		// remove that keeps the branch with no step has (keptAll, claustrum's own for
		// this state, not measured).
		if !req.movedBranchGate(p, repo) {
			return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: pending == "", Error: pending, BranchKept: !skippedBranchName(p.BranchName)})
		}
	}
	// The branch step adds no text to the reply. A kept branch adds the member only.
	kept := runBranchStep(req.use, repo, p.BranchName).kept()
	return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: pending == "", Error: pending, BranchKept: kept})
}

// pinBeforeLocate records, on Windows, the folder of baseRepo and its git directory
// before the remove opens the folder of the worktree. A later step compares the folder
// with it (movedSincePin), and on a move the locks and the entry are read in this git
// directory. It runs for every request: a plain baseRepo can become a link too.
func (req *gitCall) pinBeforeLocate(repo string) {
	if !walkStartsAsSpelled {
		return
	}
	// One resolve serves every read of the pin, so a junction in the path of baseRepo that moves after the resolve does not change those reads.
	// A move before the resolve, or a swap of .git during the pin, can still give the pin another repository (pinIsOfBase).
	at := repo
	if final, ok := linkFreePath(repo); ok {
		at = final
	}
	// The handle of the folder stays open through the hold. With no handle, for example
	// at a dangling junction, the remove keeps no pin and no hold, so its later reads go
	// by the path and a move with no pin refuses (claustrum's own, not measured).
	base := openPinBase(at)
	if base != nil {
		defer func() { _ = base.Close() }()
		req.removeBase = rootFolderIdentity(base)
	}
	req.removePinned = true
	req.removeBaseDir = pathcanon.FinalDir(at)
	req.removeDotGitLink = dotGitLinkNow(at)
	t := requestGitDirTrust(at, false)
	if base == nil || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return
	}
	req.removePin, req.removePinInfo = t.pinCommonDir, baseFolderIdentity(t.pinCommonDir)
	req.holdRemoveGitDir(t.holdFolderFor(t.pinCommonDir))
	if !req.pinIsOfBase(base) {
		req.closeRemoveHold()
		req.removePin, req.removePinInfo = "", nil
	}
}

// openPinBase opens the folder of baseRepo for the pin, or answers nil. The first open
// lets another process rename the folder (openHeldGitDir). It follows no link at the
// last name and needs its parent folder to open, so a plain open is the second try.
// A test replaces it.
var openPinBase = func(at string) *os.Root {
	if r, err := openHeldGitDir(at); err == nil {
		return r
	}
	r, err := openRootNoWait(at)
	if err != nil {
		return nil
	}
	return r
}

// pinIsOfBase reports whether the pin and the hold are the git directory of the open
// folder base, by the path that the handle has now. It makes no git call. A second swap
// during that read can still keep a pin of another repository (from the code, not measured).
func (req *gitCall) pinIsOfBase(base *os.Root) bool {
	f, err := base.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	final, err := pathcanon.PathOfOpenFolder(f)
	if err != nil {
		return false
	}
	t := requestGitDirTrust(final, false)
	if t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return false
	}
	now := baseFolderIdentity(t.pinCommonDir)
	if now == nil || req.removePinInfo == nil || !os.SameFile(now, req.removePinInfo) {
		return false
	}
	if req.removeHold == nil {
		return true
	}
	held := gitDirIdentity(t.holdFolderFor(t.pinCommonDir))
	return held != nil && os.SameFile(held, req.removeHoldInfo)
}

// holdRemoveGitDir opens the real git directory dir once, with no link at its last
// name. A git directory that is a link at the pin gets no hold, and its entry goes by
// the path as before. A `.git` put there later cannot move this handle.
func (req *gitCall) holdRemoveGitDir(dir string) {
	before := gitDirIdentity(dir)
	if before == nil {
		return
	}
	root, err := openHeldGitDir(dir)
	if err != nil {
		return
	}
	info := rootFolderIdentity(root)
	if info == nil || !os.SameFile(info, before) {
		_ = root.Close()
		return
	}
	req.removeHold, req.removeHoldPath, req.removeHoldInfo = root, dir, info
}

// closeRemoveHold closes the hold of pinBeforeLocate.
func (req *gitCall) closeRemoveHold() {
	if req.removeHold != nil {
		_ = req.removeHold.Close()
		req.removeHold = nil
	}
}

// holdsGitDir reports whether commonDir is the git directory of the hold.
func (req *gitCall) holdsGitDir(commonDir string) bool {
	if req.removeHold == nil {
		return false
	}
	c := filepath.Clean(commonDir)
	return sameGitDirPath(c, filepath.Clean(req.removePin)) || sameGitDirPath(c, filepath.Clean(req.removeHoldPath))
}

// dotGitLinkNow reports whether `<repo>/.git` is a junction or a symbolic link now.
func dotGitLinkNow(repo string) bool {
	fi, err := os.Lstat(filepath.Join(repo, ".git"))
	return err == nil && folderIsLink(fi.Mode())
}

// dotGitBecameLink reports whether a request with a daemon GIT_COMMON_DIR finds a link
// at `<repo>/.git` where the pin found none. The git directory of the hold is then
// another folder, so holdLeftItsPath does not see the change.
func (req *gitCall) dotGitBecameLink(repo string) bool {
	return req.removePinned && !req.removeDotGitLink && daemonCommonDirSet() && dotGitLinkNow(repo)
}

// holdLeftItsPath reports whether the path of the held git directory no longer names
// the held folder, as a real folder.
func (req *gitCall) holdLeftItsPath() bool {
	now := gitDirIdentity(req.removeHoldPath)
	return now == nil || !os.SameFile(now, req.removeHoldInfo)
}

// pinStands reports whether the pinned git directory is still at its path. The pin of
// a plain baseRepo is spelled through the folder that moved, so after a move it can
// name the second repository. Then no lock can be read in the first one.
func (req *gitCall) pinStands() bool {
	if req.removePin == "" || req.removePinInfo == nil {
		return false
	}
	now := baseFolderIdentity(req.removePin)
	return now != nil && os.SameFile(now, req.removePinInfo)
}

// sameRemoveBase reports whether now is the folder of baseRepo at the pin. Two folders
// that did not open count as one.
func (req *gitCall) sameRemoveBase(now os.FileInfo) bool {
	if now == nil || req.removeBase == nil {
		return now == nil && req.removeBase == nil
	}
	return os.SameFile(now, req.removeBase)
}

// movedSincePin reports whether baseRepo now leads to another folder than at the pin.
// It compares the identity of the open folder. It is false with no pin.
func (req *gitCall) movedSincePin(repo string) bool {
	return req.removePinned && !req.sameRemoveBase(baseFolderIdentity(repo))
}

// openedOtherBase reports whether the locate opened another folder than the pinned
// one, also when the path leads back to the pinned folder by now.
func (req *gitCall) openedOtherBase(target removeTarget) bool {
	return req.removePinned && target.baseInfo != nil && !req.sameRemoveBase(target.baseInfo)
}

// movedBranchGate reports whether the branch step can run in the repository that a
// moved baseRepo now leads to: a main git directory whose gate passes.
func (req *gitCall) movedBranchGate(p *gitParams, repo string) bool {
	t := requestGitDirTrust(repo, false)
	if t.verdict != gitDirTrusted || t.pinCommonDir == "" || t.worktreeEntry != "" {
		return false
	}
	msg, noRepo := req.removeGate(t, p.ShadowDirOff, repo)
	return msg == "" && !noRepo
}

// removeGoneAfterMove answers a gone leaf whose baseRepo moved to a second repository
// after the locate. 87375cc7 answers success there and deletes only the ref of the same
// name in the second repository. The entry of each repository, the leaf and the
// sentinel of the second stay (measured on Windows). A state that no row covers keeps
// everything: no pinned git directory, a locked entry of the leaf in either
// repository, a second repository with no main git directory, or a gate that refuses
// (claustrum's own, not measured).
func (req *gitCall) removeGoneAfterMove(p *gitParams, repo, path string, checked bool) rpc.Response {
	refuse := removeRefusal(req)
	if !checked || !req.pinStands() {
		return refuse(lockCheckRefusal(p.WorktreePath))
	}
	first := newWorktreePathSet(p.WorktreePath)
	if rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath)); err == nil && filepath.IsLocal(rel) {
		first = newWorktreePathSet(p.WorktreePath, filepath.Join(req.removeBaseDir, rel))
	}
	if lock, readable, _ := worktreeLockedByPath(req.removePin, first); !readable {
		return refuse(lockCheckRefusal(p.WorktreePath))
	} else if lock != notLocked {
		return refuse(goneLockedWorktreeRefusal(p.WorktreePath, lock))
	}
	t := requestGitDirTrust(repo, false)
	if t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return refuse(lockCheckRefusal(p.WorktreePath))
	}
	if lock, readable, _ := worktreeLockedByPath(t.pinCommonDir, goneWorktreePathSet(p, repo, path)); !readable {
		return refuse(lockCheckRefusal(p.WorktreePath))
	} else if lock != notLocked {
		return refuse(goneLockedWorktreeRefusal(p.WorktreePath, lock))
	}
	if !req.movedBranchGate(p, repo) {
		return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: !skippedBranchName(p.BranchName)})
	}
	kept := runBranchStep(req.use, repo, p.BranchName).kept()
	return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: kept})
}

// pinnedCommonDir is the git directory of the registrations of a remove after the pin.
// It is the pin, unless a daemon GIT_COMMON_DIR chose another one and baseRepo did not
// move. moved reports a move.
func (req *gitCall) pinnedCommonDir(repo, commonDir string) (dir string, moved bool) {
	moved = req.movedSincePin(repo)
	if req.removePin != "" && (moved || !daemonCommonDirSet()) {
		return req.removePin, moved
	}
	return commonDir, moved
}

// removeGoneWorktree answers a remove whose worktree directory is not there. The
// registration is checked by path. A locked one is refused. Else the one entry whose
// record names the worktree is deleted. Then the branch step runs. path is the
// spelling of the worktree, or "" when a parent directory is missing too.
//
// In the repo, a config that cannot be listed, or a git directory that fails the trust
// check, is refused with its reason (measured). A baseRepo that holds no repository
// skips the registration (measured). Measured on the reference, f6010b97, on macOS.
func removeGoneWorktree(req *gitCall, p *gitParams, repo, path string) rpc.Response {
	refuse := removeRefusal(req)
	lockCheck := func(reason string) rpc.Response {
		return refuse(goneLockCheckText(p.WorktreePath, reason))
	}
	// The check of the daemon's own GIT_CONFIG_COUNT refuses in both modes. Without
	// worktreeRoot it comes after a trust refusal and before the "no repository"
	// answer (docs/protocol/git-rules-calls.md → "Where each method answers the refusal").
	checkRegistration := true
	// answered is the answer of `rev-parse --absolute-git-dir` in baseRepo, or "".
	answered := ""
	// The success of a remove that keeps the registration and the branch, with no
	// step after it.
	keptAll := func() rpc.Response {
		return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: !skippedBranchName(p.BranchName)})
	}
	if p.WorktreeRoot == "" {
		// On Linux and macOS a baseRepo that is a regular file gets success. If `git
		// version` fails, the remove refuses with its text. b3081b51 does so, also
		// with a hook that fails each listing (measured on Linux and macOS). Its place before the trust check and the count check is
		// claustrum's own, not measured. Windows is not measured and keeps its path.
		if fi, err := os.Stat(repo); err == nil && fi.Mode().IsRegular() && runtime.GOOS != "windows" {
			if v := gitVersionRun(req.use, true); v.err != nil {
				return lockCheck(gitCannotRunPrefix + v.detail())
			}
			return keptAll()
		}
		t := requestGitDirTrust(repo, false)
		if t.verdict == gitDirRefused {
			return lockCheck(t.refusal)
		}
		// A daemon GIT_DIR that names a file: no git call, and the branch is kept
		// (gitDirTrust.noGit). An empty branchName is not measured there. A `.git`
		// file to a regular file with a refused GIT_CONFIG_* entry of the daemon gets
		// the same answer (gitDirTrust.fileGitDir).
		msg, bad := daemonCountRefusal()
		if t.verdict == gitDirNoRepo && ((t.noGit && !bad) || (t.fileGitDir && bad)) {
			return keptAll()
		}
		if bad {
			// b3081b51 puts the count text after a colon, with no "retry", for a plain
			// folder and a missing baseRepo. In a repository it runs the start test of
			// the settings copy first, and its listing of the config fails under the
			// daemon's pair. The reply then has the text of that failed test (measured
			// on Linux, macOS and Windows). A test that passes or does not run keeps the
			// count text (claustrum's own, not measured).
			if t.verdict == gitDirTrusted && t.pinCommonDir != "" {
				if failed := req.shadow.gate(bool(p.ShadowDirOff)); failed != "" {
					msg = failed
				}
			}
			return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
		}
		// On Windows GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment and
		// the alternates file are checked here too, and the registration and the
		// branch stay (measured on Windows, alternateEnvRefusal, alternatesRefusal).
		// The one git call before the env refusal is the excludes read of a new
		// daemon (4c111e86, measured on Windows).
		if t.verdict == gitDirTrusted {
			if msg := alternateEnvRefusal(bool(p.ShadowDirOff)); msg != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
			}
			if msg := alternatesRefusal(t.pinCommonDir, false); msg != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
			}
		}
		// A baseRepo that does not exist runs no start test and no `git version`
		// (4c111e86, measured on Windows for a local baseRepo). Below a repository on a
		// share the start test still runs, in removeGate below. So it does with a
		// daemon GIT_DIR, as before (claustrum's own, not measured). The start test waits
		// for the next request that needs it. Nothing is deleted. For a share host that
		// does not resolve, the calls of 4c111e86 are measured: none, and the start test
		// later. Linux and macOS share this path: claustrum's own, not measured there. So
		// is a baseRepo below a local repository. Only the branch step runs, and git
		// cannot start in the missing folder. The trust verdict of a missing baseRepo
		// carries no pin of a daemon GIT_DIR, so the test reads the environment.
		_, statErr := os.Stat(repo)
		missing := errors.Is(statErr, fs.ErrNotExist) && os.Getenv("GIT_DIR") == ""
		missingDone := func() rpc.Response {
			defer lockWorktreeAddStep(repo)()
			// A folder that took the missing path since the pin gets no branch step.
			if createStepHook != nil {
				createStepHook("remove branch step")
			}
			if req.movedSincePin(repo) {
				return keptAll()
			}
			kept := runBranchStep(req.use, repo, p.BranchName).kept()
			return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: kept})
		}
		if missing && missingShareRepo(repo) == "" {
			return missingDone()
		}
		// The daemon's own read of the repository's config refuses here too
		// (repoConfigGate), with the text of a worktree folder that exists. No
		// registration and no branch is deleted. 5fd08069 answers so on Windows
		// (measured), where 89cb6289 answers success. It deletes the branch with the
		// files of 20 MiB and with the absolute include (measured), and keeps it
		// with format version `2`. Linux and macOS measured the same refusal. Their
		// states are format version `2`, a config of 20 MiB and an info/exclude of
		// 20 MiB. With worktreeRoot the read came before this function
		// (externalWorkTreeRefusal).
		if msg, noRepo := req.removeGate(t, p.ShadowDirOff, repo); msg != "" {
			return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + msg)
		} else if noRepo && t.goneEnvGitDir {
			return keptAll()
		} else if noRepo && goneNotARepoKeepsAll && req.gitNotARepo && measuredNotARepoLength(repo) {
			// On Windows git finds no repository in a folder that the trust check
			// accepted: its call about the repository exited 128 with a text of
			// notARepoStderr, and the second run exited 0. A remove of a gone
			// worktree then gets success and deletes nothing. It runs no branch step
			// (from the code). b3081b51 answers so where `<baseRepo>\.git\config`
			// has 262, 267 or 269 characters (measured on Windows). The other
			// lengths from 262 to 269 are claustrum's own. 120, 250 and 259 gave
			// another reply, and so did 280 (measured). A worktree folder that
			// exists and every other length keep the refusal (claustrum's own,
			// not measured).
			return keptAll()
		} else if noRepo && shareGitDir(repo) != "" {
			// A `.git` folder on a network share that git does not use gets success,
			// and nothing is deleted (shareGitDir). This noRepo is the answer of
			// askGit, since a daemon GIT_DIR to a missing path answered above. No
			// lock is checked. A locked registration in the share, the local drive
			// or the 8.3 spelling gets success, and the lock, the registration and
			// the branch stay (4c111e86, measured on Windows).
			return keptAll()
		} else if noRepo {
			return refuse(lockCheckRefusal(p.WorktreePath))
		}
		// On Windows a missing baseRepo below a repository on a share asks git
		// about that repository once, and again only after a bad answer, as
		// removeGate does (askForMissingOnShare). After two bad answers it refuses
		// or answers success with the branch kept, and deletes nothing. Otherwise
		// the steps below run, as for a missing baseRepo off a share.
		if a, asked := req.askForMissingOnShare(repo, bool(p.ShadowDirOff)); asked && a.bad() {
			if a, _ = req.askForMissingOnShare(repo, bool(p.ShadowDirOff)); a.text != "" {
				return refuse("failed to remove worktree: could not check whether " + p.WorktreePath + " is locked: " + a.text)
			} else if a.noRepo {
				return keptAll()
			}
		}
		// Otherwise, after a good answer or with no call, the remove takes the steps of
		// a missing baseRepo off a share, with no `git version` (claustrum's own, not
		// measured).
		if missing {
			return missingDone()
		}
		switch t.verdict {
		case gitDirNoRepo:
			// 89cb6289 runs the listing and the rev-parse here too, and the rev-parse
			// fails (measured on Linux, macOS and Windows). claustrum does not use the
			// answer of the rev-parse. A listing that fails refuses, and nothing is
			// deleted (measured on Linux, macOS and Windows). Without the entry of
			// noRepoPinned the answer of the listing is not read.
			c := hostileConfigRefusal(req.use, repo, true)
			// git 2.56 fails the listing of a `.git` file to a gone target that is no
			// entry with its gitfile text. b3081b51 then answers success and keeps the
			// branch, with and without "shadowDirOff":true (measured on Linux), and so
			// does 4c111e86 (measured on Linux, macOS and Windows with git 2.56.1).
			// Without gitFileNowhereAsOlderGit the answer is the refusal of 5fd08069,
			// with the listing refusal under the switch.
			if c.gitFileText != "" && goneNonEntryGitFile(repo) {
				if gitFileNowhereAsOlderGit {
					return keptAll()
				}
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
			// else (measured on Linux, macOS and Windows).
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
	if req.movedSincePin(repo) {
		return req.removeGoneAfterMove(p, repo, path, checkRegistration)
	}
	if checkRegistration {
		// Without worktreeRoot the registrations are those of registrationGitDir. If
		// <git dir>/worktrees exists, the call log of 89cb6289 shows the pair of
		// registrationProbe once (measured on Linux, macOS and Windows). If no entry
		// names the worktree, it shows the pair twice (measured on Windows, with an
		// empty worktrees directory). claustrum runs the first pair before it looks
		// at the entries and the second before the delete of the entry. With
		// worktreeRoot nothing changed.
		commonDir := verifyGitDir(repo)
		var probe registrationProbe
		if p.WorktreeRoot == "" {
			commonDir = registrationGitDir(repo, answered)
			var moved bool
			commonDir, moved = req.pinnedCommonDir(repo, commonDir)
			if moved && !req.pinStands() {
				return refuse(lockCheckRefusal(p.WorktreePath))
			}
			probe = newRegistrationProbe(req.use, answered, inRepoWorkTree(repo))
			probe.pairIfWorktreesDir(commonDir)
		}
		sp := goneWorktreePathSet(p, repo, path)
		// D22: a locked entry in the `.git` folder of baseRepo is refused too, with and
		// without worktreeRoot. 89cb6289 answers success there and deletes nothing (measured
		// on Linux, macOS and Windows, and with worktreeRoot on Linux). On Windows c9125eb8
		// and 4c111e86 refuse too (measured).
		if msg := baseRepoLockRefusal(repo, commonDir, "", path, p.WorktreePath, sp, goneLockedWorktreeRefusal); msg != "" {
			return refuse(msg)
		}
		// A worktrees directory that cannot be read does not stop a gone remove. The
		// reference answers success there (measured on Linux with worktreeRoot and a
		// gone target). Without worktreeRoot it is not measured. A worktrees
		// directory that is a symbolic link is read and deleted in no place
		// (openRegistrationsFolder), and the remove goes on. With a locked entry
		// behind the link, 5fd08069 answers success and deletes the branch, and the
		// registration stays (measured on Linux).
		lock, _, matches := worktreeLockedByPath(commonDir, sp)
		if lock != notLocked {
			return refuse(goneLockedWorktreeRefusal(p.WorktreePath, lock))
		}
		if matches == 0 {
			probe.pairIfWorktreesDir(commonDir)
		}
		// A `.git` file of baseRepo whose path ends in a blank keeps the registration,
		// and the branch step still runs. The reference answers so for `gitdir: t.git `
		// (build c8e2f083, measured on Linux). The test covers each white space at the
		// end, not only a blank (claustrum's own, not measured). A remove with
		// worktreeRoot, and every other `.git`, delete the registration as before.
		// A junction or a directory symbolic link at <git dir>\worktrees keeps the
		// entry behind it, and the branch step runs. b3081b51 answers success so
		// (measured on Windows, which refuses worktreeRoot before this step). On
		// Linux and macOS openRegistrationsFolder refuses a symbolic link there too.
		linked := removeRegistrationsLinked(filepath.Join(commonDir, worktreesSubdir))
		// Behind a junction the entry goes only when its record names the leaf as the
		// caller spelled it. A record of the real path keeps its entry, and the folder
		// behind the junction stays (hasJunctionBelow).
		dropSet := sp
		if req.behindJunction {
			dropSet = newWorktreePathSet(p.WorktreePath)
		}
		if !linked && (p.WorktreeRoot != "" || !baseRepoGitFileEndsInSpace(repo)) {
			if createStepHook != nil {
				createStepHook("remove entry step")
			}
			if req.holdsGitDir(commonDir) {
				// A `.git` that left the held folder keeps every registration (no row
				// has a gone leaf there, claustrum's own).
				if req.holdLeftItsPath() {
					return refuse(lockCheckRefusal(p.WorktreePath))
				}
				dropHeldEntryByPath(req.removeHold, commonDir, dropSet)
			} else {
				dropWorktreeEntryByPath(commonDir, dropSet, func() bool { return req.movedSincePin(repo) })
			}
		}
	}
	// A move after the test above reaches no branch of the second repository.
	if createStepHook != nil {
		createStepHook("remove branch step")
	}
	if req.movedSincePin(repo) || (req.removeHold != nil && req.holdLeftItsPath()) || req.dotGitBecameLink(repo) {
		return keptAll()
	}
	// The branch step runs on this path too (measured). Without a repository its
	// for-each-ref fails, and the branch counts as kept (measured).
	kept := runBranchStep(req.use, repo, p.BranchName).kept()
	return rpc.OKResult(req.ID, rpc.WorktreeRemoveReply{Success: true, BranchKept: kept})
}

// goneWorktreePathSet is the set of spellings that the lock tests of a gone
// worktree match. The baseRepo part of the path is also matched in its resolved
// form, and the rest as sent. So a baseRepo sent in 8.3 form or through a junction
// still finds a locked registration when the worktree and its parent are gone
// (measured on Windows). A present worktree does not get this spelling, so an 8.3
// leaf keeps its entry (measured).
func goneWorktreePathSet(p *gitParams, repo, path string) worktreePathSet {
	underBase := ""
	if p.WorktreeRoot == "" {
		if rel, err := filepath.Rel(repo, filepath.Clean(p.WorktreePath)); err == nil && filepath.IsLocal(rel) {
			underBase = filepath.Join(pathcanon.FinalDir(repo), rel)
		}
	}
	return newWorktreePathSet(path, p.WorktreePath, underBase)
}

// worktreeRemoveHomeRefusal is the home guard of git.worktree_remove (D2). It refuses a
// worktreePath that is the home directory or holds it, before anything is deleted.
// worktreePath is `~`-expanded first (internal/pathcanon/expandpath.go), and at 5db5e4a the reference
// answered {"success":true} to "worktreePath":"~" and deleted the home directory. The
// containment checks refuse that input first today, so this guard is defense in depth.
// It is the same defect that destroyed the maintainer's home directory through
// files.extract_tar on 2026-08-02.
func worktreeRemoveHomeRefusal(worktreePath string) string {
	if homeguard.WipesHomeDir(worktreePath) {
		return fmt.Sprintf("worktreePath must not be or contain the home directory: %q", worktreePath)
	}
	return ""
}

// worktreeLeafHomeRefusal is the second home guard of git.worktree_remove (D2). It
// compares the opened leaf folder with home and each parent folder of home by
// identity (homeguard.FolderHoldsHome). So a spelling that the text test misses,
// such as a path through a symbolic link, is refused too, with the same text. A
// leaf that the Stat cannot read keeps the verdict of the text test.
func worktreeLeafHomeRefusal(leafDir *os.Root, worktreePath string) string {
	fi, err := leafDir.Stat(".")
	if err != nil || !homeguard.FolderHoldsHome(worktreePath, fi) {
		return ""
	}
	return fmt.Sprintf("worktreePath must not be or contain the home directory: %q", worktreePath)
}

// pruneGitDir is the git directory whose `worktrees` holds the entries that a remove
// deletes: the repository git directory the trust check pinned for repo. For a main
// repository that is <repo>/.git. For a linked worktree used as baseRepo it is the main
// repository's git directory, since the worktree's own `.git` is a file. A look under
// <worktree>/.git/worktrees finds nothing, because that directory never exists.
// Measured side by side against f6010b97 on Linux (a remove from a linked worktree
// whose commondir starts with white space: the reference deletes the entry). The trust
// check now refuses that commondir (commondirValue, claustrum's own on build 52d82f0a,
// not measured). When the daemon's own environment sets GIT_COMMON_DIR, or the check
// pinned nothing, the old <repo>/.git stays. A removal without worktreeRoot then takes
// the answer of its rev-parse call (registrationGitDir).
func pruneGitDir(repo string) string {
	if !daemonCommonDirSet() {
		if t := gitDirTrustFor(repo); t.verdict == gitDirTrusted && t.pinCommonDir != "" {
			return t.pinCommonDir
		}
	}
	return filepath.Join(repo, ".git")
}

// goneLockCheckText is the refusal of a remove of a gone worktree whose lock check
// failed for reason. A reason of git that cannot run follows a colon, with no
// "retry": b3081b51 answers `... is locked: git cannot run on this host; git not
// run: exit status 1` (measured on Linux, for a plain folder and a repository, a
// gone and an empty leaf). On macOS and Windows the colon form is claustrum's own,
// not measured. A trust refusal of the git directory follows a colon too, with no
// "retry": 4c111e86 and c9125eb8 answer `... is locked: the repository's git
// directory could not be trusted; ...` for a stray commondir (measured on Linux).
// An older macOS row put that text in parentheses. The newer row wins on every
// system. Other reasons keep the parentheses.
func goneLockCheckText(worktreePath, reason string) string {
	if strings.HasPrefix(reason, gitCannotRunPrefix) || strings.HasPrefix(reason, gitDirTrustLead) {
		return "failed to remove worktree: could not check whether " + worktreePath + " is locked: " + reason
	}
	return "failed to remove worktree: could not check whether " + worktreePath +
		" is locked (" + reason + "); retry"
}

// dotGitAbsentOrFolder reports whether repo holds no `.git` entry, or a `.git` that
// is a real folder. A link, a file and an entry that the lstat cannot read give
// false (claustrum's own).
func dotGitAbsentOrFolder(repo string) bool {
	fi, err := os.Lstat(filepath.Join(repo, ".git"))
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return fi.IsDir()
}

// notARepoConfigLengths are the lowest and the highest length of the path
// `<baseRepo>\.git\config`, in UTF-16 units, of a gone worktree that gets
// success. b3081b51 answers so at 262, 267 and 269 (measured on Windows). The
// lengths between are claustrum's own. A variable, so that a test can give the
// length of its own fixture.
var notARepoConfigLengths = [2]int{262, 269}

// measuredNotARepoLength reports whether the config path of repo, the
// baseRepo as the daemon resolved it, has a length of notARepoConfigLengths.
// A name outside ASCII is not measured.
func measuredNotARepoLength(repo string) bool {
	n := len(utf16.Encode([]rune(filepath.Join(repo, ".git", "config"))))
	return n >= notARepoConfigLengths[0] && n <= notARepoConfigLengths[1]
}

// lockCheckRefusal is git.worktree_remove's answer when baseRepo's registrations cannot
// be examined. The path is echoed as given, unquoted and not cut.
func lockCheckRefusal(worktreePath string) string {
	return "failed to remove worktree: could not check whether " + worktreePath +
		" is locked (its registrations could not be examined); retry"
}

// unreadableRepoLockCheckRefusal is the lock-check refusal of git.worktree_remove
// that names the repository (docs/protocol/git-rules-calls.md → "Where each method answers the
// refusal").
func unreadableRepoLockCheckRefusal(worktreePath, repo string) string {
	return "failed to remove worktree: could not check whether " + worktreePath +
		" is locked (the repository at " + repo + " could not be read); retry"
}

// linkedGitDirOf reads a linked worktree's `.git` pointer file
// ("gitdir: <mainGitDir>/worktrees/<name>") and returns that admin directory, or
// "" when worktreePath is not a linked worktree. The rollback of git.worktree_create
// uses it to find the entry of the worktree it created. A `.git` that is not a
// regular file, a FIFO for example, gives "" at once (readGitPlainFile).
func linkedGitDirOf(worktreePath string) string {
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
// worktree.useRelativePaths (measured on macOS). readErr is the error of a record
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
	// pathcanon.Same, not ==: on Windows git writes this record with forward slashes,
	// and once the worktree is deleted neither side can be resolved, so the two
	// spellings differ only in slash direction. Measured against f6010b97 on Windows 11,
	// where the reference deletes the entry.
	return pathcanon.Same(pathcanon.OfGone(record),
		pathcanon.OfGone(filepath.Join(worktreePath, ".git")))
}
