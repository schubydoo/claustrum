package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
// The core.excludesFile entry is a PLACEHOLDER: hardenedArgs substitutes the user's
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
	"core.excludesFile=/dev/null", // placeholder — see hardenedArgs / userExcludesFile
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
	"core.excludesFile=/dev/null", // placeholder — see hardenedArgs / userExcludesFile
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

// hardenedGitEnv builds the environment for a hardened git command: the daemon's
// own environment, the hook pins, terminal/prompt suppression, and the protocol
// gate. heavy adds the no-lazy-fetch and askpass clearing the heavy profile uses.
func hardenedGitEnv(heavy bool) []string {
	env := append(os.Environ(), hookPinEnv()...)
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if heavy {
		env = append(env,
			"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
			"GIT_ASKPASS=",
			"GIT_NO_LAZY_FETCH=1",
		)
	} else {
		env = append(env, "GIT_ALLOW_PROTOCOL=https:ssh")
	}
	return env
}

// hardenedArgs prefixes the `-C dir` selector and the profile's `-c` overrides
// before the git subcommand. The profile's placeholder core.excludesFile is filled
// in with the user's resolved global excludes (userExcludesFile) — 7d193f89 runs
// every git op with the user's ~/.config/git/ignore in force, not /dev/null, so that
// e.g. git.status classifies a globally-ignored file as ignored. On a host with no
// global excludes the resolver returns /dev/null, leaving the old behaviour.
func hardenedArgs(dir string, heavy bool, args ...string) []string {
	profile := gitHardenLight
	if heavy {
		profile = gitHardenHeavy
	}
	excludes := userExcludesFile()
	full := make([]string, 0, 2+2*len(profile)+len(args))
	full = append(full, "-C", dir)
	for _, c := range profile {
		if strings.HasPrefix(c, "core.excludesFile=") {
			c = "core.excludesFile=" + excludes
		}
		full = append(full, "-c", c)
	}
	return append(full, args...)
}

// hookPrecursor runs the config-enumeration the reference issues before every
// hardened command (`git -C dir config -z --list --name-only`) to discover
// config-defined hooks. Its result does not change the pinning for a repo with no
// hook config — the two base pins in hookPinEnv cover that — but the call is part
// of the reference's process trace, so it is reproduced. Best-effort: a failure
// leaves the base pins in place.
func hookPrecursor(ctx context.Context, dir string) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "-z", "--list", "--name-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh")
	_, _ = cmd.Output()
}

// hardenedGitContext runs a git subcommand under the hardening profile and
// environment, with the config precursor first. Combined output, like git().
func hardenedGitContext(ctx context.Context, dir string, heavy bool, args ...string) (string, bool) {
	hookPrecursor(ctx, dir)
	cmd := exec.CommandContext(ctx, "git", hardenedArgs(dir, heavy, args...)...)
	cmd.Env = hardenedGitEnv(heavy)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err == nil
}

// hardenedGit is hardenedGitContext with the standard git timeout context.
func hardenedGit(dir string, heavy bool, args ...string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	return hardenedGitContext(ctx, dir, heavy, args...)
}

// hardenedGitStdout is hardenedGit but returns stdout only and the exec error,
// for callers that split a warning on stderr from a real failure (git.status,
// git.list_branches).
func hardenedGitStdout(dir string, heavy bool, args ...string) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	hookPrecursor(ctx, dir)
	cmd := exec.CommandContext(ctx, "git", hardenedArgs(dir, heavy, args...)...)
	cmd.Env = hardenedGitEnv(heavy)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

var (
	userExcludesOnce   sync.Once
	userExcludesCached string
)

// userExcludesFile resolves the user's global git excludes file once: the configured
// core.excludesFile if it is an absolute path, else $XDG_CONFIG_HOME/git/ignore or
// $HOME/.config/git/ignore when that file exists, else "/dev/null". This is the
// excludes path 7d193f89 passes as core.excludesFile on every git invocation (observed
// via a git-argv trace). hardenedArgs substitutes it for every git op.
func userExcludesFile() string {
	userExcludesOnce.Do(func() { userExcludesCached = resolveUserExcludesFile() })
	return userExcludesCached
}

func resolveUserExcludesFile() string {
	// The configured value wins if absolute. `config --includes --path` follows
	// includes and expands a leading ~, matching the reference. It runs WITHOUT the
	// hardening profile so git reports the real global setting, not the pin.
	ctx, cancel := gitCtx()
	defer cancel()
	// GIT_DIR=/dev/null makes this read only the user's global/system config, never a
	// repo's own core.excludesFile — a repo must not be able to inject excludes. This
	// is the once-cached read that replaced the reference's per-call excludes probe.
	cmd := exec.CommandContext(ctx, "git", "config", "--includes", "--path", "core.excludesFile")
	cmd.Env = append(os.Environ(), "GIT_DIR=/dev/null", "GIT_TERMINAL_PROMPT=0")
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
	return "/dev/null"
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
func hostileConfigRefusal(dir string) (string, bool) {
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "-z", "--list", "--name-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh")
	_, err := cmd.Output()
	if err == nil {
		return "", false
	}
	// A failure to CHDIR into dir — it does not exist, is a non-directory, or a parent
	// is not traversable — is NOT a hostile config: git never reached a repo, so there
	// are no config-defined hooks to pin off. This is a fully client-controlled honest
	// input (a stale or mistyped path), so detect it locale-independently with os.Stat,
	// BEFORE parsing git's localizable "cannot change to" text — a git build whose
	// translation covers that message would otherwise flip this honest path to the
	// -32603 refusal. The reference lets such an input fall through to its normal
	// not-a-repo shape (measured against 7d193f89: a nonexistent path/baseRepo answers
	// isRepo:false on the read methods, not_a_repo on worktree_create, and success on
	// worktree_remove — NOT this refusal). Only a config git CAN reach but cannot parse
	// (a corrupt .git/config) is refused here.
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		return "", false
	}
	var stderr string
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr = strings.TrimSpace(string(ee.Stderr))
	}
	// Residual fallback for the one chdir failure os.Stat cannot see: dir exists and is
	// a directory, but git still cannot chdir into it (dir itself lacks +x). git's -C
	// chdir failure reads "cannot change to '<dir>'". This stays behavioural for that
	// exotic, non-honest input; the honest nonexistent-path case never reaches it.
	if strings.Contains(stderr, "cannot change to") {
		return "", false
	}
	// The reference wraps the git error as "<exit status>: <stderr>", then as
	// "listing the configuration in force: <that>", then as the hooks refusal.
	detail := err.Error()
	if stderr != "" {
		detail = err.Error() + ": " + stderr
	}
	return "config-defined hooks could not be pinned off; git not run: " +
		"listing the configuration in force: " + detail, true
}

// stderrHeadCap is the byte cap 7d193f89 applies to captured git output before it
// reaches an error frame (its bounded stderr sink keeps only the first 512 bytes).
const stderrHeadCap = 512

// boundedStderrHead caps s at its first stderrHeadCap BYTES and trims surrounding
// whitespace, matching 7d193f89: a git failure whose output exceeds 512 bytes is
// reported with only the head, so an error frame (e.g. "git worktree add failed: …")
// cannot balloon with a huge git message. The cap is on bytes, not runes, so a
// multi-byte rune split at the boundary is kept as-is — exactly as the reference's
// byte-bounded buffer does.
func boundedStderrHead(s string) string {
	if len(s) > stderrHeadCap {
		s = s[:stderrHeadCap]
	}
	return strings.TrimSpace(s)
}
