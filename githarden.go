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

// hookPinEnv is the always-present config-hook pinning the reference injects via
// GIT_CONFIG_* on every hardened command: hook.enabled=false and an empty
// hook.event. A repo that defines its own hooks in config would need further pins
// (not yet reproduced); the two base pins are constant.
func hookPinEnv() []string {
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=hook.enabled",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=hook.event",
		"GIT_CONFIG_VALUE_1=",
	}
}

// profileEnv is the part of the environment that the profile decides. The light
// profile sets the protocol gate, terminal prompt suppression, and turns off
// replace objects and grafts. The heavy profile turns off lazy fetch, forbids every
// protocol, and clears the askpass helper.
//
// The light profile turns off replace objects and grafts
// (GIT_NO_REPLACE_OBJECTS=1, GIT_GRAFT_FILE=<null device>). git.worktree_create runs
// every git step under the light profile, except the heavy rev-parse
// --absolute-git-dir. Its checkout thus uses the real blob and
// the real commit, even when refs/replace or info/grafts name others. git.status runs
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
		"GIT_ALLOW_PROTOCOL=https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull,
	}
}

// precursorEnv is the environment of the configuration listing that runs before a
// hardened call: the daemon's own environment, the profile of the call that follows
// it, and pin. It carries no hook pins. f6010b97 sets exactly these on its listing,
// the heavy set before a heavy call and the light set before a light call. Measured
// on Linux, macOS and Windows VMs.
//
// pin is the GIT_COMMON_DIR pin of the git-directory trust check (commonDirPinEnv),
// or nil.
func precursorEnv(heavy bool, pin []string) []string {
	env := append(os.Environ(), profileEnv(heavy)...)
	return append(env, pin...)
}

// hardenedGitEnv builds the environment for a hardened git command: precursorEnv,
// then the hook pins.
//
// GIT_OPTIONAL_LOCKS=0 is not part of it. hardenedGitStatus adds it to the status
// call only, as f6010b97 does. The heavy `rev-parse --absolute-git-dir` of
// git.worktree_create and git.worktree_remove runs without it on the same VMs.
func hardenedGitEnv(heavy bool, pin []string) []string {
	return append(precursorEnv(heavy, pin), hookPinEnv()...)
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

// hookPrecursor runs the config-enumeration the reference issues before every
// hardened command (`git config -z --list --name-only`, with dir as its working
// directory) to discover config-defined hooks. Its result does not change the
// pinning for a repo with no hook config — the two base pins in hookPinEnv cover
// that — but the call is part of the reference's process trace, so it is reproduced.
// heavy is the profile of the call that follows. Best-effort: a failure leaves the
// base pins in place.
//
// The user's global excludes are resolved first. f6010b97 reads them before its
// first listing, measured on Linux and Windows VMs.
func hookPrecursor(ctx context.Context, dir string, heavy bool) {
	userExcludesFile()
	cmd := exec.CommandContext(ctx, "git", "config", "-z", "--list", "--name-only")
	cmd.Dir = dir
	cmd.Env = precursorEnv(heavy, commonDirPinEnv(dir))
	_, _ = cmd.Output()
}

// hardenedGitCmd builds a hardened git command in dir, with the config precursor
// first when precursor is true. It is false for the first call after
// hostileConfigRefusal: that check is the listing f6010b97 runs before that call, so
// the call gets no second one. Measured on Linux, macOS and Windows VMs.
func hardenedGitCmd(ctx context.Context, dir string, heavy, precursor bool, args ...string) *exec.Cmd {
	if precursor {
		hookPrecursor(ctx, dir, heavy)
	}
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(heavy, args...)...)
	cmd.Dir = dir
	cmd.Env = hardenedGitEnv(heavy, commonDirPinEnv(dir))
	return cmd
}

// hardenedGitContext runs a git subcommand under the hardening profile and
// environment, with the config precursor first. Combined output, like git().
func hardenedGitContext(ctx context.Context, dir string, heavy bool, args ...string) (string, bool) {
	return hardenedGitCombined(ctx, dir, heavy, true, args...)
}

