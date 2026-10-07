package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Git-invocation hardening, matching reference build 7d193f89. That build stopped
// shelling out to plain `git <cmd>` and instead runs every git operation under a
// fixed set of `-c` config overrides plus a hardened environment, with any
// config-defined hooks pinned off first. The observable output is unchanged for an
// ordinary repository (the frame battery is byte-identical either way) — this is
// process-level hardening — but it changes behaviour on a repo carrying hooks,
// custom attributes, submodules, or a global excludes/credential config, and this
// project matches the reference as closely as possible even off the wire.
//
// The exact profiles and environment were captured with a logging `git` wrapper on
// an ephemeral VM (scratch/git-hardening-7d193f89.md is the ground truth).

// gitHardenLight is the `-c` set for repo-local reads and mutations that never
// reach a remote (git.info's tree walks, git.list_branches, git.worktree_create).
// The core.excludesFile entry is a PLACEHOLDER: hardenedProfileArgs substitutes the user's
// resolved global excludes (userExcludesFile) for it — 7d193f89 keeps the user's
// ~/.config/git/ignore in force, and the literal /dev/null is only the fallback when
// none exists.
var gitHardenLight = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"branch.autoSetupMerge=false",
	"fetch.bundleURI=",
	"http.saveCookies=false",
	"core.alternateRefsCommand=",
	"alias.remote-https=",
	"alias.remote-http=",
	"alias.remote-ssh=",
	"core.excludesFile=/dev/null", // placeholder — see hardenedProfileArgs / userExcludesFile
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=false",
}

// gitHardenHeavy is the fuller `-c` set for the status/diff plumbing and anything
// that could touch a remote: it additionally forbids every transport protocol and
// clears the credential/askpass helpers.
var gitHardenHeavy = []string{
	"core.fsmonitor=false",
	"core.hooksPath=/dev/null",
	"core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null", // placeholder — see hardenedProfileArgs / userExcludesFile
	"core.longpaths=true",
	"protocol.allow=never",
	"protocol.ext.allow=never",
	"protocol.fd.allow=never",
	"protocol.file.allow=never",
	"protocol.git.allow=never",
	"protocol.ssh.allow=never",
	"protocol.http.allow=never",
	"protocol.https.allow=never",
	"credential.helper=",
	"core.askPass=",
	"fetch.bundleURI=",
	"core.alternateRefsCommand=",
	"alias.remote-https=",
	"alias.remote-http=",
	"alias.remote-ssh=",
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=false",
}

