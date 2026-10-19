package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The settings control of the git methods, as 5fd08069 has it. That build keeps
// its own copy of the git settings of a repository in a folder beside its socket,
// and it runs git against that copy. Before the first use it tests once whether
// the git of the host works on such a copy. The result is the state of the daemon,
// and git.info reports it in the member settingsControl.
//
// This file holds the state, the folder of one daemon and the start test.
// gitshadowrepo.go holds the folder of one repository, and gitshadowconfig.go the
// text of the copied settings. gitshadowuse.go holds the variables that point a
// git call at the folder of a repository.

// The values of the member settingsControl of a git.info result. 5fd08069 sends
// these three (s3-linux cells A1 and B1, s3p2-oldgit cell O1, Linux VM).
const (
	controlShadow   = "shadow"
	controlFallback = "fallback-git"
	controlOff      = "off-switch"
)

// The two log lines of the state. 5fd08069 writes one at the first decision of
// the state and one more at a change (s3p2-linux cell B3a, s3p2b-edges-linux2
// cells 28 and 29, Linux VM). It writes none for a repeat test with the same
// result, and none for a failed test (cell 32 of that round).
const (
	shadowStateLine   = "[GitHandler] this host's git works on Claude's own copy of a repository's settings (a shadow directory): a repository's own git config and hooks are not read"
	fallbackStateLine = "[GitHandler] this host's git does not work on Claude's own copy of a repository's settings (fallback-git; older than about 2.13?): a repository's own git config is read, and only the -c overrides limit it"
)

// shadowRootName is the folder of all daemons below the folder of the socket.
// shadowLockName is the file in the folder of one daemon that the daemon holds.
const (
	shadowRootName = "git-shadow"
	shadowLockName = "lock"
)

// The times of the start test. Each is a variable, so that a test can lower it.
var (
	// shadowFailureKeep is how long a failed test is kept, from the end of that
	// test. In that time a request gets the kept text with no git call. On
	// 5fd08069 a request 9.90 s after the reply made no call, and one 10.00 s
	// after it ran the test again (s3p2b-edges-linux2 cell 26, Linux VM. 9.904
	// and 10.007 s on a macOS VM, 9.910 and 10.006 s on a Windows VM).
	shadowFailureKeep = 10 * time.Second
	// shadowRetestAfter is the age of the last test after which a daemon in the
	// state fallback-git tests again. On 5fd08069 a request at 599.8 s ran no
	// test and one at 605.5 s ran it (s3p2-oldgit cell O5 and s3p2b-edges-linux2
	// cell 28, Linux VM). The time between those two rows is not measured.
	shadowRetestAfter = 10 * time.Minute
	// shadowCallBound ends one call of the test. shadowCallDrain is the wait for
	// its output pipes after that. On 5fd08069 a call that its wrapper held for
	// 29 s passed. For a call held for 32 s or 40 s the reply came 31.0 s after
	// the start on a Linux VM, where the sleep child of the wrapper lived on
	// (s3p2b-edges-linux2 cell 31). On a macOS VM the next call started at
	// 30.04 s (section S4 of val-s3p2b2-1-macos). The 1 s wait is fitted from
	// the Linux rows.
	shadowCallBound = 30 * time.Second
	shadowCallDrain = time.Second
	// shadowClock is the clock of the two kept times.
	shadowClock = time.Now
)

// shadowHost is the settings control of one daemon. A nil host, or one with no
// run folder, has no state: no test runs, no folder is made and git.info sends no
// settingsControl member. The -serve daemon always has a run folder, the folder
// of its socket.
type shadowHost struct {
	// runDir is the folder of the socket.
	runDir string

	// testMu makes the requests wait for a test that runs. mu guards the fields
	// below. A shutdown takes mu only, so a held test call does not hold it.
	testMu sync.Mutex
	mu     sync.Mutex
	// state is "" until the first test passed, then controlShadow or
	// controlFallback.
	state string
	// tested is the end of the last test that gave a state.
	tested time.Time
	// failure is the kept text of a failed test, and failed is the end of that
	// test.
	failure string
	failed  time.Time
	// dir is the folder of this daemon, <runDir>/git-shadow/<name>, or "" while
	// there is none. lock is the open lock file in it.
	dir  string
	lock *os.File
	// closed is true after the shutdown removed the folder.
	closed bool

	// repoMu guards the folders of the repositories below dir, one request at a
	// time. repos has what the daemon knows of each folder that it made, by its
	// name. A folder that is not there starts again with nothing known
	// (syncRepo).
	// repoBusy counts the requests that run on each folder (syncRepo), and
	// useCount numbers the good uses (shadowRepo.used).
	repoMu   sync.Mutex
	repos    map[string]*shadowRepo
	repoBusy map[string]int
	useCount uint64

	// probeMu guards probes, the kept good answers of git about a repository, by
	// the root of the work tree (askGit).
	probeMu sync.Mutex
	probes  map[string]keptProbe

	// probeFolderMu is held while the daemon looks for the folder `.probe` and
	// makes it (ensureProbeFolder). A second request then never runs its call
	// on a folder that the first one is still making.
	probeFolderMu sync.Mutex

	// scanMu guards the kept answer of the `git version` call of the include
	// step: scanNew is the answer, when scanKept (includeScan).
	scanMu   sync.Mutex
	scanKept bool
	scanNew  bool
}