func hardenedGitCombined(ctx context.Context, dir string, heavy, precursor bool, args ...string) (string, bool) {
	out, err := hardenedGitCmd(ctx, dir, heavy, precursor, args...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// hardenedGitFirst is hardenedGit for the first call after hostileConfigRefusal on
// the same dir. It runs no config precursor, because that check was its precursor.
func hardenedGitFirst(dir string, heavy bool, args ...string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	return hardenedGitCombined(ctx, dir, heavy, false, args...)
}

// hardenedGitStderr is hardenedGitContext for a call whose failure text goes on the
// wire: it returns stderr only, untrimmed, and the exec error. stdout is not kept,
// because the failure frames of git.worktree_create quote stderr only (measured
// against f6010b97 and 90fca6e6 on a macOS VM). See worktreeGitText.
func hardenedGitStderr(ctx context.Context, dir string, heavy bool, args ...string) (string, error) {
	cmd := hardenedGitCmd(ctx, dir, heavy, true, args...)
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
// The config precursor runs the same way: `--git-dir=<gitDir> config -z --list
// --name-only` with the leaf as its working directory, and the light precursorEnv.
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
//     NEVER to "during the checkout";
//   - err: the underlying exec error — git's own "signal: killed" *ExitError when the
//     deadline killed a still-running git, exec.ErrWaitDelay on a drain overrun, or a
//     non-zero *ExitError otherwise. nil when git exited 0.
//
// With no deadline armed (D5 off and timeoutMs off) WaitDelay is left unset, so the
// drain is unbounded and byte-identical to the reference default, which applies no cap.
func hardenedGitCheckout(ctx context.Context, leaf, gitDir, indexFile string, pin []string, args ...string) (stderr string, drained bool, err error) {
	pre := exec.CommandContext(ctx, "git", "--git-dir="+gitDir, "config", "-z", "--list", "--name-only")
	pre.Dir = leaf
	pre.Env = precursorEnv(false, pin)
	_, _ = pre.Output()
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(false, args...)...)
	cmd.Dir = leaf
	cmd.Env = append(hardenedGitEnv(false, pin), "GIT_INDEX_FILE="+indexFile)
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
// git exits 0, moves that index into adminDir, the new worktree's registration. The
// temporary directory is removed afterwards. The results are those of
// hardenedGitCheckout. The -c pins core.splitIndex=false and core.commitGraph=false
// follow the profile, as in the argv measured against f6010b97.
//
// Moving the index is best-effort: a real read-tree that exits 0 has written it. If
// the move fails, the worktree has no index, as after `worktree add --no-checkout`.
func runWorktreeCheckout(ctx context.Context, leaf, gitDir, adminDir, rev string, pin []string) (stderr string, drained bool, err error) {
	idxDir, err := os.MkdirTemp("", checkoutIndexTempPrefix)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = os.RemoveAll(idxDir) }()
	idx := filepath.Join(idxDir, "index")
	stderr, drained, err = hardenedGitCheckout(ctx, leaf, gitDir, idx, pin,
		"-c", "core.splitIndex=false", "-c", "core.commitGraph=false",
		"--git-dir="+gitDir, "--work-tree="+leaf,
		"read-tree", "-u", "--reset", "--no-recurse-submodules", rev)
	if err == nil || drained {
		if !filepath.IsAbs(adminDir) {
			adminDir = filepath.Join(leaf, adminDir)
		}
		installWorktreeIndex(idx, filepath.Join(adminDir, "index"))
	}
	return stderr, drained, err
}

// installWorktreeIndex moves the index file src to dst. A rename fails across file
// systems, so a copy is the fallback. The copy goes to a temporary file beside dst
// and is renamed into place only after a full write, so a failed copy leaves no
// index rather than a partial one. Best-effort: see runWorktreeCheckout.
func installWorktreeIndex(src, dst string) {
	if indexRename(src, dst) == nil {
		return
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "index.tmp-")
	if err != nil {
		return
	}
	err = indexWrite(tmp, b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = indexRename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// indexRename and indexWrite are seams for the tests of installWorktreeIndex.
var (
	indexRename = os.Rename
	indexWrite  = func(f *os.File, b []byte) error { _, err := f.Write(b); return err }
)

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
	return hardenedGitRunPre(dir, heavy, true, stdin, args...)
}

// hardenedGitRunPre is hardenedGitRun with the choice of precursor of hardenedGitCmd.
func hardenedGitRunPre(dir string, heavy, precursor bool, stdin io.Reader, args ...string) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := hardenedGitCmd(ctx, dir, heavy, precursor, args...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// readStatusFile, writeStatusFile and chtimesStatusFile are seams over the three
// filesystem calls that assemble hardenedGitStatus's temp gitdir below. The read
// after a successful Stat fails on a race (TOCTOU) or on a HEAD/index that exists
// but is unreadable — stageable only on Unix as non-root, so the seam makes the arm
// reachable on every CI leg; the write and the Chtimes target a directory and a file
// this function just created, which no fixture can make fail. Swallowing any of the
// three would run status with incomplete metadata, so the arms propagate. Production
// never reassigns them.
var (
	readStatusFile    = os.ReadFile
	writeStatusFile   = os.WriteFile
	chtimesStatusFile = os.Chtimes
)

// hardenedGitStatus runs git.status through an ISOLATED temp gitdir, the way the
// reference does, so status never refreshes the caller's index. It builds a fresh
// GIT_DIR (the worktree's own HEAD and index copied in) with GIT_COMMON_DIR pointing at
// the shared repo and --work-tree at the worktree, under the heavy hardening profile.
// Reconstructed from the reference's runtime git argv+env (scratch/probe/gitargv), which
// showed --git-dir=<temp>, GIT_COMMON_DIR=<repo>/.git and --work-tree=<wt>.
//
// On Linux and macOS this is byte-identical to a direct `git -C <wt> status` (measured),
// and to the reference. On Windows the reference's own git.status of a linked worktree
// answers exit status 128 (measured), and this returns the status. That is the
// deliberate divergence D16. With Git for Windows 2.55.0 its cause is the
// core.excludesFile value of the status call (statusExcludesFile). See
// docs/DIVERGENCES.md. Returns stdout and the raw exec error, like
// hardenedGitStdout, so a non-zero exit surfaces as "exit status N", the same
// string the reference puts on the wire.
//
// The call has the shape of f6010b97 on Linux and macOS VMs. Its working
// directory is the worktree. The argv is --attr-source (attrSourceArgs), the heavy
// profile, --git-dir, --work-tree, then args. Its config precursor is `--git-dir=<temp>
// config -z --list --name-only` in the worktree. Both carry the heavy profile and
// GIT_COMMON_DIR=commonDir, cleaned. git prints commonDir with forward slashes on
// Windows, and f6010b97 pins it with backslashes on a Windows VM. The status
// call alone adds GIT_OPTIONAL_LOCKS=0: 4534d86
// sets it on its status commands. Without it a plain `git status` REWRITES the
// worktree's index (refreshing the stat cache) and takes index.lock — a mutation of
// the caller's repo on a read. With it the status output is byte-identical
// (measured) but the index is not touched, matching the reference.
func hardenedGitStatus(worktree, gitDir, commonDir string, args ...string) (string, error) {
	tmp, err := os.MkdirTemp("", statusGitDirTempPrefix)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// Copy the worktree's own HEAD and index into the temp gitdir so status compares
	// against the right commit and staged state; objects and refs resolve through
	// GIT_COMMON_DIR. A genuinely absent file (a worktree with no index yet) is skipped
	// — git rebuilds it from HEAD. A file that EXISTS but cannot be read (permission,
	// I/O, transient error) is propagated, not swallowed: skipping it would run status
	// with incomplete metadata and fabricate deletions or untracked entries.
	for _, f := range []string{"HEAD", "index"} {
		src := filepath.Join(gitDir, f)
		fi, e := os.Stat(src)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return "", e
		}
		b, e := readStatusFile(src)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return "", e
		}
		dst := filepath.Join(tmp, f)
		if e := writeStatusFile(dst, b, 0o600); e != nil {
			return "", e
		}
		// Preserve the source mtime. For the index this is load-bearing: git's
		// racy-clean check compares each work-tree file's mtime against the index's
		// own mtime, and a fresh (newer) index would make git trust the stale stat
		// cache and miss an unstaged modification whose size is unchanged (a "two\n"
		// over a "one\n"). Copying the mtime keeps status byte-identical to a direct
		// run against the worktree's real index.
		if e := chtimesStatusFile(dst, fi.ModTime(), fi.ModTime()); e != nil {
			return "", e
		}
	}
	// The temp gitdir must carry a `commondir` file naming the shared repo, so git
	// resolves a branch symref HEAD (`ref: refs/heads/<wt>`) against the common refs.
	// GIT_COMMON_DIR alone is not enough for that ref resolution (measured): without
	// commondir a branch worktree reports every tracked file as a fresh add.
	if e := writeStatusFile(filepath.Join(tmp, "commondir"), []byte(commonDir+"\n"), 0o600); e != nil {
		return "", e
	}
	ctx, cancel := gitCtx()
	defer cancel()
	attr := attrSourceArgs()
	excludes := statusExcludesFile()
	pin := []string{"GIT_COMMON_DIR=" + filepath.Clean(commonDir)}
	pre := exec.CommandContext(ctx, "git", "--git-dir="+tmp, "config", "-z", "--list", "--name-only")
	pre.Dir = worktree
	pre.Env = precursorEnv(true, pin)
	_, _ = pre.Output()
	full := append(attr, profileArgsWithExcludes(true, excludes, append([]string{"--git-dir=" + tmp, "--work-tree=" + worktree}, args...)...)...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = worktree
	cmd.Env = append(hardenedGitEnv(true, pin), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// statusExcludesFile is the core.excludesFile value of the status call. It is
// userExcludesFile, except that the null device is spelled /dev/null on every OS.
// On Windows git status exits 128 with "fatal: cannot use NUL as an exclude file"
// when core.excludesFile is NUL. It accepts /dev/null there. git ls-files and git
// check-ignore accept NUL. Measured with Git for Windows 2.55.0 on a Windows 11 VM.
// f6010b97 passes NUL to its status call on that VM and answers exit status 128.
// This one value is the divergence D16. On Linux and macOS os.DevNull is /dev/null,
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

// hostileConfigRefusal runs the config-hook enumeration 7d193f89 performs before
// every git command (`git config -z --list --name-only`). When that enumeration
// FAILS — e.g. a corrupt `.git/config` — the reference cannot pin the repo's
// config-defined hooks off, so it refuses the whole method rather than run git.
// Returns the refusal detail and true on failure; "" and false when the config is
// enumerable. Callers phrase the method-specific frame (a -32603 for the read
// methods, a worktreeResult for worktree_create, its own message for
// worktree_remove). Measured byte-for-byte against 7d193f89 on an ephemeral VM.
//
// In git.info, git.list_branches, git.worktree_create and git.worktree_remove, this
// check is the first listing of the method, not an extra one. It stands in for the
// precursor of the method's first hardened call: dir is its working directory, and
// heavy is the profile of that call. f6010b97 makes one listing there, not two, on
// Linux, macOS and Windows VMs. When the next call is on the same dir, the caller
// runs it with no precursor of its own (hardenedGitFirst).
func hostileConfigRefusal(dir string, heavy bool) (string, bool) {
	userExcludesFile()
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "-z", "--list", "--name-only")
	cmd.Dir = dir
	cmd.Env = precursorEnv(heavy, commonDirPinEnv(dir))
	_, err := cmd.Output()
	if err == nil {
		return "", false
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
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		return "", false
	}
	// Residual fallback for the one chdir failure os.Stat cannot see: dir exists and is
	// a directory, but git cannot start in it (dir itself lacks +x). The start then
	// fails with a *fs.PathError, before git runs. This stays behavioural for that
	// exotic, non-honest input. The honest nonexistent-path case never reaches it.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return "", false
	}
	var stderr string
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// Not trimmed here. worktreeGitText cuts the raw stderr at 512 bytes first and
		// trims after that, so leading white space counts toward the cap.
		stderr = string(ee.Stderr)
	}
	// The reference wraps the git error as "<exit status>: <stderr>", then as
	// "listing the configuration in force: <that>", then as the hooks refusal.
	// git's text goes through worktreeGitText, the rule of the git.worktree_create
	// failure frames. 90fca6e6 reports git's "…/  \t..': No such file or directory" as
	// "…/   ..': …" (Linux VM, C10). f6010b97 applies the whole rule in this frame. A stub
	// git on a Windows VM gave multi-line, CRLF, over-512-byte, invalid UTF-8 and control
	// byte payloads. 40 newlines and 40 spaces before a long text showed that the cap
	// comes before the trim (row HK_leadlong).
	detail := err.Error()
	if text := worktreeGitText(stderr, nil); text != "" {
		detail += ": " + text
	}
	return hooksRefusalPrefix + detail, true
}

// hooksRefusalPrefix starts the hooks refusal. The exec error and git's text follow.
const hooksRefusalPrefix = "config-defined hooks could not be pinned off; git not run: " +
	"listing the configuration in force: "

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
// It runs right after hostileConfigRefusal on dir, so it runs no precursor.
func noRepositoryAt(dir string) bool {
	return repositoryCheckError(dir, false) != nil
}

// repositoryCheckError is noRepositoryAt with the exec error of the failed call, for
// example "exit status 128". It answers nil where noRepositoryAt answers false.
//
// The call runs on the heavy profile, as f6010b97 runs it on Linux, macOS and
// Windows VMs. precursor is false when it is the first call after
// hostileConfigRefusal on dir (see hardenedGitCmd).
func repositoryCheckError(dir string, precursor bool) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	_, err := hardenedGitRunPre(dir, true, precursor, nil, "rev-parse", "--absolute-git-dir")
	return err
}

// statInsideDir looks at name inside dir through a handle on dir, without
// following a symlink at name. It reports nil when dir does not exist, and when
// name does not exist: those inputs keep their own answers. Any other failure is
// returned as is. A directory that can be opened but not searched (mode 0600)
// gives "statat .claude: permission denied". One that cannot be opened at all
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
