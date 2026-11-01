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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Git-invocation hardening. The call shape came from reference build 7d193f89, and
// the two lists and the variables are now those of 5fd08069, with an empty
// core.fsmonitor= of claustrum's own (see gitHardenLight and gitHardenHeavy). 7d193f89 stopped
// shelling out to plain `git <cmd>` and instead runs every git operation under a
// fixed set of `-c` config overrides plus a hardened environment, with any
// config-defined hooks pinned off first. The observable output is unchanged for an
// ordinary repository (the frame battery is byte-identical either way) — this is
// process-level hardening — but it changes behaviour on a repo carrying hooks,
// custom attributes, submodules, or a global excludes/credential config, and this
// project matches the reference as closely as possible even off the wire.
//
// The exact profiles and environment were captured with a logging `git` wrapper on
// an ephemeral VM.

// gitHardenLight is the `-c` list of the calls of git.worktree_create and of the
// branch step. It is the list of 20 items that 5fd08069 sends, except that
// core.fsmonitor= is empty (claustrum's own, not measured). s3-linux cells B0 and
// B1 show it on a Linux VM. Cell 50 of s3p2b-edges shows it on macOS and Windows
// VMs. The measured calls are `rev-parse --is-inside-work-tree`, `rev-parse
// --abbrev-ref HEAD`, `worktree add`, `read-tree`, `for-each-ref --count=10001`,
// `rev-list` and `update-ref`. `read-tree` and `rev-list` add their own items after
// the list. The last three calls are in s3p2-linux2 cell L8 (Linux VM) and in cell
// 50. With "shadowDirOff":true, val-s3p2b1 cell A3 shows the list on the calls of
// existingBranch, of sourceBranch and of the .worktreeinclude step (Linux, macOS and
// Windows VMs). Cell A4 shows it on the calls of worktreeRoot (Linux and macOS VMs).
//
// The advice.graftFileDeprecated item turns off git's graft-file hint: lines. So
// the failure texts of git.worktree_create do not start with that hint. Those of
// 5fd08069 do not either, and those of 89cb6289 start with 8 hint: lines
// (val-s3p2b1 cell B2, Linux, macOS and Windows VMs).
//
// With worktree.useRelativePaths=false a new worktree gets absolute paths, also
// where the user's config sets the key. 5fd08069 does the same, and 89cb6289 writes
// relative paths there (val-s3p2b1 cell B6, macOS and Windows VMs). With
// core.longpaths=true a create with a tree path of 270 characters succeeds on
// Windows, as on 5fd08069. 89cb6289 fails there (val-s3p2b1 cell B4, Windows VM).
//
// The core.excludesFile entry is a placeholder. profileArgsWithExcludes puts the
// user's excludes file there (userExcludesFile). On the Windows VM 5fd08069 sends
// the user's ignore file there (cell 50). core.hooksPath is hooksNullDevice.
var gitHardenLight = []string{
	"core.hooksPath=" + hooksNullDevice,
	"core.fsmonitor=",
	"branch.autoSetupMerge=false",
	"fetch.bundleURI=",
	"http.saveCookies=false",
	"core.alternateRefsCommand=",
	"alias.remote-https=",
	"alias.remote-http=",
	"alias.remote-ssh=",
	"alias.remote-denied_by_claude_ssh.command=",
	"help.autocorrect=0",
	"core.excludesFile=/dev/null", // placeholder, see profileArgsWithExcludes
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"advice.graftFileDeprecated=false",
	"worktree.useRelativePaths=false",
	"core.longpaths=true",
}

// gitHardenHeavy is the `-c` list of the calls of git.info, git.status and
// git.list_branches, and of each `rev-parse --absolute-git-dir`. It is the list of
// 28 items that 5fd08069 sends on the same calls, except that core.fsmonitor=
// is empty (claustrum's own, not measured). s3-linux cells B0 and B1 show it
// on a Linux VM. Cell B1 of s3-macos and of s3-windows shows it for git.info and
// git.list_branches on macOS and Windows VMs, with "shadowDirOff":true. On the
// Windows VM 5fd08069 sends /dev/null in core.attributesFile (cell B1 of
// s3-windows). core.hooksPath is hooksNullDevice. 89cb6289 sends 13 items for git.info and
// git.list_branches, and 23 for git.status apart from its `rev-parse
// --show-toplevel` pair.
var gitHardenHeavy = []string{
	"core.fsmonitor=",
	"core.hooksPath=" + hooksNullDevice,
	"core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null", // placeholder, see profileArgsWithExcludes
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
	"alias.remote-denied_by_claude_ssh.command=",
	"help.autocorrect=0",
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"core.commitGraph=false",
}