// newShadowHost returns the settings control of a daemon whose socket is in
// runDir. A relative runDir counts from the working directory of the daemon at
// this call, its start, and the control keeps the absolute form.
//
// With `-socket s.sock` and with `-socket rel/rpc.sock` 5fd08069 has the
// absolute form in every path of the settings copy: the argv, the working
// directory and the variables of the test calls, and the folder variables of
// the later calls. Its replies, call lists and copied texts equal those of the
// absolute socket path (cell R1 of val-s3p2b2-7-linux2, Linux VM). On a
// Windows VM with `-socket s.sock` its folder `git-shadow` is under the
// working directory, its test calls have absolute arguments, and its five
// replies are normal (table R1 of val-s3p2b2-7-windows). A working directory
// that the system does not give is not measured: the path stays as it is.
func newShadowHost(runDir string) *shadowHost {
	if runDir != "" {
		if abs, err := filepath.Abs(runDir); err == nil {
			runDir = abs
		}
	}
	return &shadowHost{runDir: runDir}
}

func (h *shadowHost) active() bool {
	return h != nil && h.runDir != ""
}

// control is the settingsControl value of a request, or "" for no member. It is
// the one place that answers which state a request has. off is the shadowDirOff
// member of the request.
//
// A request with the member gets off-switch, also as the first request of a
// daemon, in the "no repository" reply and while a failure is kept (s3-linux
// cells A1 and B1, s3p2a-edges cell X1 and s3p2b-edges-linux2 cell 32, Linux VM).
// A request without it gets the state of the daemon, and no member before the
// first test passed (s3-linux cell A1, Linux VM).
func (h *shadowHost) control(off bool) string {
	if !h.active() {
		return ""
	}
	if off {
		return controlOff
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// readsRepoConfig reports whether a request makes the daemon's own read of the
// repository config. In the state fallback-git 5fd08069 makes no such read and
// gives no config refusal: format version 2 gets the "no repository" answer
// (s3p2-oldgit cell O4, Linux VM).
func (h *shadowHost) readsRepoConfig() bool {
	return h.control(false) != controlFallback
}

// folderUse is how a request in the state fallback-git names its short-lived
// folders: in the folder of the daemon, with no copy of a repository. It is the
// zero value for a daemon in its shutdown or with no folder.
func (h *shadowHost) folderUse() gitUse {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.dir == "" {
		return gitUse{}
	}
	return gitUse{host: h, folder: h.dir}
}

// gate runs before the first git call of a method in a repository that the trust
// check accepted, or for git.info on a `.git` file to a missing path (gitInfo).
// It returns the text of a failed start test, or "".
//
// A request with the member runs no test and makes no folder (s3-linux cell B1,
// Linux VM). Without it, the first such request of a daemon runs the test. A
// failure is kept for shadowFailureKeep. In the state shadow no second test runs
// (s3p2b-edges-linux2 cell 28: none at 590 s, 610 s and 720 s). In the state
// fallback-git the test runs again after shadowRetestAfter.
//
// Each method reads the user's excludes file before it comes here. With a run
// folder that cannot take the folder, each of the five methods makes that one
// git call on 5fd08069 (s3p2b-edges-linux2 cell 30, Linux VM).
func (h *shadowHost) gate(off bool) string {
	if !h.active() || off {
		return ""
	}
	h.testMu.Lock()
	defer h.testMu.Unlock()

	if kept, done := h.decided(); done {
		return kept
	}
	state, cause := h.startTest()
	end := shadowClock()

	if state == "" && cause == "" {
		// The shutdown came first: no test ran.
		return ""
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if cause != "" {
		// The frame of every cause (s3p2b-edges-linux2 cells 26 and 30 to 32, Linux
		// VM. Cell 33 of that round on macOS and Windows VMs). A cause with
		// gitCannotRunPrefix is the whole text (cannotRunText).
		h.failure = cause
		if !strings.HasPrefix(cause, gitCannotRunPrefix) {
			h.failure = "Claude could not test git on the remote machine (" + cause + "). Check free disk space and quota there, then try again."
		}
		h.failed = end
		return h.failure
	}
	h.tested = end
	if state != h.state {
		h.state = state
		if state == controlShadow {
			logInfof(shadowStateLine)
		} else {
			logInfof(fallbackStateLine)
		}
	}
	return ""
}

// folderWithNoTest makes the folder of this daemon when it is not there, and
// runs no test. It is the step of a git.info on a host with no git (gitInfo).
// A folder that cannot be made is not measured: the request goes on.
func (h *shadowHost) folderWithNoTest() {
	if !h.active() {
		return
	}
	h.testMu.Lock()
	defer h.testMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.remakeFolder()
	}
}

// decided reports whether a request needs no test now. kept is then the text of
// a kept failure, or "". A daemon in its shutdown runs no test.
func (h *shadowHost) decided() (kept string, done bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return "", true
	}
	now := shadowClock()
	if h.failure != "" {
		if now.Sub(h.failed) < shadowFailureKeep {
			return h.failure, true
		}
		h.failure = ""
	}
	if h.state == controlShadow || (h.state == controlFallback && now.Sub(h.tested) <= shadowRetestAfter) {
		h.remakeFolder()
		return "", true
	}
	return "", false
}

// hostForm is the host name with each character outside A-Z, a-z, 0-9 and "."
// written as "_". On 5fd08069 a host name of the form aa-bb gives aa_bb, and a
// "." stays (s3-linux cell C6, Linux VM, and the macOS and Windows rounds).
// On a macOS VM the ".local" end of the host name stays (round task075-01).
// Other characters are claustrum's own (not measured). A host
// name that the system does not give is not measured: the form is then empty.
func hostForm() string {
	name, _ := shadowHostname()
	return strings.Map(func(r rune) rune {
		if r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return '_'
	}, name)
}

// shadowHostname is os.Hostname. A variable, so that a test can name the host.
var shadowHostname = os.Hostname

// makeFolder makes the folder of this daemon and takes its lock. The caller holds
// mu. The steps and their order are those of 5fd08069. First comes the folder of
// all daemons (0700), then the folder of this daemon (0700), then the lock file
// (0600, 0 bytes). The rows are s3p2-oldgit section 5 and the tmpfs rows of
// s3p2b-edges-linux2 cell 32 (Linux VM). The name is <host form>-<pid>-<16 hex>,
// and the hex part is new each time (s3-linux cells C6 and B6).
//
// The error of each step is the error of the system call with its path. 5fd08069
// answers `mkdir <run>/git-shadow: permission denied`, `mkdir <S>: no space left
// on device` and `open <S>/lock: no space left on device` (cells 30 and 32). On
// a Windows VM a file named git-shadow gives `mkdir <path>: The system cannot
// find the path specified.` (s3p2-windows cell C1), which is the text of
// os.MkdirAll for a path that is no folder.
//
// After a lock file that cannot be made, 5fd08069 leaves no folder of the daemon
// (the tmpfs row with 2 inodes). So the folder that this call made goes again
// here.
//
// After the lock, the sweep removes the folders of daemons that are gone
// (sweepOldFolders). 5fd08069 sweeps at the request that makes its folder, and
// not at the start of the daemon (cells G1 and G12 of round task075-01, Linux
// VM).
func (h *shadowHost) makeFolder() error {
	root := filepath.Join(h.runDir, shadowRootName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	name := hostForm() + "-" + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(salt[:])
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	lock, err := openShadowLock(filepath.Join(dir, shadowLockName))
	if err != nil {
		_ = h.removeEntry(name)
		return err
	}
	h.dir, h.lock = dir, lock
	h.sweepOldFolders(name)
	return nil
}

// openShadowLock makes the lock file of the daemon and takes the hold on it
// (openShadowLockFile, one per system). A variable, so that a test can fail the
// step.
var openShadowLock = openShadowLockFile

// remakeFolder makes the folder of this daemon again when it is gone. The caller
// holds mu, and the state is decided. After the driver removed the folder,
// 5fd08069 answered the next request normally and made the folder again, with
// the same pid and another hex part (s3-linux cell B6, Linux VM, and s3-macos
// cell B6). No test ran then. A folder that cannot be made again is not
// measured: the request goes on.
func (h *shadowHost) remakeFolder() {
	if h.dir != "" {
		if _, err := os.Lstat(h.dir); err == nil {
			return
		}
	}
	h.dropLock()
	h.dir = ""
	_ = h.makeFolder()
}

func (h *shadowHost) dropLock() {
	if h.lock != nil {
		_ = h.lock.Close()
		h.lock = nil
	}
}

// removeEntry deletes rel, a path below the folder of all daemons. rel is a
// name that this daemon made, or one that a directory read of git-shadow or of
// the folder of the daemon returned. It never comes from a request. The delete
// goes through a root at the folder of all daemons, so it follows no link out of
// it. wipesHomeDir on the full path is defense in depth.
func (h *shadowHost) removeEntry(rel string) error {
	base := filepath.Join(h.runDir, shadowRootName)
	if wipesHomeDir(filepath.Join(base, rel)) {
		return errors.New("refusing to delete a path that holds the home folder")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(rel)
}

// removeLink deletes name, a link that a directory read of the folder of all
// daemons returned. It is one plain remove through a root at that folder, so
// it deletes the link and never what the link names (sweepLink).
// wipesHomeDir on the full path is defense in depth.
func (h *shadowHost) removeLink(name string) error {
	base := filepath.Join(h.runDir, shadowRootName)
	if wipesHomeDir(filepath.Join(base, name)) {
		return errors.New("refusing to delete a path that holds the home folder")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Remove(name)
}

// close removes the folder of this daemon at a shutdown. 5fd08069 removes it at
// -stop and at server.shutdown, and the empty folder of all daemons stays
// (s3p2-linux section 1, Linux VM, and the macOS and Windows rounds). It removes
// the folder also while a git call of a request runs (s3p2-linux2 cell F3, Linux
// VM, and cell 48 of s3p2b-edges on macOS and Windows VMs). The folder of
// another daemon stays (s3p2b-edges-linux2 cell 44, Linux VM).
//
// The lock file is closed first: on Windows an open file cannot be removed.
func (h *shadowHost) close() {
	if !h.active() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.dropLock()
	if h.dir != "" {
		_ = h.removeEntry(filepath.Base(h.dir))
		h.dir = ""
	}
}

// shadowRun is the result of one call of the start test.
type shadowRun struct {
	out    string
	stderr string
	err    error
	// ended is true when shadowCallBound ended the call.
	ended bool
}

// detail is the text of a failed call with the last line that git wrote to its
// error output, or the exec error when git wrote none. The cause of a failed
// start test has every line (allLines). Each caller has a failed run.
func (r shadowRun) detail() string {
	lines := strings.Split(strings.TrimRight(r.stderr, "\r\n"), "\n")
	if last := strings.TrimRight(lines[len(lines)-1], "\r"); last != "" {
		return last
	}
	return r.err.Error()
}

// gitSaidNo reports whether a failed call of the loop of the start test is a
// refusal by git itself: an exit error of a process that no signal ended,
// inside the bound, whose error output does not hold `No space left`. Only
// such a failure can give the state fallback-git (shadowTestCalls). 5fd08069
// answers fallback-git after exit 128 with one error line and after exit 1
// with no output (cells C1, C4 and N1-control of round tier0-5, Linux VM). It
// fails the test after a call that signal 9 ended and after an error text
// with `No space left on device` (cells N3, N1 and N2 of round tier0-5, all at
// call 10). No other text is measured, so no other text counts. On Windows a
// call that another process killed reads as an exit, so the signal rule holds
// on Linux and macOS only (not measured on Windows).
func (r shadowRun) gitSaidNo() bool {
	return r.exited() && !r.ended && !strings.Contains(r.stderr, "No space left")
}

// exited reports whether the call failed with an exit code of its own: an exit
// error of a process that no signal ended.
func (r shadowRun) exited() bool {
	var exit *exec.ExitError
	return errors.As(r.err, &exit) && exit.Exited()
}

// shadowTestPrefix is the option list of every call of the test. On a Windows
// VM the first item is core.hooksPath=NUL (s3p2-linux and s3p2-windows cells
// B3a).
func shadowTestPrefix() []string {
	return []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "commit.gpgsign=false",
	}
}

// shadowTestNames are the variables that the test sets. The daemon's own
// entries of these names do not go to a test call.
var shadowTestNames = []string{
	"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "LC_ALL", "LANGUAGE", "GIT_TERMINAL_PROMPT",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
}

// shadowTestDropped are two variables of the daemon that 5fd08069 does not
// pass to a test call. With GIT_DEFAULT_HASH=sha256 in the daemon, its test
// repository is a SHA-1 repository and the test passes, and the later calls
// have the variable. With GIT_CONFIG_SYSTEM in the daemon the same holds
// (cells H1 and H3 of val-s3p2b2-15-linux2, Linux VM, one run each). With the
// first one in a test call, `git init` makes a SHA-256 repository and the
// fixed tree id of call 4 fails.
var shadowTestDropped = []string{"GIT_DEFAULT_HASH", "GIT_CONFIG_SYSTEM"}

// shadowDaemonEnv is the daemon's own environment as a test call gets it.
// claustrum drops the entries that it drops for its other own calls
// (gitVersionRun), the names that the test sets, so that the test stays in its
// own folder, and shadowTestDropped. Which other entries of the daemon
// 5fd08069 passes is not measured. The first `git version` call of
// shadowVersionNote and versionAfterFailure get it with no cleaning.
// shadowTestEnv cleans it for the test calls.
func shadowDaemonEnv() []string {
	env := withoutConfigCountSet(listingEnvOf(os.Environ()))
	return slices.DeleteFunc(env, func(kv string) bool {
		name := envName(kv)
		return slices.Contains(shadowTestNames, name) || slices.Contains(shadowTestDropped, name)
	})
}

// shadowCheckDropped are two more variables of the daemon that a call in the
// folder `.check-<n>` does not get. With both in the daemon, the nine such
// calls of 5fd08069 have neither, and call 7 and the excludes read have both.
// Only these two names are measured (cell C8 of round tier0-5, Linux VM).
var shadowCheckDropped = []string{"GIT_NAMESPACE", "GIT_INDEX_FILE"}

// shadowTestEnv is the environment of a test call with no folder variable: the
// nine variables of the test, then the daemon's own without shadowCheckDropped.
// The call log of 5fd08069 has this order on a Linux VM (s3p2-linux cell B3a).
// GIT_CONFIG_GLOBAL is /dev/null on a Windows VM too (s3p2-windows cell B3a).
// The daemon's part is cleaned as for every git child (gitChildEnv) and has no
// GIT_ name (withoutGitNames). 5fd08069 does both on the nine calls of the test
// (rounds task004-01 and task004-02, Linux, macOS and Windows VMs).
func shadowTestEnv() []string {
	daemon := slices.DeleteFunc(cleanGitChildEnv(shadowDaemonEnv(), gitEnvWindowsForm), func(kv string) bool {
		return slices.Contains(shadowCheckDropped, envName(kv))
	})
	daemon = withoutGitNames(daemon)
	return append([]string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LC_ALL=C",
		"LANGUAGE=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=c",
		"GIT_AUTHOR_EMAIL=c@c",
		"GIT_COMMITTER_NAME=c",
		"GIT_COMMITTER_EMAIL=c@c",
	}, daemon...)
}

// shadowGit runs one git call of the test in dir with env and the full argv.
//
// A call that exits in time counts by its exit code and its output so far,
// also when a child keeps its output pipes open past the wait of
// shadowCallDrain. With a child that holds the pipes of the `init` call for 5
// s, 5fd08069 starts the next test call 1.02 s and 1.06 s after the `init`
// ended, and the test passes (cell N-a of val-s3p2b2-8-linux2, Linux VM, 2 of
// 2 runs). The eight other calls with that wait get the same rule (not
// measured).
func shadowGit(dir string, env []string, argv ...string) shadowRun {
	return exitedInTime(boundedGit(shadowCallBound, dir, env, argv...))
}

// exitedInTime takes the error out of a run whose only failure is the wait
// for its output pipes: the process ended with exit 0 before that.
func exitedInTime(r shadowRun) shadowRun {
	if errors.Is(r.err, exec.ErrWaitDelay) {
		r.err = nil
	}
	return r
}

// boundedGit runs one git call of the daemon's own in dir with env and the
// full argv. The call ends at bound, and the wait for its output pipes after
// that ends at shadowCallDrain.
func boundedGit(bound time.Duration, dir string, env []string, argv ...string) shadowRun {
	return boundedGitInput(bound, dir, env, nil, argv...)
}

// boundedGitInput is boundedGit with stdin as the standard input of the call,
// or with none for nil.
func boundedGitInput(bound time.Duration, dir string, env []string, stdin []byte, argv ...string) shadowRun {
	return boundedGitDrain(bound, shadowCallDrain, dir, env, stdin, argv...)
}

// boundedGitDrain is boundedGitInput with drain as the wait for the output
// pipes.
//
// ended is true when the call failed and the bound has passed at its return.
// That holds for a process that the bound ended. It holds too for a call that
// exits in time while a child keeps its output pipes past the bound. The row
// of that second state is at call 7 in shadowTestCalls.
func boundedGitDrain(bound, drain time.Duration, dir string, env []string, stdin []byte, argv ...string) shadowRun {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = drain
	err := cmd.Run()
	return shadowRun{
		out:    strings.TrimRight(out.String(), "\r\n"),
		stderr: errOut.String(),
		err:    err,
		ended:  err != nil && ctx.Err() != nil,
	}
}

// startTest makes the folder of the daemon when there is none, runs the test in
// a new folder .check-<n> in it and removes that folder. It returns the state,
// or the cause of a failed test.
//
// The name .check-<n> has a decimal number that differs per attempt. The folder
// is gone at the reply, also after a failed test (s3p2-oldgit section 5 and
// s3p2b-edges-linux2 cells 29 and 31, Linux VM). A folder that cannot be made
// gives `mkdir <S>/.check-<n>: no space left on device` (cell 32).
func (h *shadowHost) startTest() (state, cause string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return "", ""
	}
	if h.dir != "" {
		if _, err := os.Lstat(h.dir); err != nil {
			h.dropLock()
			h.dir = ""
		}
	}
	if h.dir == "" {
		if err := h.makeFolder(); err != nil {
			h.mu.Unlock()
			return "", err.Error()
		}
	}
	dir := h.dir
	h.mu.Unlock()

	check, err := os.MkdirTemp(dir, ".check-")
	if err != nil {
		return "", err.Error()
	}
	defer func() {
		_ = h.removeEntry(filepath.Join(filepath.Base(dir), filepath.Base(check)))
	}()
	return shadowTestCalls(dir, check)
}

// shadowTestCalls runs the calls of the test. dir is the folder of the daemon and
// check is the folder of this attempt. The argv, the working directory and the
// variables of each call are those of the call log of 5fd08069 (s3p2-linux cell
// B3a, Linux VM. s3p2-windows cell B3a, Windows VM. Cell 33 of s3p2b-edges lists
// the same ten calls on a macOS VM).
//
// The folder `shadow` has the form of the copy that the daemon keeps for a
// repository. 5fd08069 writes its HEAD and its config two times
// (makeShadowTestCopy and fillShadowTestCopy).
func shadowTestCalls(dir, check string) (state, cause string) {
	realDir := filepath.Join(check, "real")
	gitDir := filepath.Join(realDir, ".git")
	shadowDir := filepath.Join(check, "shadow")
	plainEnv := shadowTestEnv()
	realEnv := append(slices.Clone(plainEnv), "GIT_DIR="+gitDir, "GIT_WORK_TREE="+realDir)
	shadowEnv := append(slices.Clone(realEnv),
		"GIT_COMMON_DIR="+shadowDir, "GIT_OBJECT_DIRECTORY="+filepath.Join(gitDir, "objects"))
	run := func(env []string, args ...string) shadowRun {
		return shadowGit(check, env, append(shadowTestPrefix(), args...)...)
	}
	// The cause of a failed call has every line of its error output, joined by
	// one space, or the exec error when there is none. For a `mktree` that
	// writes two lines 5fd08069 answers `git mktree failed under "<S>": error:
	// first forced line fatal: second forced line (git version 2.43.0)` (cell C6
	// of round tier0-5, Linux VM). A call 10 on a full disk has both lines of
	// git too (cell N2 of round tier0-5). The other calls share this path: not
	// measured.
	failed := func(verb string, r shadowRun) (string, string) {
		return "", fmt.Sprintf("git %s failed under %q: %s", verb, dir, r.allLines())
	}
	// A failed call of the calls 1 to 5 has the version text at the end of its
	// cause, after two `version` calls (shadowVersionNote). That is measured for
	// `init` and for `mktree`. The `config`, `commit-tree` and `update-ref`
	// calls share this path: claustrum's own, not measured.
	setupFailed := func(verb string, r shadowRun) (string, string) {
		_, cause := failed(verb, r)
		return "", cause + shadowVersionNote(check, plainEnv)
	}

	// Calls 1 to 5 make the repository `real` with one empty commit.
	if r := run(plainEnv, "init", "-q", "--template=", realDir); r.err != nil {
		return setupFailed("init", r)
	}
	if r := run(realEnv, "config", "helper.check", "real"); r.err != nil {
		return setupFailed("config", r)
	}
	if r := run(realEnv, "mktree"); r.err != nil {
		return setupFailed("mktree", r)
	}
	commit := run(realEnv, "commit-tree", "-m", "c", gitEmptyTree)
	if commit.err != nil {
		return setupFailed("commit-tree", commit)
	}
	id := commit.out
	if r := run(realEnv, "update-ref", "HEAD", id); r.err != nil {
		return setupFailed("update-ref", r)
	}

	if err := makeShadowTestCopy(gitDir, shadowDir); err != nil {
		return "", err.Error()
	}

	// Call 6, the first `config --get` on the copy. A call that works and
	// prints `real` gives the state fallback-git, and no other test call runs.
	// 5fd08069 answers so with 6 test calls (cell C3b of round tier0-5, Linux
	// VM). After a call 6 that exits 1 with no output, 5fd08069 runs all ten
	// calls and the state is shadow (cell C3 of round tier0-5). Another failure
	// of a call 6 that exited by itself goes on too. So does a call 6 that
	// works with a text that is neither `real` nor `shadow`. Both are
	// claustrum's own, not measured. A call 6 that the bound ended fails the
	// test, as every ended test call but call 7. So does a call 6 that a signal
	// ended, or that did not start. Both are claustrum's own, not measured. On
	// Windows a call that another process killed reads as an exit, so it goes
	// on there.
	// Between call 6 and call 8 the daemon lists the config of `real` from
	// standard input, as it does for a repository.
	get := []string{"config", "--get", "helper.check"}
	first := run(shadowEnv, get...)
	if first.err != nil && (first.ended || !first.exited()) {
		return failed("config", first)
	}
	if first.err == nil && first.out == "real" {
		return controlFallback, ""
	}
	text, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return "", err.Error()
	}
	// Call 7 ends at the bound of a test call, and it has no short wait for its
	// output pipes. The rows are cells N-b and N-c of val-s3p2b2-8-linux2 (Linux
	// VM, 2 of 2 runs each):
	//
	//   - A child holds the pipes for 5 s after the call ended: 5fd08069 starts
	//     the next call at 5.345 s and 5.189 s, and the test passes.
	//   - The call itself runs 29 s: the test passes. It runs 40 s: 5fd08069
	//     ends it at 30 s, and the cause is the text of callSevenEnded.
	//
	//   - The call exits 0 at 0.2 s and a child holds the pipes for 35 s: 5fd08069
	//     answers the text of callSevenEnded at 31.2 s (cell N-b of
	//     val-s3p2b2-9-linux2, Linux VM, 1 run). The reply comes about 31 s
	//     after call 7 started. The row does not tell a bound from the start
	//     of the call from a bound after its end.
	//
	// A call 7 that fails on its own is followed by one `git version` call
	// (cannotRunText). If that call fails too, its text is the whole failure.
	// Else the cause is repoConfigPrefix and the error lines of call 7. A call 7
	// with exit 128 under a broken ~/.gitconfig gives the first form, and one
	// with exit 1 and `boom` gives `this repository's git config cannot be
	// used: boom` (cells E1 to E5 and E9 of round task083-01, Linux VM). A call
	// 7 with no error text gives its exec error there (claustrum's own, not
	// measured).
	listed := exitedInTime(boundedGitDrain(shadowCallBound, shadowCallBound+shadowCallDrain, gitVersionDir,
		repoConfigListEnv(), text, "config", "--no-includes", "--file", "-", "--list", "-z"))
	if listed.ended {
		return "", callSevenEnded(filepath.Join(gitDir, "config"))
	}
	if listed.err != nil {
		if whole := cannotRunText(); whole != "" {
			return "", whole
		}
		return "", repoConfigPrefix + listed.allLines()
	}
	if err := fillShadowTestCopy(gitDir, shadowDir, []byte(listed.out)); err != nil {
		return "", err.Error()
	}
	// In claustrum calls 8, 9 and 10 are the three steps of one loop, and one
	// rule judges each of them. The replies and call logs of 5fd08069 in cells
	// C1, C2, C4, C5, C9, N1, N2 and N3 of round tier0-5 (Linux VM) fit it:
	//
	//   - The call works and its answer passes: the next step. After the last
	//     step the state is shadow.
	//   - The call works and its answer does not pass: the state is
	//     fallback-git, and no second call runs (cells C4, C2 and C5 of round
	//     tier0-5, with 8, 9 and 10 test calls).
	//   - The call fails: the second call runs, which is the same call on `real`
	//     alone. If it works and git itself refused the first one (gitSaidNo),
	//     the state is fallback-git (cells C4, C1 and N1-control of round
	//     tier0-5). In every other case the test failed, and the cause names
	//     the first call (cells C9, N1, N2 and N3 of round tier0-5).
	//
	// Two failed calls are measured at call 9 only (cell C9 of round tier0-5).
	// The `No space left` and signal rows are at call 10 only. The other steps
	// share the path: not measured.
	//
	// git 2.11.4 and 2.12.5 exit 1 with no output at call 9, and the second
	// call works there: the state is fallback-git (s3p2-oldgit section 4, Linux
	// VM). A call that the bound ended is no refusal by git, so the test failed
	// with the text of that call also when the second call works. For an ended
	// add 5fd08069 answers so on a Windows VM, with `exit status 1` as the text
	// (val-s3p2b2-1-windows cell Q-S4h-hold32-worktree-add).
	//
	// The answer test of call 10 is that the folder `wt` is there after the
	// call. In cell C5 of round tier0-5 the call exits 0 and makes nothing. No
	// cell has an add that exits 0 and makes a part of a worktree. The test for
	// the folder is claustrum's own.
	verify := []string{"rev-parse", "--verify", "-q", "HEAD^{commit}"}
	wt := filepath.Join(check, "wt")
	add := func(leaf string) []string {
		return []string{"worktree", "add", "--no-checkout", "--detach", leaf, id}
	}
	steps := []struct {
		verb   string
		args   []string
		second []string
		passes func(shadowRun) bool
	}{
		{"config", get, get, func(r shadowRun) bool { return r.out == "shadow" }},
		{"rev-parse", verify, verify, func(r shadowRun) bool { return r.out == id }},
		{"worktree", add(wt), add(filepath.Join(check, "wt2")), func(shadowRun) bool {
			_, err := os.Lstat(wt)
			return err == nil
		}},
	}
	for _, s := range steps {
		r := run(shadowEnv, s.args...)
		if r.err == nil {
			if !s.passes(r) {
				return controlFallback, ""
			}
			continue
		}
		if second := run(realEnv, s.second...); second.err == nil && r.gitSaidNo() {
			return controlFallback, ""
		}
		return failed(s.verb, r)
	}
	return controlShadow, ""
}