// profileEnv is the part of the environment that the profile decides. The light
// profile sets the protocol gate, terminal prompt suppression, and turns off
// replace objects and grafts. The heavy profile turns off lazy fetch, forbids every
// protocol, and clears the askpass helper.
//
// The light profile turns off replace objects and grafts
// (GIT_NO_REPLACE_OBJECTS=1, GIT_GRAFT_FILE=<null device>). git.worktree_create runs
// every git step under the light profile, except the heavy rev-parse
// --absolute-git-dir and the calls of the branch step (branchStepEnv). Its checkout
// thus uses the real blob and the real commit, even when refs/replace or info/grafts
// name others. git.status runs
// under the heavy profile and still honours replace objects. Both were measured side
// by side against f6010b97 on a Linux VM. git.info and git.list_branches answer the
// same either way. With the graft variable set, git prints its graft-file deprecation
// hint: lines on stderr, as it does for f6010b97.
//
// The null device is os.DevNull: /dev/null, and NUL on Windows. f6010b97 sets
// GIT_GRAFT_FILE=NUL on a Windows VM, and /dev/null on Linux and macOS VMs. The
// variables and their order are those of f6010b97 on the same VMs.
func profileEnv(heavy bool) []string {
	if heavy {
		return []string{
			"GIT_NO_LAZY_FETCH=1",
			"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
			"GIT_ASKPASS=",
			"GIT_TERMINAL_PROMPT=0",
		}
	}
	return []string{
		"GIT_ALLOW_PROTOCOL=" + lightAllowProtocol(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull,
	}
}

// precursorEnv is the environment of the configuration listing that runs before a
// hardened call: listingEnvBase (the daemon's own environment without GIT_CONFIG,
// GIT_CONFIG_PARAMETERS, LC_ALL and LANGUAGE), the profile of the call that follows
// it, pin, and then LC_ALL=C and LANGUAGE=C as the last two entries. git then prints
// its listing errors in English whatever the daemon's locale (rows L01 to L03). It
// carries no hook pins, and the daemon's GIT_CONFIG_COUNT set is kept as it is. The
// heavy set comes before a heavy call and the light set before a light call
// (docs/PROTOCOL.md). The call after the listing keeps the daemon's own LC_ALL,
// LANGUAGE and LANG in their places.
//
// pin is the GIT_COMMON_DIR pin of the git-directory trust check (commonDirPinEnv),
// or nil.
func precursorEnv(heavy bool, pin []string) []string {
	env := append(listingEnvBase(), profileEnv(heavy)...)
	env = append(env, pin...)
	return append(env, "LC_ALL=C", "LANGUAGE=C")
}

// hardenedGitEnv builds the environment for a hardened git command: the daemon's
// own environment without GIT_CONFIG and GIT_CONFIG_PARAMETERS, less GIT_CONFIG_COUNT
// and every GIT_CONFIG_KEY_<digits> and GIT_CONFIG_VALUE_<digits>. Then come the
// profile, pin, and configPinEnv: the count, the inherited pairs and the hook pins of
// hooks (gitenv.go). docs/PROTOCOL.md gives the order.
//
// GIT_OPTIONAL_LOCKS=0 is not part of it. git.status adds it to its status, ls-files
// and diff-index calls only (gitstatus.go). The heavy `rev-parse --absolute-git-dir` of git.worktree_create and
// git.worktree_remove runs without it.
func hardenedGitEnv(heavy bool, pin, hooks []string) []string {
	return hardenedEnvFrom(os.Environ(), heavy, pin, hooks)
}

// hardenedEnvFrom is hardenedGitEnv with daemon as the daemon's environment. The
// light profile's GIT_ALLOW_PROTOCOL still comes from the process environment
// (lightAllowProtocol), not from daemon.
func hardenedEnvFrom(daemon []string, heavy bool, pin, hooks []string) []string {
	return hardenedEnvWith(daemon, profileEnv(heavy), pin, hooks)
}

// hardenedEnvWith is hardenedEnvFrom with the profile part given by the caller.
func hardenedEnvWith(daemon, profile, pin, hooks []string) []string {
	base := dropConfigOverrides(daemon)
	// A refused count gives 0 here.
	count, _ := inheritedConfigProblem(base)
	env := append(withoutConfigCountSet(base), profile...)
	env = append(env, pin...)
	return append(env, configPinEnv(base, count, hooks)...)
}

// branchStepEnv is the environment of the calls of the branch step (worktreebranch.go).
// After the daemon's own part come the light profile (profileEnv) without its
// GIT_ALLOW_PROTOCOL entry, pin, the count, the inherited pairs and the hook pins of
// hooks. Then come GIT_NO_LAZY_FETCH=1, GIT_ALLOW_PROTOCOL=denied_by_claude_ssh and
// GIT_ASKPASS=. 89cb6289 has that order on Linux, macOS and Windows VMs (row R01).
func branchStepEnv(pin, hooks []string) []string {
	var profile []string
	for _, kv := range profileEnv(false) {
		if !strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") {
			profile = append(profile, kv)
		}
	}
	return append(hardenedEnvWith(os.Environ(), profile, pin, hooks),
		"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh", "GIT_ASKPASS=")
}

// hardenedProfileArgs puts the profile's `-c` overrides before the git subcommand.
// There is no `-C dir`: every hardened call selects its repository by its working
// directory, as f6010b97 does on Linux, macOS and Windows VMs. The profile's
// placeholder core.excludesFile is filled in with the user's resolved global
// excludes (userExcludesFile) — 7d193f89 runs every git op with the user's
// ~/.config/git/ignore in force, not /dev/null, so that e.g. git.status classifies
// a globally-ignored file as ignored. On a host with no global excludes the resolver
// returns the null device, leaving the old behaviour.
func hardenedProfileArgs(heavy bool, args ...string) []string {
	return profileArgsWithExcludes(heavy, userExcludesFile(), args...)
}

// profileArgsWithExcludes is hardenedProfileArgs with the core.excludesFile value
// given by the caller.
func profileArgsWithExcludes(heavy bool, excludes string, args ...string) []string {
	profile := gitHardenLight
	if heavy {
		profile = gitHardenHeavy
	}
	full := make([]string, 0, 2*len(profile)+len(args))
	for _, c := range profile {
		if strings.HasPrefix(c, "core.excludesFile=") {
			c = "core.excludesFile=" + excludes
		}
		full = append(full, "-c", c)
	}
	return append(full, args...)
}

// hookPrecursor runs the configuration listing the reference issues before every
// hardened command (`git config -z --list`, with dir as its working directory) and
// returns it. The call after it pins off the hook names the listing shows
// (configPinEnv). heavy is the profile of the call that follows. A failure of this
// listing does not refuse the call. See listingRun.hooks.
//
// The user's global excludes are resolved first. f6010b97 reads them before its
// first listing, measured on Linux and Windows VMs.
func hookPrecursor(ctx context.Context, dir string, heavy bool) listingRun {
	userExcludesFile()
	return runListing(ctx, dir, "", precursorEnv(heavy, commonDirPinEnv(dir)))
}

// hardenedGitCmd builds a hardened git command in dir. When pre is nil the config
// precursor runs first, and the call pins the hooks of that listing. pre is the
// listing of hostileConfigRefusal for the first call after it: that check is the
// listing f6010b97 runs before that call, so the call gets no second one. Measured on
// Linux, macOS and Windows VMs.
func hardenedGitCmd(ctx context.Context, dir string, heavy bool, pre *configListing, args ...string) *exec.Cmd {
	var hooks []string
	if pre == nil {
		hooks = hookPrecursor(ctx, dir, heavy).hooks()
	} else {
		hooks = pre.hooks
	}
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(heavy, args...)...)
	cmd.Dir = dir
	cmd.Env = hardenedGitEnv(heavy, commonDirPinEnv(dir), hooks)
	return cmd
}