// profileEnv is the part of the environment that the profile decides.
//
// The heavy profile has six variables. It forbids every protocol, clears the
// askpass helper, and turns off lazy fetch, replace objects and grafts. 5fd08069
// sends them in this order on a Linux VM (s3-linux cell B1, with
// "shadowDirOff":true). val-s3p2b1 cell A1 shows the same order on Linux and macOS
// VMs, with that member. The Windows logs of 5fd08069 are sorted by name. They show
// the same six values and no order (cell 50 of s3p2b-edges, and cell B1 of
// s3-windows with "shadowDirOff":true). 89cb6289 sends four: it has no
// GIT_NO_REPLACE_OBJECTS and no GIT_GRAFT_FILE there. So git.status of claustrum
// reads the real objects now, also where refs/replace names others. 5fd08069
// answers the same, and 89cb6289 answers against the replacement (val-s3p2b1 cell
// B1, Linux, macOS and Windows VMs).
//
// The light profile sets the protocol gate and terminal prompt suppression, and
// turns off replace objects and grafts. git.worktree_create runs its hardened calls
// under the light profile, except the heavy rev-parse --absolute-git-dir, the heavy
// pair of gitDirWorkTreeToplevel and the calls of the branch step (branchStepEnv).
// Its checkout thus uses the real blob and the real commit, even when refs/replace
// or info/grafts name others. That was measured side by side against f6010b97 on a
// Linux VM. 5fd08069 sends the same four with "shadowDirOff":true (cell B1 of
// s3-linux, s3-macos and s3-windows, on Linux, macOS and Windows VMs). Without
// that member its two rev-parse calls and its worktree add call carry the six
// values. Its read-tree keeps the four (s3-linux cell B0, Linux VM). claustrum
// sends the six there in the state shadow (gitUse.profileEnv).
//
// With the graft variable set, git prints its graft-file deprecation hint: lines
// on stderr. The light list turns that hint off, and the heavy list does not. The
// status, rev-parse and diff-index calls of git.status print it on 5fd08069 and on
// claustrum. The repository there has no info/grafts file (val-s3p2b1 cell B8, Linux
// and Windows VMs). No reply line of cells B5 and B8 holds it (Linux, macOS and
// Windows VMs).
//
// The null device is os.DevNull: /dev/null, and NUL on Windows. 5fd08069 sets
// GIT_GRAFT_FILE=NUL on a Windows VM (cell 50 of s3p2b-edges), and /dev/null on
// Linux and macOS VMs.
func profileEnv(heavy bool) []string {
	if heavy {
		return []string{
			"GIT_NO_LAZY_FETCH=1",
			"GIT_NO_REPLACE_OBJECTS=1",
			"GIT_GRAFT_FILE=" + os.DevNull,
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
// hardened call: listingEnvBase (the daemon's own environment after gitChildEnv,
// without GIT_CONFIG, GIT_CONFIG_PARAMETERS, LC_ALL and LANGUAGE), the profile of the call that follows
// it, pin, and then LC_ALL=C and LANGUAGE=C as the last two entries. git then prints
// its listing errors in English whatever the daemon's locale (rows L01 to L03,
// TestListingLocaleEnv). It
// carries no hook pins, and the daemon's GIT_CONFIG_COUNT set is kept as it is. The
// heavy set comes before a heavy call and the light set before a light call
// (checkAlternation in TestHardenedGitCallShape). The call after the listing keeps
// the daemon's own LC_ALL, LANGUAGE and LANG in their places.
//
// pin is the GIT_COMMON_DIR pin of the git-directory trust check (commonDirPinEnv),
// or nil. gitUse.precursorEnv is this function with the profile of one request.
func precursorEnv(heavy bool, pin []string) []string {
	return precursorEnvWith(withSSHOff(profileEnv(heavy), true), pin)
}

// precursorEnvWith is precursorEnv with the profile given by the caller.
func precursorEnvWith(profile, pin []string) []string {
	env := append(withoutDaemonSSH(listingEnvBase(), profile), profile...)
	env = append(env, pin...)
	return append(env, "LC_ALL=C", "LANGUAGE=C")
}

// sshOffEntry ends a profile that turns off the ssh command of git.
const sshOffEntry = "GIT_SSH_COMMAND=false"

// withSSHOff is profile with sshOffEntry at its end, when off is true and the
// profile holds GIT_NO_LAZY_FETCH=1 and GIT_ASKPASS=. Else it is profile.
// be1c1ca5 puts that entry after GIT_ASKPASS= and before the folder variables
// (measured on Linux). On Windows it ends the environment of such a call there
// (measured on Windows), and Go sorts the block of a child by name (D23). The
// calls with no GIT_ASKPASS= keep the daemon's own GIT_SSH_COMMAND (measured
// on Linux). macOS is not measured: it follows Linux.
func withSSHOff(profile []string, off bool) []string {
	if !off || !slices.Contains(profile, "GIT_NO_LAZY_FETCH=1") || !slices.Contains(profile, "GIT_ASKPASS=") {
		return profile
	}
	return append(slices.Clone(profile), sshOffEntry)
}

// withoutDaemonSSH is daemon without its GIT_SSH_COMMAND entries when profile
// holds sshOffEntry, so that a call has one entry of that name. On Windows the
// name matches in any letter case (claustrum's own, not measured).
func withoutDaemonSSH(daemon, profile []string) []string {
	if !slices.Contains(profile, sshOffEntry) {
		return daemon
	}
	return slices.DeleteFunc(slices.Clone(daemon), func(kv string) bool {
		name := envName(kv)
		if gitEnvHere.windows {
			name = strings.ToUpper(name)
		}
		return name == "GIT_SSH_COMMAND"
	})
}

// hardenedGitEnv builds the environment for a hardened git command: the daemon's
// own environment as a git child gets it (gitChildEnv), without GIT_CONFIG and
// GIT_CONFIG_PARAMETERS, less GIT_CONFIG_COUNT
// and every GIT_CONFIG_KEY_<digits> and GIT_CONFIG_VALUE_<digits>. Then come the
// profile, pin, and configPinEnv: the count, the inherited pairs and the hook pins of
// hooks (gitenv.go). TestHardenedGitEnvOrder tests the order. docs/protocol/git-rules-calls.md →
// "Variables that the git calls keep or drop" gives the rules of the daemon's variables.
//
// GIT_OPTIONAL_LOCKS=0 is not part of it. git.status adds it to its status, ls-files
// and diff-index calls only (gitstatus.go). The heavy `rev-parse --absolute-git-dir` of git.worktree_create and
// git.worktree_remove runs without it.
func hardenedGitEnv(heavy bool, pin, hooks []string) []string {
	return hardenedEnvFrom(gitChildEnv(), heavy, pin, hooks)
}

// hardenedEnvFrom is hardenedGitEnv with daemon as the daemon's environment. The
// light profile's GIT_ALLOW_PROTOCOL still comes from the process environment
// (lightAllowProtocol), not from daemon.
func hardenedEnvFrom(daemon []string, heavy bool, pin, hooks []string) []string {
	return hardenedEnvWith(daemon, withSSHOff(profileEnv(heavy), true), pin, hooks)
}

// hardenedEnvWith is hardenedEnvFrom with the profile part given by the caller.
func hardenedEnvWith(daemon, profile, pin, hooks []string) []string {
	base := dropConfigOverrides(withoutDaemonSSH(daemon, profile))
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
// sshOff adds GIT_SSH_COMMAND=false at the end (withSSHOff, gitUse.sshOff).
func branchStepEnv(pin, hooks []string, sshOff bool) []string {
	var profile []string
	for _, kv := range profileEnv(false) {
		if !strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") {
			profile = append(profile, kv)
		}
	}
	tail := withSSHOff([]string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh", "GIT_ASKPASS="}, sshOff)
	return append(hardenedEnvWith(withoutDaemonSSH(gitChildEnv(), tail), profile, pin, hooks), tail...)
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
// returns it. u gives the profile and the folder variables of the request
// (gitUse): in the state shadow the listing reads the daemon's copy of the
// settings, as the call after it does. The call after it pins off the hook names the listing shows
// (configPinEnv). heavy is the profile of the call that follows. A failure of this
// listing does not refuse the call, unless its time limit stopped it. See
// listingRun.hooks.
//
// The user's global excludes are resolved first. f6010b97 reads them before its
// first listing, measured on Linux and Windows VMs.
func hookPrecursor(ctx context.Context, u gitUse, dir string, heavy bool) listingRun {
	userExcludesFile()
	u.recheck(dir)
	return runListing(ctx, dir, "", u.precursorEnv(heavy, u.pin(dir)))
}

// hardenedGitCmd builds a hardened git command in dir. When pre is nil the config
// precursor runs first, and the call pins the hooks of that listing. pre is the
// listing of hostileConfigRefusal for the first call after it: that check is the
// listing f6010b97 runs before that call, so the call gets no second one. Measured on
// Linux, macOS and Windows VMs.
//
// A call whose own listing runs here starts a git command, so it takes the form
// of the off switch at this moment (gitUse.atCommand).
func hardenedGitCmd(ctx context.Context, u gitUse, dir string, heavy bool, pre *configListing, args ...string) *exec.Cmd {
	var hooks []string
	if pre == nil {
		u = u.atCommand()
		hooks = hookPrecursor(ctx, u, dir, heavy).hooks()
	} else {
		hooks = pre.hooks
	}
	cmd := exec.CommandContext(ctx, "git", withFront(u.front(dir), hardenedProfileArgs(heavy, args...))...)
	cmd.Dir = dir
	// The listing before the call was its build, so a folder of the copy that
	// went since then is not made again here: the start refuses (holdRepoCopy).
	// On 5fd08069 no folder of a request with a normal reply went between a
	// listing and the call after it (E3L cell 54, Linux VM).
	u.noRenew = true
	cmd.Env = u.hardenedGitEnv(heavy, u.pin(dir), hooks)
	return cmd
}

// hardenedGitContext runs a git subcommand under the hardening profile and
// environment, with the config precursor first. It returns stdout only.
func hardenedGitContext(ctx context.Context, u gitUse, dir string, heavy bool, args ...string) (string, bool) {
	return hardenedGitAnswer(ctx, u, dir, heavy, nil, args...)
}

// hardenedGitAnswer runs one hardened call and returns its stdout and whether it
// exited 0. Each caller parses the answer into a value or reads only the exit,
// and no caller puts the text in an error. stderr is not part of the answer.
// With a loader line on stderr, 5fd08069 answers "branch":"main" in git.info and
// creates the worktree (round task004-01, cells M1 and M4, Linux VM).
func hardenedGitAnswer(ctx context.Context, u gitUse, dir string, heavy bool, pre *configListing, args ...string) (string, bool) {
	out, err := gitOutput(ctx, hardenedGitCmd(ctx, u, dir, heavy, pre, args...))
	return strings.TrimRight(string(out), "\n"), err == nil
}

// hardenedGitFirst is hardenedGit for the first call after hostileConfigRefusal on
// the same dir. It runs no config precursor, because that check was its precursor,
// and it pins the hooks of that check's listing l.
func hardenedGitFirst(u gitUse, dir string, heavy bool, l configListing, args ...string) (string, bool) {
	return hardenedGitAnswer(u.stop.context(), u, dir, heavy, &l, args...)
}

// gitDirWorkTreeToplevel runs `git --git-dir=<gitDir> config -z --list`, then the heavy
// `git -c … --git-dir=<gitDir> --work-tree=<workTree> rev-parse --show-toplevel`. Both
// run in gitDir and carry pin. It returns the answer of the second call. 89cb6289
// runs this pair in git.info on Windows (row W01) and in git.worktree_create after a
// mismatched admin record (row I07a, macOS VM).
//
// 5fd08069 sends the six heavy variables on both calls and the 28 items on the
// rev-parse call. With "shadowDirOff":true, cell B1 of s3-linux shows it in
// git.status and in git.worktree_create (Linux VM). Cell G4-off-hook of s3p2-macos
// shows it in the same two methods (macOS VM). Cell B1 of s3-windows shows it in
// those two and in git.info (Windows VM). Cell 50 of s3p2b-edges has no such member
// and shows it in create (macOS and Windows VMs). val-s3p2b1 cell A6 shows it in
// git.worktree_remove, with "shadowDirOff":true (Linux, macOS and Windows VMs).
// 89cb6289 sends the light profile there.
//
// front are the `-c` items that come before the 28 items of the second call
// (gitUse.gitDirFront), or nil. s holds the time limits of the request, or is nil.
// sshOff is gitUse.sshOff of the request.
func gitDirWorkTreeToplevel(s *gitStop, sshOff bool, gitDir, workTree string, pin, front []string) (string, error) {
	return gitDirWorkTreeToplevelIn(s.context(), sshOff, gitDir, workTree, pin, front)
}

// gitDirWorkTreeToplevelIn is gitDirWorkTreeToplevel under ctx, the context of
// the request.
func gitDirWorkTreeToplevelIn(ctx context.Context, sshOff bool, gitDir, workTree string, pin, front []string) (string, error) {
	userExcludesFile()
	profile := withSSHOff(profileEnv(true), sshOff)
	hooks := runListing(ctx, gitDir, gitDir, precursorEnvWith(profile, pin)).hooks()
	cmd := exec.CommandContext(ctx, "git", withFront(front, hardenedProfileArgs(true,
		"--git-dir="+gitDir, "--work-tree="+workTree, "rev-parse", "--show-toplevel"))...)
	cmd.Dir = gitDir
	cmd.Env = hardenedEnvWith(gitChildEnv(), profile, pin, hooks)
	out, err := gitOutput(ctx, cmd)
	return strings.TrimRight(string(out), "\r\n"), err
}

// hardenedGit is hardenedGitContext under the time limits of the request.
func hardenedGit(u gitUse, dir string, heavy bool, args ...string) (string, bool) {
	return hardenedGitContext(u.stop.context(), u, dir, heavy, args...)
}

// statusGitDirTempPrefix and checkoutIndexTempPrefix start the names of the
// temporary git dir of git.status and the temporary index directory of the
// git.worktree_create checkout. Each has the length of the prefix in the f6010b97
// argv and env, measured on Linux, macOS and Windows VMs: 18 and 17 bytes.
// os.MkdirTemp adds a random decimal suffix, as f6010b97 does.
//
// The index prefix is the one of 89cb6289 on Linux and macOS VMs. Cell X1 shows
// it in a frame: "open <tmp>/claude-ssh-index-<n>/index: no such file or
// directory". It is also in the GIT_INDEX_FILE of each read-tree there. The
// constant is the same on Windows, where the name of 89cb6289 is not measured.
// The prefix of git.status is the one of 5fd08069 with "shadowDirOff":true
// (val-s3p2b1-linux row D5, Linux VM). In the state shadow both folders are in
// the folder of the daemon, with other names (gitUse.requestDir).
const (
	statusGitDirTempPrefix  = "claude-ssh-gitdir-"
	checkoutIndexTempPrefix = "claude-ssh-index-"
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
// the read-tree subcommand itself. rule is the filter rule (gitfilter.go), or
// nil. For a create with a settings copy its plan comes from the output of the
// config precursor (filterRule.planFor). With no copy the scoped listing comes
// first (filterRule.scopedPlan), and a refusal of the plan comes right after
// the precursor (claustrum's own, not measured). The -c pairs come before the
// profile, as on 5fd08069 (Linux and Windows VMs).
//
// In the state shadow (u) the call differs in three points, as on 5fd08069
// without "shadowDirOff". The rows are N3 and N4 of val-s3p2b1-linux with its
// cell A3-create-plain (Linux VM), N2 and N3 of val-s3p2b1-macos, and N3 to N5
// of val-s3p2b1-windows.
//
//   - A second listing runs right before the read-tree call. It has the argv of
//     the first one, and the variables of the read-tree call: the hook pins,
//     GIT_INDEX_FILE and LANGUAGE=C, and no added LC_ALL. claustrum reads its
//     output to the end and keeps none of it. A failure of it refuses the create (ownConfigListing). Then
//     come the cap of the filter rule (tooManyFiltersText) and the other
//     refusals of the plan. 5fd08069 has that order (round task008-03, cells R3
//     to R5, Linux VM).
//   - The read-tree call has the item lfs.storage=<git directory>/lfs before the
//     list of the profile. The pairs of the filter rule come before that item
//     (s3p2-linux2 cells G2 and L1, Linux VM). For a baseRepo that is a linked
//     worktree the value is not measured: claustrum names the git directory of
//     the repository. A user config that sets lfs.storage removes the item
//     (round task008-01, cell C12, Linux, macOS and Windows VMs).
//   - The read-tree call has LANGUAGE=C as its last variable.
//
// Both calls keep the four values of the light profile in that state.
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
// With no timeoutMs the drain ends gitDrainWait after git exited, and the
// descendant gets no signal. On 5fd08069 the reply comes 2.0 s after the
// read-tree call exited, and a child that holds its stderr runs on (cell T16 of
// round task010-01, Linux VM). The read-tree call itself has no time limit
// (cell T17, Linux and macOS VMs).
//
// No call runs after a stopped call of the request (gitRun). The first listing
// runs under the context of the request, not under ctx: the timeoutMs of the
// caller does not end it. On 5fd08069 a listing held for 10 s with a timeoutMs of
// 3000 runs to its end, and then the timeout frame comes (cell T12, Linux VM).
// checkoutListedSwap is gitUse.listedSwap. It is a variable so that a test on
// every system can drive the two answers of the Windows test.
var checkoutListedSwap = gitUse.listedSwap

func hardenedGitCheckout(ctx context.Context, u gitUse, leaf, gitDir, indexFile string, pin []string, rule *filterRule, args ...string) (stderr string, drained bool, err error) {
	if err := u.stop.halted(); err != nil {
		return "", false, err
	}
	if rule != nil && !rule.shadow {
		rule.scopedPlan(ctx, leaf, gitDir, pin)
	}
	pre := runListing(u.stop.context(), leaf, gitDir, precursorEnv(false, pin))
	// A listing that its limit stopped fails the checkout with the hooks
	// refusal, and the rollback of a failed checkout runs (cell TA of round
	// task010-02, Linux VM).
	if err := u.stop.halted(); err != nil {
		return "", false, err
	}
	// With a settings copy a listing that fails refuses the checkout, after one
	// `git version` (cannotRunText). No second listing
	// and no read-tree call follow, and the rollback of a failed checkout runs.
	// 5fd08069 answers so (cell L6 of round task084-02, Linux VM, and cell Q4bp,
	// macOS VM). If that `git version` fails too, its text replaces the hooks
	// refusal (cell E13 of round task083-01, Linux VM). Without a copy the
	// checkout goes on as before (not measured).
	//
	// A listing whose repository folder went refuses with its own text, and
	// no `git version` runs (from the code, not measured).
	if u.shadow() && copyGone(pre.err) {
		return "", false, checkoutRefusal(pre.err.Error())
	}
	if u.shadow() && pre.err != nil {
		if whole := cannotRunText(); whole != "" {
			return "", false, checkoutRefusal(whole)
		}
		return "", false, checkoutRefusal(hooksRefusalPrefix + pre.detail())
	}
	// On Windows the path of the git directory must still name the held folder
	// after the first listing (errGitDirChanged). A regular file at the path
	// goes on to the second listing (gitUse.listedSwap).
	fileSwap, changed := checkoutListedSwap(u)
	if changed {
		return "", false, errGitDirChanged
	}
	var filters filterPlan
	if rule != nil {
		if rule.shadow {
			rule.planFor(pre.out)
		}
		filters = rule.plan
		if !rule.shadow && filters.refusal != "" {
			return "", false, checkoutRefusal(filters.refusal)
		}
	}
	hooks := pre.hooks()
	env := append(hardenedGitEnv(false, pin, hooks), "GIT_INDEX_FILE="+indexFile)
	list := slices.Clone(filters.args)
	if u.shadow() {
		// The second listing, and the two additions of the read-tree call (the
		// comment above).
		env = append(env, "LANGUAGE=C")
		if text := ownConfigListing(leaf, gitDir, env); text != "" {
			return "", false, checkoutRefusal(text)
		}
		if fileSwap {
			return "", false, errGitDirChanged
		}
		if filters.tooManyFilters {
			return "", false, checkoutRefusal(tooManyFiltersText)
		}
		if filters.refusal != "" {
			return "", false, checkoutRefusal(filters.refusal)
		}
		if !filters.userLfsStorage {
			list = append(list, "-c", "lfs.storage="+filepath.Join(u.gitDir, "lfs"))
		}
		// The items of the `config.worktree` file come after that item and
		// before the list of the profile (gitUse.checkoutFront).
		list = append(list, u.checkoutFront(gitDir)...)
	}
	cmd := exec.CommandContext(ctx, "git", append(list, hardenedProfileArgs(false, args...)...)...)
	cmd.Dir = leaf
	cmd.Env = env
	// Own process group so the teardown below can SIGKILL the whole group and reap a
	// descendant the checkout left holding the pipe — reproducing 4534d86's observed
	// descendant reap.
	cmd.SysProcAttr = newSysProcAttr()
	_, deadlined := ctx.Deadline()
	if !deadlined {
		cmd.WaitDelay = gitDrainWait
	} else {
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
	err = runHeld(cmd, cmd.Run)
	drained = errors.Is(err, exec.ErrWaitDelay)
	if drained && deadlined {
		// git exited 0 but a descendant held the pipe past the cap. WaitDelay closed
		// the daemon's own pipe ends but killed nothing; SIGKILL the group to reap the
		// descendant, matching the reference's observed descendant reap.
		reapProcessGroup(cmd.Process)
	}
	return errBuf.String(), drained, err
}

// checkoutRefusal is a refusal of the checkout before the read-tree call. Its
// text follows `git worktree add failed (checkout): ` in the frame, and the
// rollback is that of a failed checkout.
type checkoutRefusal string

func (r checkoutRefusal) Error() string { return string(r) }

// ownConfigBound ends the second listing of the checkout. On 5fd08069 a listing
// held for 31 s ended at about 30 s, and one held for 29 s passed. Those are
// cells C11 and C11b of round task008-01 (Linux and macOS VMs). On Windows
// cells A4, A4h35 and A4x of round task111-08 are equal on both builds. It is
// a variable so that a test can shrink it.
var ownConfigBound = 30 * time.Second

// ownConfigStopText is the cause of a second listing that ownConfigBound ended
// (cell C11, Linux and macOS VMs).
const ownConfigStopText = "git took too long: Claude stopped git config after 30 seconds; try again"

// ownConfigListing runs the second listing of the checkout under ownConfigBound
// and returns the refusal text of a listing that failed, or "". A listing that
// exits non-zero gives its exec error as the cause, for example `exit status 1`
// (cell C10, Linux, macOS and Windows VMs). The stderr of git is not in the
// cause. That and the other kinds of failure are claustrum's own.
//
// The `timeoutMs` of the caller does not end the listing. With `timeoutMs`
// 2000 and a listing held 5 s, 5fd08069 sends no signal, the listing ends by
// itself, and then the cap or the timeout arm answers (round task008-05, cells
// P1 and P2, Linux VM). A listing that fails after the deadline of the caller
// gives this text too (not measured).
//
// The listing holds the repository folder of the copy (runHeld). A listing
// whose folder went gives its own text alone (from the code, not measured).
//
// The daemon reads the output of the listing to its end, so the read-tree call
// starts only when the pipe closes. A child that holds it gets no signal
// (cell A4 of round task101-01, Linux VM). git is reaped at the end of that
// pipe (runToEOF). Both builds match in cell A4 of round task111-03 (Linux VM).
//
// ownConfigBound ends only a git that still runs. A git that exited before it
// counts by its exit status, however long a child holds stdout after it.
// 5fd08069 then runs the read-tree call and answers success (cell A4-35 of
// round task111-03, Linux VM, 3 of 3 runs). A git that exits non-zero there
// gives its exit status, for example `exit status 1` (claustrum's own, not
// measured).
//
// A git that still runs at the bound gets the stop of stopThenKill: SIGTERM to
// its pid, and SIGKILL gitStopGrace later. 5fd08069 does so in cell A4x of
// round task111-05 (Linux VM, 3 of 3 runs) and of round task111-07 (macOS
// VM), with the end about 2 s after the SIGTERM. An strace of round
// task111-06 shows the SIGKILL 2.002 s after the SIGTERM (Linux VM). On Windows git ends at once
// (cell A4x of round task111-08, Windows VM).
//
// The bound decides only the signals. The answer comes from how git ended. An
// exit 0 is a success, also after the SIGTERM (cell A4x-t of rounds task111-06
// and task111-10, Linux and macOS VMs). A git that the signal ended gives
// ownConfigStopText. So does a git that exits 1 at the SIGTERM, on both builds
// (cell A4x-x1 of round task111-09, Linux VM).
func ownConfigListing(leaf, gitDir string, env []string) string {
	cmd := exec.Command("git", "--git-dir="+gitDir, "config", "-z", "--list")
	cmd.Dir = leaf
	cmd.Env = env
	cmd.Stdout = io.Discard
	var mu sync.Mutex
	done, stopped := false, false
	var bound, kill *time.Timer
	err := runHeld(cmd, func() error {
		return runToEOF(cmd, gitDrainWait, func() {
			bound = time.AfterFunc(ownConfigBound, func() {
				mu.Lock()
				defer mu.Unlock()
				if done || !gitStillRuns(cmd.Process) {
					return
				}
				var stopErr error
				kill, stopErr = stopThenKill(cmd.Process, &mu, &done)
				stopped = stopErr == nil
			})
		})
	})
	mu.Lock()
	done = true
	if bound != nil {
		bound.Stop()
	}
	if kill != nil {
		kill.Stop()
	}
	wasStopped := stopped
	mu.Unlock()
	cause := ""
	switch {
	case err == nil:
		return ""
	case copyGone(err):
		return err.Error()
	case wasStopped:
		cause = ownConfigStopText
	default:
		cause = err.Error()
	}
	return "Claude could not read your own git config on the remote machine (" + cause +
		"). Run git config --list there to see the problem."
}

// Two errors of the checkout of a create, for a git directory that is no
// longer the folder that the create holds. The Windows rows are of 5fd08069
// with a junction at `repo\.git` (table of section 2 of val-s3p2b2-12-windows,
// both forms of T, Windows VM, 3 of 3 runs). In both rows the leaf is gone at
// the reply, and the registration and the branch stay. claustrum answers both
// texts there (table X1 of val-s3p2b2-13-windows, points 10 and 12, Windows
// VM, 2 runs for each form).
//
//   - errGitDirChanged, on Windows: the junction was put there while the first
//     listing of the checkout ran (the call 11 of that build). No other call
//     follows. The text is `git worktree add failed (checkout): this
//     repository's git config cannot be used: …; and the undo could not finish
//     for <leaf>: the worktree registration and the branch remain; …`.
//   - errGitDirNotReal, on every system: the link was put there after the
//     read-tree call ended (the point 13 of that build). The registration gets
//     no index. The text is `git worktree add failed (checkout): ".git" is a
//     link or a file where Claude needs a real directory; …` with the same
//     undo text. With a symbolic link, 5fd08069 answers the same text and end
//     state and makes no git call after the read-tree call (cells swp-copy and
//     swp-marker of val-s3p2b2-13-linux2, Linux VM, 6 of 6 runs. Cells p-copy
//     and p-marker of val-s3p2b2-13-macos, macOS VM, 11 of 11 runs).
//
// A regular file at `repo\.git` while the first listing runs does not give
// errGitDirChanged there. 5fd08069 then runs the second listing, which fails
// with exit 128. It answers the text of ownConfigListing with the undo text
// above (cell X2 of round task077-03, Windows VM, 2 of 2 runs). A second
// listing that passes there gives errGitDirChanged, so no read-tree call runs
// (claustrum's own, not measured).
//
// A junction that is put there while the second listing runs gets no test:
// the read-tree call then runs and fails by itself (points 11 and 12). A
// second listing that fails there now gives the text of ownConfigListing (not
// measured).
// Linux and macOS are not measured at the first step and have no test there.
var (
	errGitDirChanged = errors.New("the git directory changed after the first listing of the checkout")
	errGitDirNotReal = errors.New("the git directory changed after the read-tree call")
)

// runWorktreeCheckout is the read-tree checkout of git.worktree_create. It reads rev
// into a new index in a fresh temporary directory, fills leaf from it, and, when
// git exits 0, places that index in adminDir, the new worktree's registration
// (guardedInstallWorktreeIndex). The caller gets adminDir from createdRegistrationRefusal,
// or from absoluteAdminDir if rev-parse gave no answer. tested is the registration
// that the tests after the add accepted, or the zero value. The temporary directory is removed afterwards. stderr,
// drained and err are those of hardenedGitCheckout. The -c pins
// core.splitIndex=false and core.commitGraph=false follow the profile, as in the
// argv measured against f6010b97. workTree is the --work-tree value
// (checkoutWorkTree). The working directory stays leaf. rule is the filter
// rule, or nil (hardenedGitCheckout).
//
// installErr is the error of a placement that failed. The caller answers it as a
// failed checkout. On Windows it is nil unless the staging step moved the
// registration (installTestedWorktreeIndex).
func runWorktreeCheckout(ctx context.Context, u gitUse, leaf, workTree, gitDir, adminDir string, tested testedRegistration, rev string, pin []string, rule *filterRule) (stderr string, drained bool, err, installErr error) {
	idxDir, removeIdxDir, err := u.requestDir(shadowIndexDirPrefix, checkoutIndexTempPrefix)
	if err != nil {
		return "", false, err, nil
	}
	// In the folder of the daemon the index folder stays until the reply of the
	// create (request.runAtReply). In the temporary folder the moment is not
	// measured: it goes at the end of this step, as before.
	if u.inFolder() && u.atReply != nil {
		*u.atReply = append(*u.atReply, removeIdxDir)
	} else {
		defer removeIdxDir()
	}
	idx := filepath.Join(idxDir, "index")
	stderr, drained, err = hardenedGitCheckout(ctx, u, leaf, gitDir, idx, pin, rule,
		"-c", "core.splitIndex=false", "-c", "core.commitGraph=false",
		"--git-dir="+gitDir, "--work-tree="+workTree,
		"read-tree", "-u", "--reset", "--no-recurse-submodules", rev)
	readEnd := time.Now()
	if err == nil || drained {
		if u.swapped("index") {
			return stderr, drained, errGitDirNotReal, nil
		}
		// The settle step runs in the state shadow after a checkout that exited 0
		// in time. With no settings copy and after a drain overrun no call runs
		// (claustrum's own, not measured).
		if err == nil && u.shadow() {
			settleCheckout(ctx, u, leaf, workTree, gitDir, idx, pin, readEnd)
			// The test of the held git directory runs again right before the
			// placement, so a swap during the settle step gets the same answer
			// (claustrum's own, not measured).
			if u.swapped("settled") {
				return stderr, drained, errGitDirNotReal, nil
			}
		}
		if u.accepted != nil && tested.path == "" {
			// A create that holds its git directory, with no registration that
			// the tests after the add accepted (Windows): the index goes
			// through the held folder, and no error of that step is reported.
			u.accepted.installIndex(idx, adminDir)
		} else {
			installErr = guardedInstallWorktreeIndex(idx, adminDir, leaf, tested)
		}
	}
	return stderr, drained, err, installErr
}

// guardedInstallWorktreeIndex is installWorktreeIndex behind a guard. The guard has
// two forms.
//
// With tested set, the tests after the add accepted the registration (Linux and
// macOS, when git answered `rev-parse --absolute-git-dir`), or the staging step
// moved it (Windows). The index then goes
// into that registration with no read of its gitdir record. 89cb6289 places the
// index too when the record is gone: in
// cell B4 (Linux and macOS VMs) the record is removed after the read-tree: 89cb6289 answers
// success, and the registration holds the index. In cell Z15 (Linux and macOS VMs)
// the record is removed and the registration gets mode 0500: 89cb6289 answers
// "openat w1/index: permission denied". One state places nothing: the folder at
// the path is no longer the folder that the tests accepted (replacedIn, D24). The
// placement tests that on the registrations folder that it opened
// (installTestedWorktreeIndex). The
// error text is claustrum's own, and the caller answers it as a failed checkout.
// No cell measured a replaced folder at the placement.
//
// With tested not set, the tests did not run. On Linux and macOS the
// index then goes into the registration adminDir only if its gitdir record can be read
// and names leaf (readAdminRecord). In every other state of a registration that
// can be reached, nothing is placed, and an index that is there keeps its bytes: a
// record that is missing, a FIFO or a folder, an empty or a relative record of
// another worktree, and the record of another path. The error text is claustrum's own (not measured). The path in it is
// adminDir, the registration folder. That is the path that the .git file of the
// leaf names, not the worktreePath of the request. On Windows no guard runs.
//
// The frame joins each text to the stderr of the checkout under the 512-byte rule
// (indexInstallText). After a long stderr the frame holds a part of the text, or
// none of it.
//
// One state is apart in both forms: a registration folder whose stat fails. No index of it can be
// reached then, and the placement runs and fails by itself. Its error is the measured
// text of a registration that is gone (cell Z10) or of a registrations directory
// without the search permission (cells Z11a and Z11b, macOS VM).
func guardedInstallWorktreeIndex(idx, adminDir, leaf string, tested testedRegistration) error {
	if tested.path != "" {
		return installTestedWorktreeIndex(idx, tested)
	}
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

// worktreeBaseGitDir is the git directory of repo for git.worktree_create. answered
// reports that `rev-parse --absolute-git-dir` gave it. If git gave no answer, the
// directory is that of repoGitDir, which need not exist: a subfolder of a
// repository has no .git entry. err is the exec error of a call that failed. The
// answer is stdout only, as for hardenedGitAnswer (claustrum's own).
func worktreeBaseGitDir(u gitUse, repo string) (gitDir string, answered bool, err error) {
	ctx := u.stop.context()
	out, err := gitOutput(ctx, hardenedGitCmd(ctx, u, repo, true, nil, "rev-parse", "--absolute-git-dir"))
	if d := strings.TrimRight(string(out), "\n"); err == nil && d != "" {
		return filepath.Clean(filepath.FromSlash(d)), true, nil
	}
	return repoGitDir(repo), false, err
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
func hardenedGitStdout(u gitUse, dir string, heavy bool, args ...string) (string, error) {
	return hardenedGitRun(u, dir, heavy, nil, args...)
}

// hardenedGitStdin is hardenedGitStdout on the light profile, with stdin fed to
// git. The .worktreeinclude scan uses it for `git check-ignore --stdin`.
func hardenedGitStdin(u gitUse, dir, stdin string, args ...string) (string, error) {
	return hardenedGitRun(u, dir, false, strings.NewReader(stdin), args...)
}

func hardenedGitRun(u gitUse, dir string, heavy bool, stdin io.Reader, args ...string) (string, error) {
	return hardenedGitRunPre(u, dir, heavy, nil, stdin, args...)
}

// hardenedGitRunPre is hardenedGitRun with the precursor choice of hardenedGitCmd.
func hardenedGitRunPre(u gitUse, dir string, heavy bool, pre *configListing, stdin io.Reader, args ...string) (string, error) {
	return hardenedGitRunIn(u.stop.context(), u, dir, heavy, pre, stdin, args...)
}

// hardenedGitRunIn is hardenedGitRunPre under ctx, which gives the time limit of
// the call (withGitLimit).
func hardenedGitRunIn(ctx context.Context, u gitUse, dir string, heavy bool, pre *configListing, stdin io.Reader, args ...string) (string, error) {
	cmd := hardenedGitCmd(ctx, u, dir, heavy, pre, args...)
	cmd.Stdin = stdin
	out, err := gitOutput(ctx, cmd)
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
	// attrSourceMu guards the two values below. A probe that ends in "unknown"
	// stores nothing, so the next use runs it again.
	attrSourceMu   sync.Mutex
	attrSourceDone bool
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
// git added --attr-source in 2.40, so support is probed with a
// guard — `git --attr-source=<empty> version`, which errors on older git. The option
// is a top-level one, the first argument of the status call, as f6010b97 emits it.
//
// The probe has the filtered environment of the daemon (gitChildEnv) and adds no
// variable. f6010b97 adds none to it on Linux and Windows VMs. It runs in
// gitVersionDir. It keeps "supported" and "not supported" for the life of the
// daemon, and "unknown" not (claustrum's own, not measured). See probeAttrSource.
func attrSourceArgs() []string {
	attrSourceMu.Lock()
	defer attrSourceMu.Unlock()
	if !attrSourceDone {
		ok, known := probeAttrSource()
		if !known {
			return nil
		}
		attrSourceDone, attrSourceOK = true, ok
	}
	if attrSourceOK {
		return []string{"--attr-source=" + gitEmptyTree}
	}
	return nil
}

// probeAttrSource runs the probe. Exit 0 is "supported". A run that exits with a
// code other than 0 runs `git version` (gitVersionPlain), and exit 0 there is "not
// supported". Every other failure is "unknown": known is false. That covers a run
// that did not start, a run that a signal ended and a failed `git version`.
func probeAttrSource() (supported, known bool) {
	err := gitOutsideCmd(nil, "--attr-source="+gitEmptyTree, "version").Run()
	if err == nil {
		return true, true
	}
	if !exitedWithCode(err) {
		return false, false
	}
	return false, gitVersionPlain().Run() == nil
}

var (
	// A mutex rather than a sync.Once, for two reasons. A test has to force a
	// re-resolve of the configured value, and clearing a Once means either
	// copying a lock (go vet copylocks) or writing the pair with no
	// synchronisation at all, which races any in-flight request goroutine.
	// The read itself runs outside the mutex. userExcludesGen counts the resets of
	// the test seam, so a read that began before a reset stores nothing.
	userExcludesMu     sync.Mutex
	userExcludesDone   bool
	userExcludesCached string
	userExcludesGen    int
)

// userExcludesFile resolves the user's global git excludes file: the configured
// core.excludesFile if it is an absolute path, else $XDG_CONFIG_HOME/git/ignore or
// $HOME/.config/git/ignore when that file exists, else the null device (os.DevNull:
// /dev/null, and NUL on Windows, as f6010b97 passes on a Windows VM). This is the
// excludes path 7d193f89 passes as core.excludesFile on every git invocation (observed
// via a git-argv trace). hardenedProfileArgs substitutes it for every git op.
//
// A call that finds a stored result uses it. A call that finds none runs its own
// read, also while the read of another request runs, and stores its result when
// the read ends. 5fd08069 runs two reads for two git requests sent together, and
// one read for requests 200 to 500 ms apart (rounds task103-01 to task103-03,
// Linux VM). Each call uses the result of its own read. A later result replaces
// an earlier one (claustrum's own, not measured).
func userExcludesFile() string {
	userExcludesMu.Lock()
	if userExcludesDone {
		v := userExcludesCached
		userExcludesMu.Unlock()
		if v != "" {
			return v
		}
		return defaultExcludesFile()
	}
	gen := userExcludesGen
	userExcludesMu.Unlock()
	v := configuredExcludesFile()
	userExcludesMu.Lock()
	if gen == userExcludesGen {
		userExcludesDone, userExcludesCached = true, v
	}
	userExcludesMu.Unlock()
	if v != "" {
		return v
	}
	return defaultExcludesFile()
}

// configuredExcludesFile reads the configured core.excludesFile of the user: an
// absolute value, or "" when there is none. The read has the filtered environment
// of the daemon (gitChildEnv) and GIT_DIR=<null device>, and runs in gitVersionDir
// (claustrum's own, not measured). f6010b97 adds GIT_DIR=NUL on a Windows VM and
// runs it in the temporary directory on Linux and Windows VMs.
//
// `config --includes --path` follows includes and expands a leading ~, matching the
// reference. It runs WITHOUT the hardening profile so git reports the real global
// setting, not the pin. The read is kept for the daemon, so it has no time limit of
// a request. GIT_DIR=<null device> makes it read only the user's global and system
// config, never a repo's own core.excludesFile. A repo must not be able to inject
// excludes. 683cc6a9 makes one read too and looks up the default file on each
// git.status call (measured on Linux and macOS).
func configuredExcludesFile() string {
	windows := gitEnvHere.windows
	cmd := gitOutsideCmd([]string{"GIT_DIR=" + os.DevNull}, "config", "--includes", "--path", "core.excludesFile")
	if out, err := cmd.Output(); err == nil {
		if p := strings.TrimSpace(string(out)); absoluteFor(p, windows) {
			return p
		}
	}
	return ""
}

// defaultExcludesFile looks up the default excludes file on each call. A regular
// file at <base>/git/ignore is the result, else the null device. A file that went
// or became a folder gives the null device. The lookup with XDG_CONFIG_HOME set is
// not measured.
func defaultExcludesFile() string {
	windows := gitEnvHere.windows
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || relativeFor(base, windows) {
		base = ""
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".config")
		}
	}
	if base == "" || relativeFor(base, windows) {
		return os.DevNull
	}
	if fi, err := os.Stat(filepath.Join(base, "git", "ignore")); err == nil && fi.Mode().IsRegular() {
		return filepath.Join(base, "git", "ignore")
	}
	return os.DevNull
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
	// gitFileText is set beside refusal when the listing failed with the text of git
	// 2.56 for a `.git` file that names no valid repository (saysGitFileNowhere). It
	// is the error of a failed call about the repository, with git's lines.
	// git.worktree_remove answers it in the cells GF and N04 of round tier0-27, as
	// 5fd08069 does (Linux VM). The four other git methods answer it for a gone git
	// dir that is no worktree entry (noRepoCalls, statusGitFileNowhere). With
	// "shadowDirOff":true each of the five answers refusal there (round task039-02 part
	// b for the four, cells GFOFF and N04OFF of round task039-03 for the remove).
	gitFileText string
	// listing is the listing of a check that passed. The first hardened call after the
	// check pins its hooks (hardenedGitFirst).
	listing configListing
	// gone marks a refusal of a listing whose repository folder went (copyGone).
	// refusal then has its text alone.
	gone bool
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
//  2. The listing exits 128, names no git dir, and the daemon's own search finds no
//     usable git dir: "no repository". 89cb6289 tested the text "fatal: not a git
//     repository" instead. See listingRun.saysNoRepository.
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
// In a request with time limits the listing ends at configListingLimit. On
// 5fd08069 a listing held for 35 s gets SIGTERM at 30 s, and the method refuses
// with the hooks refusal and the stop text. One held for 25 s passes (cells T10
// and T11 of round task010-01, Linux and macOS VMs, and cell T10w on a Windows
// VM).
//
// The check of the daemon's own GIT_CONFIG_COUNT (daemonCountRefusal) comes
// first. When it refuses, no listing runs, and a directory is refused with its text.
// A dir that does not exist, or is not a directory, is not refused by that check. See
// docs/protocol/git-rules-calls.md, "The daemon's own git environment".
func hostileConfigRefusal(u gitUse, dir string, heavy bool) configCheck {
	if msg, bad := daemonCountRefusal(); bad {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return configCheck{}
		}
		return configCheck{refusal: msg}
	}
	return listingCheck(u, plainListing(u, dir, heavy), dir, heavy)
}

// plainListing runs the listing of hostileConfigRefusal in dir.
func plainListing(u gitUse, dir string, heavy bool) listingRun {
	return runListing(u.stop.context(), dir, "", u.precursorEnv(heavy, u.pin(dir)))
}

// listingCheck reads the listing r of hostileConfigRefusal, after the count check.
func listingCheck(u gitUse, r listingRun, dir string, heavy bool) configCheck {
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
	return failedListingCheck(u, r, dir, heavy)
}

// failedListingCheck sorts the failed listing r of dir into its class: outcomes 1 to
// 3 of hostileConfigRefusal. heavy is the profile of the listing.
func failedListingCheck(u gitUse, r listingRun, dir string, heavy bool) configCheck {
	if copyGone(r.err) {
		return configCheck{refusal: r.err.Error(), gone: true}
	}
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
	// (for example an `include.path` with no value) is refused here.
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
		if v := gitVersionRun(u, heavy); v.err != nil {
			if _, err := os.Stat(dir); err == nil {
				return configCheck{refusal: gitCannotRunPrefix + v.detail()}
			}
		}
		return configCheck{}
	}
	c := configCheck{refusal: failedListingText(u, r, heavy)}
	if r.saysGitFileNowhere() {
		// The text names the folder that holds `.git`, as askGit does, also for a
		// subfolder (cell c18 of rounds task039-01 and task039-02, Linux and Windows
		// VMs). A link spelling is not measured.
		root, _ := gitWalkRoot(dir)
		if root == "" {
			root = dir
		}
		c.gitFileText = repoCheckFailedText(root, shadowRun{stderr: r.stderr, err: r.err}.allLines())
	}
	return c
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
func noRepositoryAt(u gitUse, dir string, l configListing) bool {
	return repositoryCheckError(u, dir, &l) != nil
}

// repositoryCheckError is noRepositoryAt with the exec error of the failed call, for
// example "exit status 128". It answers nil where noRepositoryAt answers false.
//
// The call runs on the heavy profile, as f6010b97 runs it on Linux, macOS and
// Windows VMs. pre is the listing of hostileConfigRefusal when this is the first call
// after it on dir, else nil (see hardenedGitCmd).
func repositoryCheckError(u gitUse, dir string, pre *configListing) error {
	_, err := repositoryGitDir(u, dir, pre)
	return err
}

// repositoryGitDir is repositoryCheckError with the answer of the call: the git
// directory that git names for dir. The answer is "" where repositoryCheckError
// answers nil without a call, and after a failed call.
func repositoryGitDir(u gitUse, dir string, pre *configListing) (string, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", nil
	}
	out, err := hardenedGitRunPre(u, dir, true, pre, nil, "rev-parse", "--absolute-git-dir")
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
