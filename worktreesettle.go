package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The settle step of git.worktree_create. After a good read-tree checkout 683cc6a9
// runs `diff-files --quiet --ignore-submodules=all` with the working directory and
// the GIT_INDEX_FILE of the checkout. On Windows the argv carries
// --git-dir=<repo>\.git and --work-tree=<leaf>. Exit 0 answers at once. Exit 1
// makes the reply wait. Measured on Linux, macOS and Windows VMs.
var (
	// settleMargin is the time that must still be left before the deadline of
	// timeoutMs for the call to run. The call then gets that time less the
	// margin, and at least settleMinLimit. With timeoutMs 10000 683cc6a9 makes no
	// call. With 11500 the call gets SIGTERM 0.983 s after its start. With 15000,
	// 19999 and 20000 a call of 3 s runs to its end (Linux VM). The exact limit
	// between 11500 and 15000 is not measured. The 10 s margin and the 1 s minimum
	// are claustrum's own.
	settleMargin   = 10 * time.Second
	settleMinLimit = time.Second
	// settleWait is the time after the end of the read-tree call before which the
	// reply does not come when the call exits 1. 683cc6a9 answers 0.80 s (Linux),
	// 0.39 s (macOS) and 0.976 s (Windows) after the start of the call. On macOS
	// the reply comes 1.2 s after the start of the read-tree call. After a call of
	// 3 s that exits 1 the reply comes at once (Linux). So the wait ends at a time
	// that is fixed before the call, not after it. The anchor at the end of the
	// read-tree call and the 1.1 s are claustrum's own, not measured.
	settleWait = 1100 * time.Millisecond
	// settleWaitWhole takes the place of settleWait when the Lstat of `.git` in the
	// leaf fails or its mtime has no nanoseconds (settleWaitFor). The rule and the
	// 2.2 s are claustrum's own, not measured.
	settleWaitWhole = 2200 * time.Millisecond
)

// settleWaitFor is the wait for the new worktree leaf: settleWait when an Lstat of
// `<leaf>/.git` succeeds and its mtime has a nanosecond part other than 0, else
// settleWaitWhole. The Lstat follows no link at `.git` (claustrum's own, not
// measured). A variable, so a test can set the wait of a create.
var settleWaitFor = func(leaf string) time.Duration {
	fi, err := os.Lstat(filepath.Join(leaf, ".git"))
	if err != nil || fi.ModTime().Nanosecond() == 0 {
		return settleWaitWhole
	}
	return settleWait
}

// settleSleep waits until the time until, or until ctx ends, which comes first
// (claustrum's own, not measured).
func settleSleep(ctx context.Context, until time.Time) {
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// settleLimit is the time limit of the settle call under ctx, the deadline of the
// caller. ok is false when less than settleMargin is left: no call runs then. The
// default limit is that of the other calls of the request (s.limit), so the wide
// limit holds while a "shadowDirOff":true request runs. A nil s gives gitCallLimit.
// With timeoutMs the limit is the lower of the default and the time left less the
// margin, and at least settleMinLimit (claustrum's own, not measured).
func settleLimit(ctx context.Context, s *gitStop) (limit time.Duration, ok bool) {
	def := gitCallLimit
	if s != nil {
		def, _ = s.limit(callLimit)
	}
	deadline, set := ctx.Deadline()
	if !set {
		return def, true
	}
	left := time.Until(deadline)
	if left < settleMargin {
		return 0, false
	}
	return max(min(def, left-settleMargin), settleMinLimit), true
}

// settleCheckout runs the settle step after a good checkout, before the index goes
// into the registration. readEnd is the time when the read-tree call ended. The step
// changes no reply and starts no rollback: 683cc6a9 answers success also after a call
// that it stopped (683cc6a9, measured on Linux at timeoutMs 11500). The step runs no
// delete (claustrum's own).
//
// When the --attr-source probe of attrSourceArgs answers "not supported", no call
// runs. On 683cc6a9 the probe exits 129, `git version` follows, and no diff-files
// call comes (Linux VM). The rows quote no full argv, so its form is claustrum's
// own. The option comes first. Then comes the form of the light calls of a
// create: the items of a `config.worktree` file (gitUse.checkoutFront), the light
// profile, and the --git-dir and --work-tree of the checkout. The environment is
// the light environment of the checkout without its hook pins, filter items and
// LANGUAGE=C (claustrum's own).
//
// The stop is the one of gitRun on each system, SIGTERM on Linux and macOS, and a
// kill gitStopGrace later. It does not go through the gitStop of the request, so no
// later call of the create stops.
//
// The call starts as the other hardened calls start (hardenedGitCmd, gitRun): with no
// process group or job of its own. Only the read-tree checkout has one. stdout and
// stderr go to the null device, not to a pipe of the daemon, so no drain applies and
// a filter child of git that outlives the call does not hold the reply. Such a child
// gets no signal, as a child of the other calls gets none (claustrum's own).
func settleCheckout(callerCtx context.Context, u gitUse, leaf, workTree, gitDir, indexFile string, pin []string, readEnd time.Time) {
	limit, ok := settleLimit(callerCtx, u.stop)
	if !ok {
		return
	}
	attr := attrSourceArgs()
	if attr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	args := append(attr, withFront(u.checkoutFront(gitDir), hardenedProfileArgs(false,
		"--git-dir="+gitDir, "--work-tree="+workTree, "diff-files", "--quiet", "--ignore-submodules=all"))...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = leaf
	cmd.Env = append(hardenedGitEnv(false, pin, nil), "GIT_INDEX_FILE="+indexFile)
	cmd.Cancel = func() error { return stopGitProcess(cmd.Process) }
	cmd.WaitDelay = gitStopGrace
	err := runHeld(cmd, cmd.Run)
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return
	}
	settleSleep(callerCtx, readEnd.Add(settleWaitFor(leaf)))
}
