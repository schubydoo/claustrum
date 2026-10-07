package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode"
)

// The configuration listing that runs before every hardened git call, and what the
// daemon reads from it. 89cb6289 lists keys and values (`config -z --list`), and its
// hardened calls pin off every hook name the listings show. docs/PROTOCOL.md → "Hardened
// git calls" gives the rules. Each rule was measured side by side against 89cb6289 on
// Linux, macOS and Windows VMs, unless its comment says otherwise. The locale rule of
// row L03 (an English listing error under a German locale) is from a Linux VM only.

const (
	// gitCannotRunPrefix starts the refusal of a failed listing after which `git
	// version` also fails (rows L11, L12 and L15).
	gitCannotRunPrefix = "git cannot run on this host; git not run: "
	// configKeyMaxBytes is the longest key that the listing reads. A key of 1048576
	// bytes passes, and one of 1048577 bytes is refused (rows L08b and L08c). The
	// value has no such cap: a 2 MiB value passes (row L09). A value cap above 2 MiB
	// is not measured.
	configKeyMaxBytes = 1 << 20
	// hookNameMaxCount is the most hook names that the daemon pins. 1024 names pass,
	// and 1025 are refused (rows L06a and L06b).
	hookNameMaxCount = 1024
	// hookNameMaxBytes bounds the sum of the bytes of the hook names. 64 names of 1024
	// bytes pass (row L07a), so no overhead per name counts. 63 names of 1024 bytes and
	// one of 1025 are refused (row L07b). A sum of 65537 made only of names of at most
	// 1024 bytes is not measured. The refusal text names an aggregate bound, so the
	// daemon counts a sum.
	hookNameMaxBytes = 1 << 16
	// notARepoHead starts the stderr of a listing that answers "no repository".
	notARepoHead = "fatal: not a git repository"
)

// configListing is what the daemon takes from one listing that succeeded: the hook
// names, in the order the listing first shows each one.
type configListing struct {
	hooks []string
}

// listingReadRefusal is the reason text of a listing that the daemon cannot read.
const listingReadRefusal = "a configuration key is longer than the listing reads"

// parseConfigListing reads the output of `git config -z --list`. Each record ends with
// a NUL byte. A record is the key, a LF and the value, or the key alone for a key
// with no value (`[x] flag`). The key ends at the first LF of the record, and the
// value, LFs included, is never read as a key (row L04).
//
// A key of the section `hook` with at least three parts names a hook. The name is the
// text between the first and the last dot of the key. It can be empty (`hook..x`) and
// can hold dots (`hook.a.b.x`). A key of two parts (`hook.x`) names none. Each name
// counts once, compared byte for byte (row L05).
//
// Not measured: which variable names under hook.<name>. make a name (only command and
// event were used), whether two names that differ only in letter case are one name
// (git keeps a subsection's case, so claustrum pins both), and whether a repeated
// name counts twice toward the limits (claustrum counts it once).
//
// It returns the refusal reason, without prefix, when the listing cannot be used.
func parseConfigListing(out []byte) (configListing, string) {
	var l configListing
	seen := map[string]bool{}
	size := 0
	for len(out) > 0 {
		rec := out
		if i := bytes.IndexByte(out, 0); i >= 0 {
			rec, out = out[:i], out[i+1:]
		} else {
			out = nil
		}
		key := rec
		if i := bytes.IndexByte(rec, '\n'); i >= 0 {
			key = rec[:i]
		}
		if len(key) > configKeyMaxBytes {
			return configListing{}, listingReadRefusal
		}
		name, ok := hookNameOf(string(key))
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		size += len(name)
		l.hooks = append(l.hooks, name)
	}
	// The count is tested before the byte sum. With both limits exceeded, which one
	// 89cb6289 names is not measured.
	if len(l.hooks) > hookNameMaxCount {
		return configListing{}, "too many configured hooks to pin"
	}
	if size > hookNameMaxBytes {
		return configListing{}, "configured hook names exceed the aggregate pin byte bound"
	}
	return l, ""
}

// hookNameOf returns the hook name of a configuration key, if the key names one.
func hookNameOf(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "hook.")
	if !ok {
		return "", false
	}
	last := strings.LastIndexByte(rest, '.')
	if last < 0 {
		return "", false
	}
	return rest[:last], true
}

// listingRun is the raw outcome of one listing.
type listingRun struct {
	out    []byte
	stderr string
	err    error
}