// callSevenEnded is the cause of a start test whose call 7 the bound ended.
// path is the config of the test repository. 5fd08069 answers `Claude could
// not test git on the remote machine (this repository's git config cannot be
// used: git did not finish reading "<S>/.check-<n>/real/.git/config" within
// 30 seconds; try again). Check free disk space and quota there, then try
// again.` (cell N-c of val-s3p2b2-8-linux2, Linux VM, 2 of 2 runs).
func callSevenEnded(path string) string {
	return repoConfigPrefix + readStopText(path)
}

// readStopText is the cause of a listing of the config file at path that its
// bound of 30 s ended (callSevenEnded, repoReadBound).
func readStopText(path string) string {
	return "git did not finish reading " + quoteOperand(path) + " within 30 seconds; try again"
}

// shadowVersionNote is the end of the cause after a failed call of the calls
// 1 to 5: the answer of `git version` in brackets. After a failed `init` or
// `mktree`, 5fd08069 runs `git version` two times before the reply. After an
// `init` that the bound ended its cause ends with ` (git version 2.43.0)`
// (s3p2b-edges-linux2 cell 31, Linux VM). After an `init` that exits 1 with a
// text it is `git init failed under "<S>": fatal: forced failure by the test
// wrapper (git version 2.43.0)` (cell C2 of val-s3p2b2-7-linux2, Linux VM, 2
// of 2 runs and the control). On a Windows VM the same cause ends with ` (git
// version 2.55.0.windows.3)`, with the same four calls (table C2 of
// val-s3p2b2-7-windows, 2 of 2 runs). After a `mktree` that exits 1 the cause
// has the same end, and the request has 5 test calls (cell C6 of round
// tier0-5, Linux VM). The first version call has no option list, the root
// folder as its working directory and no added variable. The second has the
// option list, the folder and the variables of a test call. Which of the two
// gives the text is not measured.
//
// The calls from call 6 on get no such end. Four rows have none: a failed
// `worktree add` (cell 26 of s3p2b-edges-linux2 and cell N1 of round
// tier0-5), an ended `rev-parse --verify` (cell 31 of s3p2b-edges-linux2) and an
// ended `worktree add` (cell W8a of s3b-linux). No record has a failed
// `config`, `commit-tree` or `update-ref` call of the calls 1 to 5. Those
// three share the path of `init` and `mktree`: not measured (setupFailed in
// shadowTestCalls).
func shadowVersionNote(check string, env []string) string {
	first := shadowGit(gitVersionDir, shadowDaemonEnv(), "version")
	second := shadowGit(check, env, append(shadowTestPrefix(), "version")...)
	text := second.out
	if text == "" {
		text = first.out
	}
	if text == "" {
		return ""
	}
	return " (" + text + ")"
}