// hardenedGitContext runs a git subcommand under the hardening profile and
// environment, with the config precursor first. Combined output, like git().
func hardenedGitContext(ctx context.Context, dir string, heavy bool, args ...string) (string, bool) {
	return hardenedGitCombined(ctx, dir, heavy, nil, args...)
}

func hardenedGitCombined(ctx context.Context, dir string, heavy bool, pre *configListing, args ...string) (string, bool) {
	out, err := hardenedGitCmd(ctx, dir, heavy, pre, args...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// hardenedGitFirst is hardenedGit for the first call after hostileConfigRefusal on
// the same dir. It runs no config precursor, because that check was its precursor,
// and it pins the hooks of that check's listing l.
func hardenedGitFirst(dir string, heavy bool, l configListing, args ...string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	return hardenedGitCombined(ctx, dir, heavy, &l, args...)
}

// gitDirWorkTreeToplevel runs `git --git-dir=<gitDir> config -z --list`, then the light
// `git -c … --git-dir=<gitDir> --work-tree=<workTree> rev-parse --show-toplevel`. Both
// run in gitDir and carry pin. It returns the answer of the second call. 89cb6289 runs
// this pair in git.info on Windows (row W01) and in git.worktree_create after a
// mismatched admin record (row I07a, macOS VM).
func gitDirWorkTreeToplevel(gitDir, workTree string, pin []string) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	userExcludesFile()
	hooks := runListing(ctx, gitDir, gitDir, precursorEnv(false, pin)).hooks()
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(false,
		"--git-dir="+gitDir, "--work-tree="+workTree, "rev-parse", "--show-toplevel")...)
	cmd.Dir = gitDir
	cmd.Env = hardenedGitEnv(false, pin, hooks)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\r\n"), err
}