// runListing runs `git [--git-dir=<gitDir>] config -z --list` in dir with env. gitDir
// is "" for the plain form.
func runListing(ctx context.Context, dir, gitDir string, env []string) listingRun {
	args := []string{"config", "-z", "--list"}
	if gitDir != "" {
		args = append([]string{"--git-dir=" + gitDir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	// Stderr is set before Output, so (*exec.ExitError).Stderr stays empty. Read
	// listingRun.stderr, never ee.Stderr.
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	return listingRun{out: out, stderr: errBuf.String(), err: err}
}

// hooks is the listing's hook names, or none when the listing failed or cannot be
// used. A later listing of a method that fails, or that exceeds a limit, is not a
// refusal: the call after it gets the two base pins only, as before. Every measured
// failure hit the first listing of a method, so this choice is not measured.
//
// Each hardened call takes its pins from the listing right before it. That choice is
// not measured: only configurations that stay the same during a request were run.
func (r listingRun) hooks() []string {
	if r.err != nil {
		return nil
	}
	l, reason := parseConfigListing(r.out)
	if reason != "" {
		return nil
	}
	return l.hooks
}

// saysNoRepository reports whether the listing failed the way git fails where it finds
// no repository: exit status 128, and stderr that starts with "fatal: not a git
// repository" once leading white space is dropped, compared without regard to letter
// case (rows L14a, L14b, L14d and L14e). Exit 1 with that text does not qualify (row
// L14c). Not measured: whether 89cb6289 tests a prefix or a substring
// (every text started with it), and exit codes other than 1 and 128.
func (r listingRun) saysNoRepository() bool {
	var ee *exec.ExitError
	if !errors.As(r.err, &ee) || ee.ExitCode() != 128 {
		return false
	}
	s := strings.TrimLeftFunc(r.stderr, unicode.IsSpace)
	return len(s) >= len(notARepoHead) && strings.EqualFold(s[:len(notARepoHead)], notARepoHead)
}

// gitNotOnPath reports whether the listing did not start because PATH holds no git.
// No process starts then (row L13).
func (r listingRun) gitNotOnPath() bool {
	return errors.Is(r.err, exec.ErrNotFound)
}

// gitLookupError is the exec error that a git call gets when PATH holds no git, or
// nil. It starts no process.
func gitLookupError() error {
	if _, err := exec.LookPath("git"); errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return nil
}

// detail is the text that follows the listing prefix in a refusal: the exec error,
// then ": " and git's stderr through worktreeGitText when that text is not empty.
func (r listingRun) detail() string {
	var stderr string
	var ee *exec.ExitError
	if errors.As(r.err, &ee) {
		// Not trimmed here. worktreeGitText cuts the raw stderr at 512 bytes first and
		// trims after that, so leading white space counts toward the cap.
		stderr = r.stderr
	}
	// The refusal text is the hooks prefix, then `listing the configuration in force: `,
	// then the exec error, then `: ` and git's text.
	// git's text goes through worktreeGitText, the rule of the git.worktree_create
	// failure frames. f6010b97 applies the whole rule in this frame. A stub git on a
	// Windows VM gave multi-line, CRLF, over-512-byte, invalid UTF-8 and control byte
	// payloads. 40 newlines and 40 spaces before a long text showed that the cap comes
	// before the trim (row HK_leadlong).
	d := r.err.Error()
	if text := worktreeGitText(stderr, nil); text != "" {
		d += ": " + text
	}
	return d
}

// listingEnvBase is the daemon's own environment as the listing gets it: without
// GIT_CONFIG and GIT_CONFIG_PARAMETERS (gitenv.go), and without every entry named
// LC_ALL or LANGUAGE (row L02). LANG and every other entry stay in place. Other LC_*
// variables, such as LC_MESSAGES, stay too. Only LC_ALL, LANGUAGE and LANG were
// measured. The names match exactly, also on Windows (not measured).
func listingEnvBase() []string {
	env := dropConfigOverrides(os.Environ())
	out := env[:0]
	for _, kv := range env {
		if n := envName(kv); n != "LC_ALL" && n != "LANGUAGE" {
			out = append(out, kv)
		}
	}
	return out
}

// noRepoListingRefusal runs the configuration listing in dir, a folder in which the
// trust check finds no repository, and returns the refusal text of a listing that
// refuses, or "". heavy is the profile of the listing. With a broken GIT_CONFIG_KEY_<n>
// in the daemon's environment git fails that listing, and 89cb6289 then refuses
// git.info, git.list_branches and git.worktree_create. Rows A-N1, A-N2, A-N3, A-N6 and
// A-X2 show that on Linux and macOS VMs. Cells P3-infoN, P3-infoX, P3-lbN and P3-crN,
// and the same cells of P5 to P7, show it on a Windows VM. The call log of 89cb6289
// shows the listing with the folder as its working directory, also with no
// GIT_CONFIG_* entry. On Linux and macOS a folder that git cannot start in keeps its
// answer (row A-X1, mode 0000). On Windows a folder that git cannot start in refuses
// (cell A-13, see unenterableDir).
//
// Without the entry of t.noRepoPinned, no listing runs and the answer is "". One such
// state is a daemon GIT_DIR that names a regular file on Linux and macOS (t.noGit).
// 89cb6289 answers the "no repository" frame of git.info there, on a plain folder and
// on a repository, and its call log shows no listing. That holds with and without a
// broken GIT_CONFIG_KEY_0 (cells CfP0 and CfP3, Linux and macOS VMs). On Windows that
// GIT_DIR does not come here: the trust check pins it, and the listing of the method
// fails with git's `invalid gitfile format` text (cells Wd, Windows VM). The other
// states are a daemon GIT_COMMON_DIR and a `.git` file that names a regular file. Both
// are not measured. A `.git` file that names a git dir that is gone gets the listing,
// and git answers "not a git repository" there, so the frame stays (cells CbP0 and
// CbP3, Linux and macOS VMs).
//
// Not measured: a listing that passes and exceeds a limit of parseConfigListing, and
// a `.git` file with no gitdir line. claustrum answers them by hostileConfigRefusal.
func noRepoListingRefusal(t gitDirTrust, dir string, heavy bool) string {
	if !t.noRepoPinned() {
		return ""
	}
	return hostileConfigRefusal(dir, heavy).refusal
}

// gitVersionRun runs the plain `git version` that 89cb6289 runs after a listing that
// failed for any reason other than "no repository" (rows L10 to L12, L14c and L15). It
// has no -c option. Its working directory is gitVersionDir. In claustrum its
// environment is the one of the failed listing without the GIT_COMMON_DIR pin and
// without LC_ALL=C and LANGUAGE=C. With a daemon GIT_DIR that names a regular file,
// the call of 89cb6289 has no GIT_DIR entry, and the call of claustrum has the
// daemon's (cells Wd, Windows VM). The frame and the disk are equal there. The
// daemon's own LC_ALL and LANGUAGE do not come back: whether they do
// is not measured. heavy is the profile of the failed listing. After the listing of
// unenterableBaseListing, this call gets the light environment, as on 89cb6289 (rows
// L16a and L16b on a Linux VM). In the symlink-chain rows LNKb-g and LNKr-g, 89cb6289
// runs no `git version` (Linux VM), and claustrum runs none there.
//
// The call gets no GIT_CONFIG_COUNT and no GIT_CONFIG_KEY_<digits> or
// GIT_CONFIG_VALUE_<digits>. The call log of 89cb6289 shows none of the three entries
// of the daemon on it (Linux, macOS and Windows VMs, each row with an empty
// GIT_CONFIG_KEY_0). A leading-zero name such as GIT_CONFIG_KEY_01 is not measured.
//
// The call gets no GIT_CONFIG_GLOBAL either. With a GIT_CONFIG_GLOBAL that names a
// file git cannot parse, the `git version` of 89cb6289 has no such entry and exits 0,
// and the answer is the hooks refusal of the listing (cell C-c on Linux and macOS VMs:
// git.info on a plain folder and on a repository, and git.worktree_create). Cells Wb
// show the same on a Windows VM. No log shows a GIT_CONFIG_SYSTEM or a
// GIT_CONFIG_NOSYSTEM of the daemon, so both stay (not measured).
//
// The names match by exact letter case, also on Windows, where the names of the
// environment have no case. So a lower-case git_config_count or git_config_global
// of the daemon stays on the call there. Not measured.
func gitVersionRun(heavy bool) listingRun {
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "version")
	cmd.Dir = gitVersionDir
	env := withoutConfigCountSet(listingEnvBase())
	env = slices.DeleteFunc(env, func(kv string) bool { return envName(kv) == "GIT_CONFIG_GLOBAL" })
	cmd.Env = append(env, profileEnv(heavy)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return listingRun{stderr: errBuf.String(), err: err}
}

// failedListingText is the refusal text of a listing that failed for any reason other
// than "no repository". When `git version` also fails, git cannot run on this host
// (rows L11, L12 and L15). Else the text is the listing refusal, as before (rows L10
// and L14c), with the listing's detail.
//
// The "cannot run" text carries the detail of the version call: its exec error, then
// ": " and its stderr through worktreeGitText. 89cb6289 answers `git cannot run on
// this host; git not run: exit status 1: <stderr of git version>` after a listing
// that exits 128. Row A-T1 with a `git version` that fails shows that on Linux and
// macOS VMs, and cells P11-infoN and P11-infoT on a Windows VM. One stderr line was
// measured, so the text rule is claustrum's choice.
//
// Every caller of this function gets that text. Three callers have no row with a
// `git version` that fails: the two later listings of git.status (statusWorkTreeProbe
// and statusRun.git) and unenterableBaseListing.
func failedListingText(r listingRun, heavy bool) string {
	if v := gitVersionRun(heavy); v.err != nil {
		return gitCannotRunPrefix + v.detail()
	}
	return hooksRefusalPrefix + r.detail()
}