// The first texts of the folder `shadow` of the test, and the end of its config
// in both texts. 5fd08069 writes these bytes on Linux, macOS and Windows VMs
// (val-s3p2b2-1-linux section 1.3, val-s3p2b2-1-macos rows D1 and D2,
// val-s3p2b2-1-windows row D3).
const (
	shadowTestFirstHead   = "ref: refs/heads/.invalid\n"
	shadowTestFirstConfig = "[core]\n\trepositoryformatversion = 0\n" + shadowTestHelper
	shadowTestHelper      = "[helper]\n\tcheck = shadow\n"
)

// makeShadowTestCopy makes the folder `shadow` of the test for the repository
// at gitDir, before call 6. It holds HEAD and config with their first texts
// and the three empty folders. The call also makes the folder `worktrees` of
// the repository. Where the system links (shadowLinksWorktrees), `shadow` gets
// the link `worktrees` to it.
//
// A poll of 2 ms on a Linux VM saw all of these at one time, before call 6, on
// 5fd08069: HEAD with 25 bytes, config with 61 bytes, `info`, `objects`, `refs`
// (0700), the link, and `real/.git/worktrees` with mode 0700
// (val-s3p2b2-1-linux, cells s3-first-* and s6-*). The macOS VM shows the same
// entries and modes. On a Windows VM `real\.git\worktrees` is there as an empty
// folder before call 10, and the copy has no link (val-s3p2b2-1-windows row
// D3).
func makeShadowTestCopy(gitDir, shadowDir string) error {
	if err := os.Mkdir(shadowDir, 0o700); err != nil {
		return err
	}
	if err := writeShadowTestFiles(shadowDir, []byte(shadowTestFirstHead), []byte(shadowTestFirstConfig)); err != nil {
		return err
	}
	for _, name := range []string{"info", "objects", "refs"} {
		if err := os.Mkdir(filepath.Join(shadowDir, name), 0o700); err != nil {
			return err
		}
	}
	target := filepath.Join(gitDir, worktreesSubdir)
	if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !shadowLinksWorktrees {
		return nil
	}
	return os.Symlink(target, filepath.Join(shadowDir, worktreesSubdir))
}