// hardenedGitStderr is hardenedGitContext for a call whose failure text goes on the
// wire: it returns stderr only, untrimmed, and the exec error. stdout is not kept,
// because the failure frames of git.worktree_create quote stderr only (measured
// against f6010b97 and 90fca6e6 on a macOS VM). See worktreeGitText.
func hardenedGitStderr(ctx context.Context, dir string, heavy bool, args ...string) (string, error) {
	cmd := hardenedGitCmd(ctx, dir, heavy, nil, args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return errBuf.String(), err
}

// hardenedGit is hardenedGitContext with the standard git timeout context.
func hardenedGit(dir string, heavy bool, args ...string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	return hardenedGitContext(ctx, dir, heavy, args...)
}

// statusGitDirTempPrefix and checkoutIndexTempPrefix start the names of the
// temporary git dir of git.status and the temporary index directory of the
// git.worktree_create checkout. Each has the length of the prefix in the f6010b97
// argv and env, measured on Linux, macOS and Windows VMs: 18 and 17 bytes.
// os.MkdirTemp adds a random decimal suffix, as f6010b97 does.
const (
	statusGitDirTempPrefix  = "claustrum-git-dir-"
	checkoutIndexTempPrefix = "claustrum-gitidx-"
)

// worktreeCreateDrainCap bounds the post-exit pipe drain of git.worktree_create's
// read-tree checkout. Measured against 4534d86: when the checkout leaves a descendant
// (a smudge/hook filter, or something it backgrounds) holding one of the daemon's
// output pipes, the reference caps the drain at a FIXED ~5.0s from the checkout git's
// OWN exit — independent of the caller timeoutMs (measured 5.004 / 5.007 / 5.009s) —
// then reaps the descendant (so the reply arrives at ~5s, not at the descendant's
// lifetime), and only then gates the reply on timeoutMs
// (scratch/probe/wt-success-lingering-4534d86.md).
//
// This is the drain-cap grace period of exec.Cmd.WaitDelay, whose timer for a
// pipe-drain overrun starts when the process itself exits — so the cap is measured
// from git-exit, matching the reference. (var, not const, so tests can shrink it.)
var worktreeCreateDrainCap = 5 * time.Second

// hardenedGitCheckout runs the read-tree checkout of git.worktree_create under the
// hardening profile, in its OWN process group, with the post-exit output-pipe drain
// capped at worktreeCreateDrainCap when a deadline is armed. It is the only git exec
// path that caps the drain and reaps the process group, because it is the only path
// measured to need it: the read-tree checkout can run a smudge/hook filter that
// backgrounds a descendant which inherits the daemon's output pipes and outlives git.
//
// The call has the shape measured against f6010b97 and 90fca6e6 on a Windows VM. Its
// working directory is the new worktree (leaf), it passes no -C, and its --git-dir is
// gitDir, the git dir of baseRepo. The index goes to indexFile through GIT_INDEX_FILE.
// The config precursor runs the same way: `--git-dir=<gitDir> config -z --list`
// with the leaf as its working directory, and the light precursorEnv.
// pin is the GIT_COMMON_DIR pin of baseRepo (commonDirPinEnv), or nil. Both calls
// carry it. args carries the -c pins and options after the hardening profile, and
// the read-tree subcommand itself.
//
// It returns:
//   - stderr: stderr only, untrimmed. The failure frames quote it (worktreeGitText).
//     stdout goes to its own pipe, which the drain cap also covers, and is not kept.
//   - drained: git EXITED 0 but a descendant held a pipe past the drain cap. Go
//     surfaces this as exec.ErrWaitDelay. The caller routes it to the timeoutMs
//     verdict (success if timeoutMs exceeded the drain, else timeout + rollback with
//     the "after the checkout finished" message) — NEVER to worktree_add_failed and
//     NEVER to "during the checkout". One case is apart: on Linux and macOS, an
//     index install that fails after a drain overrun answers worktree_add_failed
//     (runWorktreeCheckout, claustrum's choice, not measured);
//   - err: the underlying exec error — git's own "signal: killed" *ExitError when the
//     deadline killed a still-running git, exec.ErrWaitDelay on a drain overrun, or a
//     non-zero *ExitError otherwise. nil when git exited 0.
//
// With no deadline armed (D5 off and timeoutMs off) WaitDelay is left unset, so the
// drain is unbounded and byte-identical to the reference default, which applies no cap.
func hardenedGitCheckout(ctx context.Context, leaf, gitDir, indexFile string, pin []string, args ...string) (stderr string, drained bool, err error) {
	hooks := runListing(ctx, leaf, gitDir, precursorEnv(false, pin)).hooks()
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(false, args...)...)
	cmd.Dir = leaf
	cmd.Env = append(hardenedGitEnv(false, pin, hooks), "GIT_INDEX_FILE="+indexFile)
	// Own process group so the teardown below can SIGKILL the whole group and reap a
	// descendant the checkout left holding the pipe — reproducing 4534d86's observed
	// descendant reap.
	cmd.SysProcAttr = newSysProcAttr()
	if _, deadlined := ctx.Deadline(); deadlined {
		// Cap the drain from git's OWN exit (WaitDelay's pipe-drain timer starts at
		// process exit), reproducing the reference's fixed post-exit cap.
		cmd.WaitDelay = worktreeCreateDrainCap
		// When the deadline fires, kill git immediately and, on Unix, tear down its whole
		// process group so a descendant is reaped too. cmd.Process.Kill() is what makes the
		// interrupt land on every OS: reapProcessGroup is a no-op on Windows, so without the
		// direct kill a Windows deadline would leave git running until the WaitDelay fallback
		// and answer the caller ~worktreeCreateDrainCap late. WaitDelay still bounds any
		// post-kill pipe drain. Returning nil marks the interrupt as successful; Wait still
		// prefers git's own "signal: killed" *ExitError, so the wire suffix is unchanged.
		cmd.Cancel = func() error {
			reapProcessGroup(cmd.Process)
			_ = cmd.Process.Kill()
			return nil
		}
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	drained = errors.Is(err, exec.ErrWaitDelay)
	if drained {
		// git exited 0 but a descendant held the pipe past the cap. WaitDelay closed
		// the daemon's own pipe ends but killed nothing; SIGKILL the group to reap the
		// descendant, matching the reference's observed descendant reap.
		reapProcessGroup(cmd.Process)
	}
	return errBuf.String(), drained, err
}

// runWorktreeCheckout is the read-tree checkout of git.worktree_create. It reads rev
// into a new index in a fresh temporary directory, fills leaf from it, and, when
// git exits 0, places that index in adminDir, the new worktree's registration
// (placeWorktreeIndex). The caller gets adminDir from createdIndexDir. The temporary directory is removed afterwards. stderr,
// drained and err are those of hardenedGitCheckout. The -c pins
// core.splitIndex=false and core.commitGraph=false follow the profile, as in the
// argv measured against f6010b97. workTree is the --work-tree value
// (checkoutWorkTree). The working directory stays leaf.
//
// installErr is the error of a placement that failed. The caller answers it as a
// failed checkout. On Windows it is always nil.
func runWorktreeCheckout(ctx context.Context, leaf, workTree, gitDir, adminDir, rev string, pin []string) (stderr string, drained bool, err, installErr error) {
	idxDir, err := os.MkdirTemp("", checkoutIndexTempPrefix)
	if err != nil {
		return "", false, err, nil
	}
	defer func() { _ = os.RemoveAll(idxDir) }()
	idx := filepath.Join(idxDir, "index")
	stderr, drained, err = hardenedGitCheckout(ctx, leaf, gitDir, idx, pin,
		"-c", "core.splitIndex=false", "-c", "core.commitGraph=false",
		"--git-dir="+gitDir, "--work-tree="+workTree,
		"read-tree", "-u", "--reset", "--no-recurse-submodules", rev)
	if err == nil || drained {
		installErr = placeWorktreeIndex(idx, adminDir, leaf)
	}
	return stderr, drained, err, installErr
}

// placeWorktreeIndex is installWorktreeIndex behind a guard. On Linux and macOS the
// index goes into the registration adminDir only if its gitdir record can be read
// and names leaf (readAdminRecord). In every other state of a registration that
// can be reached, nothing is placed, and an index that is there keeps its bytes: a
// record that is missing, a FIFO or a folder, an empty or a relative record of
// another worktree, and the record of another path. The create refuses those states
// before the checkout, so only a record that changed during the checkout reaches the
// guard. The error text is claustrum's own (not measured).
//
// One state is apart: a registration folder whose stat fails. No index of it can be
// reached then, and the placement runs and fails by itself. Its error is the measured
// text of a registration that is gone (cell Z10) or of a registrations directory
// without the search permission (cells Z11a and Z11b, macOS VM).
//
// Cell Z15 (macOS VM) removes the record after the read-tree and sets the
// registration to mode 0500. 89cb6289 answers "openat w1/index: permission denied"
// there. claustrum answers the text of the guard: that is a known difference.
func placeWorktreeIndex(idx, adminDir, leaf string) error {
	if adminRecordChecked {
		if _, err := os.Stat(adminDir); err == nil && readAdminRecord(adminDir, leaf) != recordNamesLeaf {
			return errors.New("the registration " + adminDir + " has no gitdir record that names this worktree")
		}
	}
	return installWorktreeIndex(idx, adminDir)
}

// indexInstallText is the text after "git worktree add failed (checkout): " when
// the placement of the index failed. The stderr of the checkout and the error are
// joined with nothing between them, and the rule of worktreeGitText then applies to
// the joined text. Measured against 89cb6289 on a Linux VM:
//
//   - A stderr that ends with one newline gives one space before the error (rows
//     A14 and A14f). With no final newline there is no space (cell Y2a), and with
//     two there are two (cell Y2b).
//   - With no stderr, the text is the error alone (cell X2).
//   - The stderrHeadCap cut covers the error too. A stderr of 478 bytes keeps the
//     whole error, 500 bytes keep its first 12 bytes, and 512 or 1509 bytes keep
//     none of it (cells Y1b, Y1a, Y1c and X3).
func indexInstallText(stderr string, installErr error) string {
	return worktreeGitText(stderr+installErr.Error(), nil)
}

// repoGitDir is the git dir of repo when `rev-parse --absolute-git-dir` gives no
// answer: the admin dir that repo's .git file names, or <repo>/.git.
func repoGitDir(repo string) string {
	if admin := worktreeAdminDir(repo); admin != "" {
		if !filepath.IsAbs(admin) {
			admin = filepath.Join(repo, admin)
		}
		return admin
	}
	return filepath.Join(repo, ".git")
}

// hardenedGitStdout is hardenedGit but returns stdout only and the exec error,
// for callers that split a warning on stderr from a real failure (git.status,
// git.list_branches).
func hardenedGitStdout(dir string, heavy bool, args ...string) (string, error) {
	return hardenedGitRun(dir, heavy, nil, args...)
}

// hardenedGitStdin is hardenedGitStdout on the light profile, with stdin fed to
// git. The .worktreeinclude scan uses it for `git check-ignore --stdin`.
func hardenedGitStdin(dir, stdin string, args ...string) (string, error) {
	return hardenedGitRun(dir, false, strings.NewReader(stdin), args...)
}

func hardenedGitRun(dir string, heavy bool, stdin io.Reader, args ...string) (string, error) {
	return hardenedGitRunPre(dir, heavy, nil, stdin, args...)
}

// hardenedGitRunPre is hardenedGitRun with the precursor choice of hardenedGitCmd.
func hardenedGitRunPre(dir string, heavy bool, pre *configListing, stdin io.Reader, args ...string) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := hardenedGitCmd(ctx, dir, heavy, pre, args...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// statusExcludesFile is the core.excludesFile value of the status, ls-files and
// diff-index calls of git.status. It is userExcludesFile, except that the null device is spelled /dev/null on every OS.
// On Windows git status exits 128 with "fatal: cannot use NUL as an exclude file"
// when core.excludesFile is NUL. It accepts /dev/null there. git ls-files and git
// check-ignore accept NUL. Measured with Git for Windows 2.55.0 on a Windows 11 VM.
// f6010b97 passes NUL to its status call on that VM and answers exit status 128.
// This value on those three calls is the divergence D16. On Linux and macOS os.DevNull is /dev/null,
// so nothing changes there.
func statusExcludesFile() string {
	if e := userExcludesFile(); e != os.DevNull {
		return e
	}
	return "/dev/null"
}

// gitEmptyTree is git's canonical empty-tree object (SHA-1). Passing it as
// --attr-source makes git read gitattributes from an empty tree — i.e. ignore a
// repository's in-repo .gitattributes.
const gitEmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var (
	attrSourceOnce sync.Once
	attrSourceOK   bool
)

// attrSourceArgs returns the `--attr-source=<empty-tree>` top-level git option when
// the runtime git supports it, else nil. git.status runs it (4534d86 does) so a
// repository's in-repo .gitattributes cannot influence the status: without it a
// .gitattributes clean filter runs during `git status` and can both flip a file's
// modified-ness (wire-visible) and execute an attacker-controlled command
// (scratch/probe/attrsource-4534d86.md). It is a no-op on a repo with no attribute
// rules, so status on an ordinary repo stays byte-identical.
//
// git added --attr-source in 2.40, so support is probed once with the reference's own
// guard — `git --attr-source=<empty> version`, which errors on older git. The option
// is a top-level one, the first argument of the status call, as f6010b97 emits it.
//
// The probe runs with the daemon's own environment and adds no variable. f6010b97
// adds none to it on Linux and Windows VMs.
func attrSourceArgs() []string {
	attrSourceOnce.Do(func() {
		cmd := exec.Command("git", "--attr-source="+gitEmptyTree, "version")
		attrSourceOK = cmd.Run() == nil
	})
	if attrSourceOK {
		return []string{"--attr-source=" + gitEmptyTree}
	}
	return nil
}

var (
	// A mutex rather than a sync.Once, for two reasons. A test that moves HOME or
	// XDG_CONFIG_HOME has to force a re-resolve, and clearing a Once means either
	// copying a lock (go vet copylocks) or writing the pair with no
	// synchronisation at all, which races any in-flight request goroutine.
	userExcludesMu     sync.Mutex
	userExcludesDone   bool
	userExcludesCached string
)

// userExcludesFile resolves the user's global git excludes file once: the configured
// core.excludesFile if it is an absolute path, else $XDG_CONFIG_HOME/git/ignore or
// $HOME/.config/git/ignore when that file exists, else the null device (os.DevNull:
// /dev/null, and NUL on Windows, as f6010b97 passes on a Windows VM). This is the
// excludes path 7d193f89 passes as core.excludesFile on every git invocation (observed
// via a git-argv trace). hardenedProfileArgs substitutes it for every git op.
func userExcludesFile() string {
	userExcludesMu.Lock()
	defer userExcludesMu.Unlock()
	if !userExcludesDone {
		userExcludesCached = resolveUserExcludesFile()
		userExcludesDone = true
	}
	return userExcludesCached
}

func resolveUserExcludesFile() string {
	// The configured value wins if absolute. `config --includes --path` follows
	// includes and expands a leading ~, matching the reference. It runs WITHOUT the
	// hardening profile so git reports the real global setting, not the pin.
	ctx, cancel := gitCtx()
	defer cancel()
	// GIT_DIR=<null device> makes this read only the user's global/system config, never
	// a repo's own core.excludesFile — a repo must not be able to inject excludes. This
	// is the once-cached read that replaced the reference's per-call excludes probe.
	// Its working directory is the temporary directory, and GIT_DIR is its only added
	// variable. f6010b97 runs it so on Linux and Windows VMs, with GIT_DIR=NUL on
	// Windows.
	cmd := exec.CommandContext(ctx, "git", "config", "--includes", "--path", "core.excludesFile")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GIT_DIR="+os.DevNull)
	if out, err := cmd.Output(); err == nil {
		if p := strings.TrimSpace(string(out)); filepath.IsAbs(p) {
			return p
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		if p := filepath.Join(xdg, "git", "ignore"); fileExists(p) {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".config", "git", "ignore"); fileExists(p) {
			return p
		}
	}
	return os.DevNull
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// configCheck is the outcome of hostileConfigRefusal.
type configCheck struct {
	// refusal is the whole refusal text when the method refuses, else "".
	refusal string
	// noRepo is true when the method answers as if dir held no repository.
	noRepo bool
	// noRepoText is git's stderr through worktreeGitText when the listing itself said
	// "not a git repository". git.worktree_remove with worktreeRoot quotes it.
	noRepoText string
	// listing is the listing of a check that passed. The first hardened call after the
	// check pins its hooks (hardenedGitFirst).
	listing configListing
}

// refused reports whether the method stops on this check, with a refusal or with the
// "no repository" answer.
func (c configCheck) refused() bool {
	return c.refusal != "" || c.noRepo
}

// hostileConfigRefusal runs the configuration listing (`git config -z --list`) that
// comes first in a git method, and reads it. Callers phrase the method-specific frame
// (a -32603 for the read methods, a worktreeResult for worktree_create, its own
// message for worktree_remove). The outcomes, in the order 89cb6289 takes them on
// Linux, macOS and Windows VMs:
//
//  1. PATH holds no git: "no repository" (row L13). A Linux VM measured the other
//     methods. Rows L13xa and L13xb are its removes without worktreeRoot.
//     git.worktree_remove with worktreeRoot does not come here with no git:
//     externalWorkTreeRefusal answers first.
//  2. The listing exits 128 and says "fatal: not a git repository": "no repository"
//     (rows L14a, L14b, L14d, L14e and N01 to N05). See listingRun.saysNoRepository.
//  3. Any other failure: `git version` runs. If it fails too, the refusal starts with
//     gitCannotRunPrefix, else with hooksRefusalPrefix (rows L10 to L12, L14c and L15).
//  4. The listing succeeded but cannot be used: a key over configKeyMaxBytes (row
//     L08c), or hook names over hookNameMaxCount or hookNameMaxBytes (rows L06b and
//     L07b). The key refusal carries the listing prefix. The two hook refusals carry
//     hooksPinPrefix only.
//
// In git.info, git.list_branches, git.worktree_create and git.worktree_remove, this
// check is the first listing of the method, not an extra one. It stands in for the
// precursor of the method's first hardened call: dir is its working directory, and
// heavy is the profile of that call. f6010b97 makes one listing there, not two, on
// Linux, macOS and Windows VMs. When the next call is on the same dir, the caller
// runs it with no precursor of its own (hardenedGitFirst).
//
// The check of the daemon's own GIT_CONFIG_COUNT (daemonCountRefusal) comes
// first. When it refuses, no listing runs, and a directory is refused with its text.
// A dir that does not exist, or is not a directory, is not refused by that check. See
// docs/PROTOCOL.md, "The daemon's own git environment".
func hostileConfigRefusal(dir string, heavy bool) configCheck {
	if msg, bad := daemonCountRefusal(); bad {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return configCheck{}
		}
		return configCheck{refusal: msg}
	}
	ctx, cancel := gitCtx()
	defer cancel()
	r := runListing(ctx, dir, "", precursorEnv(heavy, commonDirPinEnv(dir)))
	if r.err == nil {
		l, reason := parseConfigListing(r.out)
		switch reason {
		case "":
			return configCheck{listing: l}
		case listingReadRefusal:
			return configCheck{refusal: hooksRefusalPrefix + reason}
		default:
			return configCheck{refusal: hooksPinPrefix + reason}
		}
	}
	return failedListingCheck(r, dir, heavy)
}

// failedListingCheck sorts the failed listing r of dir into its class: outcomes 1 to
// 3 of hostileConfigRefusal. heavy is the profile of the listing.
func failedListingCheck(r listingRun, dir string, heavy bool) configCheck {
	if r.gitNotOnPath() {
		return configCheck{noRepo: true}
	}
	if r.saysNoRepository() {
		return configCheck{noRepo: true, noRepoText: worktreeGitText(r.stderr, nil)}
	}
	// A failure to CHDIR into dir — it does not exist, is a non-directory, or a parent
	// is not traversable — is NOT a hostile config: git never reached a repo, so there
	// are no config-defined hooks to pin off. This is a fully client-controlled honest
	// input (a stale or mistyped path), so detect it with os.Stat, not from any git
	// text. The reference lets such an input fall through to its normal
	// not-a-repo shape (measured against 7d193f89: a nonexistent path/baseRepo answers
	// isRepo:false on the read methods, not_a_repo on worktree_create, and success on
	// worktree_remove — NOT this refusal). Only a config git CAN reach but cannot parse
	// (a corrupt .git/config) is refused here.
	//
	// 89cb6289 still runs `git version` after such a start failure, and its frames
	// stay the same (rows L16a, L16b, L16d and L16f: a file and a folder of mode 0600).
	// When that `git version` fails for a dir that exists, the method refuses with the
	// "cannot run" text and the detail of the version call. 89cb6289 answers so for
	// git.info on a regular file and on a folder of mode 0000 (rows A-M2 and A-X1 in
	// phase P11, Linux and macOS VMs). It answers so for git.list_branches, git.status
	// and git.worktree_create too: on a regular file (cell C-g, Linux and macOS VMs)
	// and on a folder of mode 0000 (cell C-h in P11, Linux VM). git.worktree_remove
	// is not measured on a regular file there. A dir that does not exist keeps its
	// answer then (rows A-M1 and A-M3 to A-M5 in P11).
	// Not measured: a path under a regular file with a failing `git version`. claustrum
	// keeps its answer. A path that does not exist is measured on
	// git.worktree_remove with worktreeRoot only for its calls. 89cb6289 runs no `git
	// version` there (rows L13wa-g and L13wb-g), and that request does not come here
	// (externalWorkTreeRefusal).
	//
	// On Windows a dir that exists and is not a folder is not such an input: the start
	// error of the listing is a refusal there (see unenterableDir).
	if unenterableDir(dir, r.err) {
		if v := gitVersionRun(heavy); v.err != nil {
			if _, err := os.Stat(dir); err == nil {
				return configCheck{refusal: gitCannotRunPrefix + v.detail()}
			}
		}
		return configCheck{}
	}
	return configCheck{refusal: failedListingText(r, heavy)}
}

// unenterableDir reports whether the listing failed because git cannot start in dir:
// dir does not exist, is not a directory, or cannot be searched. err is the listing's
// exec error.
//
// With nonFolderStartRefuses (Windows), a dir that exists and is not a directory
// reports false, so the start error of the listing is a refusal. 89cb6289 answers so
// on a Windows VM for a regular file and a file symlink: the hooks refusal that ends
// with `fork/exec <git.exe path>: The directory name is invalid.` (cells A-01 to A-04,
// A-04b2, A-06 and A-10). The path NUL and the name of a file with one more dot at
// its end get the same refusal (cells Wc). When `git version` fails too, it answers
// the "cannot run" text (cells A-14 and Wa-infoF). A path that does not exist, a path under a file and a junction
// whose target is gone keep their answer there (cells A-07m, A-07s and A-11). A folder
// with a path of 296 characters gets the same refusal (cell A-13). From the code: that
// folder passes os.Stat here, and the stat of "<dir>/." below passes too.
func unenterableDir(dir string, err error) bool {
	fi, statErr := os.Stat(dir)
	if statErr != nil {
		return true
	}
	if !fi.IsDir() {
		return !nonFolderStartRefuses
	}
	// Residual fallback for the one chdir failure os.Stat cannot see: dir exists and is
	// a directory, but git cannot start in it (dir itself lacks +x). The start then
	// fails with a *fs.PathError, before git runs. Only that case falls through. The
	// search test is a stat of "<dir>/.", which needs +x on dir. Any other start
	// failure keeps the refusal.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		if _, serr := os.Stat(dir + string(os.PathSeparator) + "."); serr != nil {
			return true
		}
	}
	return false
}

// hooksRefusalPrefix starts the hooks refusal. The exec error and git's text follow.
const hooksRefusalPrefix = hooksPinPrefix + "listing the configuration in force: "

// stderrHeadCap is the byte cap 7d193f89 applies to captured git stderr before it
// reaches an error frame.
const stderrHeadCap = 512

// worktreeGitText makes the git text of the git.worktree_create failure frames: the
// checkout failure, the add failure, the killed checkout, and each of the two parts
// of the attach-fallback frame. The rule was measured against f6010b97 and 90fca6e6
// on a macOS VM, and both builds agree:
//
//  1. Use stderr only. stdout is not quoted.
//  2. Keep the first stderrHeadCap bytes.
//  3. Drop every byte that is not valid UTF-8, anywhere in the text. The cap comes
//     first, so the bytes of a rune that the cap cuts are dropped too.
//  4. Replace each rune that is not printable with one space. Runs are not
//     collapsed, so "\r\n" gives two spaces.
//  5. Trim the spaces at both ends.
//  6. If the result is empty, use the exec error, for example "exit status 128" or
//     "signal: killed".
//
// Step 4 uses unicode.IsPrint. That is an inference from the measured payloads, not
// a proof: every sample fits it. The samples cover \r, \n, \t, NUL, \x01, \x1b, \x7f,
// U+0085, U+009B, U+00A0, U+200B and U+2028, which all became one space.
func worktreeGitText(stderr string, err error) string {
	s := stderr
	if len(s) > stderrHeadCap {
		s = s[:stderrHeadCap]
	}
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, s)
	s = strings.Trim(s, " ")
	if s == "" && err != nil {
		s = err.Error()
	}
	return s
}

