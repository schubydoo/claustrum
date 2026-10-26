package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"unicode"
)

// The configuration listing that runs before every hardened git call, and what the
// daemon reads from it. 89cb6289 lists keys and values (`config -z --list`), and its
// hardened calls pin off every hook name the listings show. gitlisting_test.go tests
// the rules, and docs/protocol/git-rules-calls.md → "Hook pins" gives them for a reader. Each rule was measured side by side against 89cb6289 on
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
	// notARepoHead starts the stderr of git where it finds no repository. Only
	// statusNoRepoCalls still reads it.
	notARepoHead = "fatal: not a git repository"
	// gitFileNowhereHead starts the stderr of git 2.56 for a `.git` file that names no
	// valid repository. Older gits print notARepoHead there.
	gitFileNowhereHead = "fatal: gitfile does not point to a valid repository"
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
	// dir, gitDir and env are the call that ran: its folder, the --git-dir= option and
	// its environment. saysNoRepository reads them.
	dir, gitDir string
	env         []string
}

// runListing runs `git [--git-dir=<gitDir>] config -z --list` in dir with env. gitDir
// is "" for the plain form. In a request with time limits the listing ends at
// configListingLimit (gitRun). The daemon reads its output to the end
// (untilPipeEOF). The command has no SysProcAttr, so a dir that does not exist
// fails the start in os.StartProcess before any process starts
// (missingPathListing).
func runListing(ctx context.Context, dir, gitDir string, env []string) listingRun {
	ctx = untilPipeEOF(withGitLimit(ctx, listingLimit))
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
	out, err := gitOutput(ctx, cmd)
	return listingRun{out: out, stderr: errBuf.String(), err: err, dir: dir, gitDir: gitDir, env: env}
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

// saysNoRepository reports the "no repository" verdict of a failed listing. 5fd08069
// tested the text: exit status 128, and stderr that starts with "fatal: not a git
// repository".
//
// claustrum ignores the text. The listing must end with exit code 128 and name no
// git dir: no --git-dir= option, no GIT_DIR= entry in its environment. Then claustrum
// searches for a git dir upward from the folder (listingFindsNoRepository). That rule
// is claustrum's own, not measured. In a folder with no repository the listing
// carries GIT_DIR=<null device>. c8e2f083 then takes the generic path on exit 128
// (measured on Linux and Windows).
//
// One text still counts: for the gitfile text of git 2.56 (saysGitFileNowhere)
// claustrum gives no "no repository" verdict and keeps the earlier refusal
// (claustrum's own, not measured).
func (r listingRun) saysNoRepository() bool {
	var ee *exec.ExitError
	if !errors.As(r.err, &ee) || ee.ExitCode() != 128 || listingNamesGitDir(r.gitDir, r.env) ||
		r.saysGitFileNowhere() {
		return false
	}
	return listingFindsNoRepository(r.dir)
}

// saysGitFileNowhere reports whether the listing failed with the text of git 2.56 for
// a `.git` file that names no valid repository. Exit 128 and the exact text were
// measured (cells WR13, GF and N04 of round tier0-27, Linux VM). The rule of white
// space and letter case is that of saysNoRepository (not measured).
func (r listingRun) saysGitFileNowhere() bool {
	return r.exit128Says(gitFileNowhereHead)
}

// exit128Says reports whether the listing exited 128 with stderr that starts with
// head once leading white space is dropped, compared without regard to letter case.
func (r listingRun) exit128Says(head string) bool {
	var ee *exec.ExitError
	if !errors.As(r.err, &ee) || ee.ExitCode() != 128 {
		return false
	}
	s := strings.TrimLeftFunc(r.stderr, unicode.IsSpace)
	return len(s) >= len(head) && strings.EqualFold(s[:len(head)], head)
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
// measured. The names match exactly, also on Windows (not measured). The base is
// the daemon's environment as a git child gets it (gitChildEnv).
func listingEnvBase() []string {
	return listingEnvOf(gitChildEnv())
}

// listingEnvOf is listingEnvBase with daemon as the daemon's environment.
// shadowDaemonEnv gives it os.Environ(), with no cleaning.
func listingEnvOf(daemon []string) []string {
	env := dropConfigOverrides(daemon)
	out := env[:0]
	for _, kv := range env {
		if n := envName(kv); n != "LC_ALL" && n != "LANGUAGE" {
			out = append(out, kv)
		}
	}
	return out
}

// noRepoCalls runs the configuration listing in dir, a folder in which the trust
// check finds no repository, and returns the refusal text of a listing that
// refuses, or "". After a listing that passed it runs the call args in dir.
// heavy is the profile of both calls, and u their form. With a broken GIT_CONFIG_KEY_<n>
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
// GIT_DIR does not come here: the trust check pins it, and the daemon's own read of
// the config refuses it (readRepoConfig, cell V-Wd of round task039-02). The other
// states are a daemon GIT_COMMON_DIR and a `.git` file that names a regular file. Both
// are not measured. A `.git` file that names a git dir that is gone gets the listing.
// With git older than 2.56, git answers "not a git repository" there, so the frame
// stays (cells CbP0 and CbP3, Linux and macOS VMs).
//
// git 2.56 answers its gitfile text there (saysGitFileNowhere). A gone entry under
// .git/worktrees then gets "no repository" (cells c01, c03 and c04 on a Linux VM, c26
// and c27 on a Windows VM, all of round task039-01). It does so also as the first git
// request of a daemon and with "shadowDirOff":true (cells c01 first, c03 first and
// c01off, c03off and c04off of round task039-03, Linux VM). Any other gone git dir gets the
// error of the call about the repository (cells c09, c11, c12 and c18, Linux and
// Windows VMs), also as the first git request (cells c09 first to c12 first of round
// task039-03, Linux VM, and F-S2w, Windows VM). With off, the "shadowDirOff":true
// member of the request, it gets the listing refusal (cells c09off, c11off and c12off
// of round task039-02, Linux VM).
//
// Not measured: a listing that passes and exceeds a limit of parseConfigListing, and
// a `.git` file with no gitdir line. claustrum answers them by hostileConfigRefusal.
func noRepoCalls(t gitDirTrust, u gitUse, dir string, heavy, off bool, args ...string) string {
	if !t.noRepoPinned() {
		return ""
	}
	c := hostileConfigRefusal(u, dir, heavy)
	if c.gitFileText != "" {
		switch {
		case !goneNonEntryGitFile(dir):
			return ""
		case off:
			return c.refusal
		}
		return c.gitFileText
	}
	if !c.refused() {
		hardenedGitFirst(u, dir, heavy, c.listing, args...)
	}
	return c.refusal
}

// noRepoInfoCalls is noRepoCalls for git.info. After a
// listing that passed, 5fd08069 runs the heavy `rev-parse --git-dir` in the folder,
// with GIT_DIR=<null device> and the hook pins of the listing. The call exits 128.
// The reply is the "no repository" answer, as without the call. The rows are a
// folder with no repository and a repository with a HEAD that is no ref, with and
// without "shadowDirOff": cells S1-first-norepo and S1-first-badhead of
// val-s3p2b2-3-macos (macOS VM), row D6 of val-s3p2b2-3-windows (Windows VM) and
// cell E5-HEAD-garbage of val-s3p2b2-3-linux (Linux VM). The Linux and macOS call
// logs of those cells do not keep the order of the variables. claustrum does not
// read the answer.
//
// git.list_branches makes the same two calls. On 5fd08069 a git.list_branches on
// a repository whose HEAD has mode 000 makes the listing and then that heavy
// `rev-parse --git-dir`, with GIT_DIR=<null device> and exit 128 (cell
// h1-mode000 of val-s3p2b2-7-linux2, Linux VM). A folder with no `.git` entry
// is not measured for git.list_branches: it gets the call too.
func noRepoInfoCalls(t gitDirTrust, s *gitStop, dir string, off bool) string {
	return noRepoCalls(t, gitUse{stop: s}, dir, true, off, "rev-parse", "--git-dir")
}

// missingPathListing is the listing of noRepoInfoCalls for a git.info on dir, a
// path that does not exist. It takes the steps of that listing, with its pin and
// its walk, and returns the error of the start. os.StartProcess, which
// exec.Cmd.Start calls, tests the folder of a command with no SysProcAttr before
// it starts a process, on Linux, macOS and Windows (from the code of Go). So the
// start fails on the missing folder, and no process starts. 5fd08069 starts none
// there either: no clone, fork, execve or chdir follows the request (cell T7s of
// round task107-01, Linux VM). A folder that appears before the start gets the
// listing, and git.info does not read it (claustrum's own, not measured). The
// listing has its own stop state, so a listing that its time limit stops there
// does not change the reply.
func missingPathListing(dir string) error {
	u := gitUse{stop: &gitStop{}}
	return runListing(u.stop.context(), dir, "", u.precursorEnv(true, u.pin(dir))).err
}

// noRepoCreateCalls is noRepoCalls for git.worktree_create. On 5fd08069 a create on a repository whose HEAD has mode 000 makes
// the listing and then the light `rev-parse --is-inside-work-tree`, with
// GIT_DIR=<null device>, the hook pins and exit 128. Both calls carry the six
// values of gitUse.profileEnv in its order, in a daemon that has no state yet
// (cell h1-mode000 of val-s3p2b2-7-linux2, Linux VM). The reply is the "not a
// git repository" answer, as without the call.
//
// six is true for a request without the member of a daemon with a run folder.
// With the member and with no run folder the two calls keep the four light
// values (not measured). A folder with no `.git` entry is not measured: it
// gets the call too.
func noRepoCreateCalls(t gitDirTrust, s *gitStop, six, off bool, dir string) string {
	return noRepoCalls(t, gitUse{lightSix: six, stop: s}, dir, false, off, "rev-parse", "--is-inside-work-tree")
}

// gitVersionRun runs the plain `git version` that 89cb6289 runs after a listing that
// failed for any reason other than "no repository" (rows L10 to L12, L14c and L15). It
// has no -c option. Its working directory is gitVersionDir. The daemon's own LC_ALL
// and LANGUAGE do not come back: whether they do
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
// show the same on a Windows VM.
//
// The names that go are those of gitVersionEnv (claustrum's own, not
// measured). They include GIT_CONFIG_SYSTEM. GIT_CONFIG_NOSYSTEM of the daemon stays.
// The base is the filtered environment of the daemon (gitChildEnv), then the profile.
// The names match by exact letter case, also on Windows, where the names of the
// environment have no case. So a lower-case git_config_count or git_config_global
// of the daemon stays on the call there.
func gitVersionRun(u gitUse, heavy bool) listingRun {
	cmd := exec.Command("git", "version")
	cmd.Dir = gitVersionDir
	cmd.Env = gitVersionEnv(append(listingEnvBase(), u.profileEnv(heavy)...))
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := gitRun(u.stop.context(), cmd)
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
func failedListingText(u gitUse, r listingRun, heavy bool) string {
	// A listing that its time limit stopped refuses with the hooks refusal and the
	// stop text (gitStopped), and no `git version` follows (cell T10 of round
	// task010-01, Linux and macOS VMs, and cell T10w on a Windows VM).
	//
	// A listing whose repository folder went refuses with its own text: the
	// listing form (`listingInForcePrefix` + `repoCopyGoneText`), or the text of
	// a failed remake (copyGone). No `git version` follows (from the code, not
	// measured).
	if isGitStop(r.err) || copyGone(r.err) {
		return r.err.Error()
	}
	if v := gitVersionRun(u, heavy); v.err != nil {
		return gitCannotRunPrefix + v.detail()
	}
	return hooksRefusalPrefix + r.detail()
}