// fillShadowTestCopy writes the second texts of the folder `shadow` of the
// test. HEAD gets the text of the HEAD of the repository at gitDir. The config
// gets the text of the copied settings for listing, the listing of the config
// of that repository (shadowConfigText), and then the helper.check block. On
// 5fd08069 the second config has 181 bytes on a Linux VM and 237 on a Windows
// VM, and the second HEAD is `ref: refs/heads/master` and a newline (the rows
// of makeShadowTestCopy).
//
// The step of this write is not fixed by a record. On gits 2.11.4 and 2.12.5
// the test ends at call 9, and the poll saw both texts there
// (val-s3p2b2-1-linux cells s6-*). So the write comes before the end of call
// 9. claustrum writes right after call 7, whose listing the text needs, and
// before call 8. Each of the two files is a new file then, with a new inode
// (writeShadowTestFiles).
func fillShadowTestCopy(gitDir, shadowDir string, listing []byte) error {
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return err
	}
	text := shadowConfigText(listedSettings(listing), nil) + shadowTestHelper
	return writeShadowTestFiles(shadowDir, head, []byte(text))
}

// writeShadowTestFiles writes HEAD and config of the folder `shadow` of the
// test as new files (writeNewFile). On 5fd08069 both files have a new inode
// number in their second state, in 14 of 14 cells (val-s3p2b2-3-linux2 row D5,
// Linux VM), and a poll saw `.check-<n>/shadow/.tmp-<digits>` (val-s3p2b2-3-macos
// row D7, macOS VM).
func writeShadowTestFiles(shadowDir string, head, config []byte) error {
	root, err := os.OpenRoot(shadowDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := writeNewFile(root, "HEAD", head); err != nil {
		return err
	}
	_, err = writeNewFile(root, "config", config)
	return err
}

// settingsGateRefusal is the refusal text of a git method before its first git
// call in the repository of the trust verdict t, or "". noRepo is true when the
// method answers as if the folder held no repository. The order of a first
// request is that of 5fd08069: the start test, the call that asks git about the
// repository (askGit), the daemon's own read of the config with its included
// files, and then the folder of the repository. The call log of a first git.info
// has the ten test calls, that one call, the reads and then the first listing
// (s3p2-linux cell B3a and cell 1 of s3p2b-edges2-linux, Linux VM).
//
// A bad answer of git ends the gate before the read: the request makes no
// other git call, and the repository gets no folder (cell 39 of
// s3p2b-edges-linux2). For git's own refusal of the repository the method gives
// its "no repository" answer, and for every other bad answer the text. Each
// method gives that text the shape of a failed start test (cells 38 and 39).
//
// The daemon makes no call of its own about the owner for a refused config,
// and it uses no kept refused read: 5fd08069 reads the config again in each
// request (repoConfigGate). A server with no run folder has no call of askGit,
// and it reads the config the same way.
//
// The test runs for a trusted git directory. A folder in which the daemon
// finds no usable git folder starts no test and makes no folder (s3p2-linux
// cell E5 and s3-linux cell A1, Linux VM). git.info on a `.git` file to a
// missing path is the exception (gitInfo). Each method gives the text of a
// failed test the shape that it gives a refused config: the error object on
// git.info, git.status and git.list_branches, and the result object on
// git.worktree_create and git.worktree_remove (s3p2b-edges-linux2 cell 30, Linux
// VM). With worktreeRoot a remove gives it as the reason of its work-tree
// refusal (externalWorkTreeRefusal): that shape is not measured.
//
// In the state fallback-git the read of the config does not run
// (readsRepoConfig), and the daemon keeps no folder of a repository: its own
// folder holds the lock file only there (s3p2-oldgit section 5, Linux VM).
func (req *request) settingsGateRefusal(t gitDirTrust, off jsonTrueSwitch, dir string) (msg string, noRepo bool) {
	req.gitRefused, req.configRefused = false, false
	// git.info and git.list_branches skip the trust check with a GIT_COMMON_DIR
	// in the daemon environment (requestGitDirTrust). With a GIT_DIR there too,
	// the request still gets its copy (daemonCommonDirStays). The gate then
	// takes the verdict of the check for the copy alone.
	if !bool(off) && t.verdict == gitDirTrusted && t.pinCommonDir == "" && daemonCommonDirSet() && !daemonCommonDirStays() {
		if judged := gitDirTrustFor(dir); judged.verdict == gitDirTrusted {
			t = judged
		}
	}
	if t.verdict == gitDirTrusted {
		if msg := req.shadow.gate(bool(off)); msg != "" {
			return msg, false
		}
	}
	if !req.shadow.readsRepoConfig() {
		// The state fallback-git: the request has no copy, and its short-lived
		// folders are in the folder of the daemon (gitUse.inFolder).
		if !off && t.verdict == gitDirTrusted {
			req.use = req.shadow.folderUse()
			req.use.atReply = &req.atReply
			req.use.stop = req.stop
		}
		return "", false
	}
	var read *repoVerdict
	if req.shadow.asksGit(t, bool(off)) {
		if a := req.shadow.askGit(t, dir); a.bad() {
			req.gitRefused = true
			return a.text, a.noRepo
		}
	}
	msg, read = repoConfigGate(t, off, req.stop)
	req.configRefused = msg != ""
	if msg != "" || read == nil || req.shadow.control(bool(off)) != controlShadow {
		return msg, false
	}
	// The repository is accepted: the daemon makes its folder or brings it up to
	// date (syncRepo), and the git calls of the request name it from here on
	// (gitUse). If that step fails, the request is refused, so no call runs
	// against a folder that is not complete (shadowRepoFailureText).
	req.repoRead = read
	use, err := req.shadow.syncRepo(t.pinCommonDir, read)
	if err != nil {
		return shadowRepoFailureText(err), false
	}
	req.heldRepos = append(req.heldRepos, filepath.Base(use.repoDir))
	req.use = use
	req.use.atReply = &req.atReply
	req.use.stop = req.stop
	return "", false
}

// shadowRepoFailureText is the refusal of a request whose folder of the
// repository cannot be made or brought up to date. 5fd08069 answers this text
// for a folder that cannot be made, with the error of the mkdir as the cause
// (syncRepoFolder has the three measured causes). The cells are N4 of
// val-s3p2b2-3-linux2, val-s3p2b2-3-macos and val-s3p2b2-3-windows. The same
// request again gets the same line with no git call, and the next request
// after the cause is gone is normal, with no new read of the config. Both
// follow here from the kept read and the kept answer of git: nothing of the
// failure is kept. Every other failure of the step gets the same frame with
// its own error text (not measured). A repoLimitError is the whole text.
func shadowRepoFailureText(err error) string {
	var full repoLimitError
	if errors.As(err, &full) {
		return full.Error()
	}
	return "Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: " + err.Error()
}