// noRepositoryAt reports whether the hardened `git rev-parse --absolute-git-dir`
// fails in dir, an existing directory. That covers a directory in which git finds
// no repository. A path that does not exist, or is not a directory, reports
// false: that input keeps its own answer on each method.
//
// It runs right after hostileConfigRefusal on dir, so it runs no precursor, and it
// pins the hooks of that check's listing l.
func noRepositoryAt(dir string, l configListing) bool {
	return repositoryCheckError(dir, &l) != nil
}

// repositoryCheckError is noRepositoryAt with the exec error of the failed call, for
// example "exit status 128". It answers nil where noRepositoryAt answers false.
//
// The call runs on the heavy profile, as f6010b97 runs it on Linux, macOS and
// Windows VMs. pre is the listing of hostileConfigRefusal when this is the first call
// after it on dir, else nil (see hardenedGitCmd).
func repositoryCheckError(dir string, pre *configListing) error {
	_, err := repositoryGitDir(dir, pre)
	return err
}

// repositoryGitDir is repositoryCheckError with the answer of the call: the git
// directory that git names for dir. The answer is "" where repositoryCheckError
// answers nil without a call, and after a failed call.
func repositoryGitDir(dir string, pre *configListing) (string, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", nil
	}
	out, err := hardenedGitRunPre(dir, true, pre, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\r\n"), nil
}

// statInsideDir looks at name inside dir through a handle on dir, without
// following a symlink at name. It reports nil when dir does not exist, and when
// name does not exist: those inputs keep their own answers. Any other failure is
// returned as is. A directory that can be opened but not searched (mode 0600)
// gives "statat <name>: permission denied". One that cannot be opened at all
// (mode 0000) gives "open <dir>: permission denied". A .claude that is a symlink,
// even to a place outside dir, passes.
func statInsideDir(dir, name string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
