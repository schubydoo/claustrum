package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The branch step of git.worktree_remove and of the git.worktree_create rollbacks.
// Since 89cb6289 the daemon deletes the branch only when another local branch or a
// remote-tracking ref reaches its tip. Else it keeps the branch. docs/PROTOCOL.md →
// "The branch step" gives each rule with its rows and VMs.

// branchListLimit is the --count of the for-each-ref call. A listing of that many
// records keeps the branch, and no further call runs (rows R15a and R15c). With 10000
// heads the whole check runs (row R15b).
const branchListLimit = 10001

var (
	// branchCheckBound bounds for-each-ref, rev-parse --verify and rev-list together.
	// At the bound the running call is killed, and the branch is kept. Rows R22a,
	// B2-01, B2-02 and B2-09a show about 60 s and no SIGTERM. The bound starts before
	// the config listing of for-each-ref, and it covers rev-parse. Both are
	// claustrum's choice (not measured).
	branchCheckBound = 60 * time.Second
	// updateRefStop is when update-ref gets SIGTERM on Linux and macOS. On Windows
	// the call is killed then (rows R23a, R23a1 and B2-09e). The time counts from the
	// start of update-ref, after its config listing (row B2-09e).
	updateRefStop = 5 * time.Second
	// updateRefKillAfter is how long an update-ref that outlives the SIGTERM runs
	// before it is killed (rows R23b and R23b1: gone at about 10 s).
	updateRefKillAfter = 5 * time.Second
)

// branchOutcome is what the branch step did with the branch.
type branchOutcome int

const (
	// branchNotRun: the name is skipped, so no call ran.
	branchNotRun branchOutcome = iota
	// branchDeleted: update-ref deleted the branch.
	branchDeleted
	// branchAbsent: the branch was not listed, and rev-parse --verify exited 1.
	branchAbsent
	// branchUnique: rev-list printed a commit that no other ref reaches.
	branchUnique
	// branchUnchecked: the check did not finish. err is the process error of the
	// failed or stopped call. It is nil when no call failed. That is the listing
	// limit, a letter-case twin, a listing that does not parse, or rev-parse exit 0.
	branchUnchecked
	// branchUpdateRefFailed: update-ref failed or was stopped. err is its process error.
	branchUpdateRefFailed
)

// branchStepResult is the outcome of the branch step and the process error that
// caused it, if any.
type branchStepResult struct {
	kind branchOutcome
	err  error
}

// kept reports whether the step kept the branch. It is true unless the step deleted
// the branch, found it absent, or ran no call. git.worktree_remove then adds
// "branchKept":true to its reply (rows R02, R14, R15a, R16, R18 and R22a).
func (r branchStepResult) kept() bool {
	return r.kind == branchUnique || r.kind == branchUnchecked || r.kind == branchUpdateRefFailed
}

// skippedBranchName reports whether the branch step runs no call for branch. That is
// an empty name, or one that starts with "-" or "+" (rows R12e, R12d and R12p).
func skippedBranchName(branch string) bool {
	return branch == "" || branch[0] == '-' || branch[0] == '+'
}

// runBranchStep checks refs/heads/<branch> and deletes it when another ref reaches
// its tip. The check and the delete run one at a time per common git dir (rows R21,
// R21b and B2-12). Different repositories run at the same time (row B2-08).
func runBranchStep(repo, branch string) branchStepResult {
	if skippedBranchName(branch) {
		return branchStepResult{kind: branchNotRun}
	}
	unlock := lockWorktreeBranchStep(repo)
	defer unlock()
	if branchStepEntered != nil {
		branchStepEntered(repo)
	}
	return checkAndDeleteBranch(repo, branch)
}

// branchStepEntered is a test seam. It runs inside the branch lock, before the
// check. Production leaves it nil.
var branchStepEntered func(repo string)

// branchRecord is one record of the for-each-ref listing.
type branchRecord struct {
	object, name, symref string
}

// parseBranchRecords splits the for-each-ref output. Each record is the object name,
// NUL, the refname, NUL, the symref target, and a newline. A record of another shape
// fails the parse. Then the branch is kept: claustrum's choice (not measured).
func parseBranchRecords(out string) ([]branchRecord, bool) {
	if out == "" {
		return nil, true
	}
	var recs []branchRecord
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 3 {
			return nil, false
		}
		recs = append(recs, branchRecord{object: f[0], name: f[1], symref: f[2]})
	}
	return recs, true
}

// checkAndDeleteBranch takes the steps of the check in the measured order:
//
//  1. for-each-ref fails or is stopped: keep.
//  2. The listing holds branchListLimit records: keep.
//  3. A listed refname differs from the target only in letter case: keep (rows R16
//     and R16b). The fold is strings.EqualFold, which also folds letters beyond
//     ASCII. That is claustrum's choice (not measured).
//  4. The target is not listed: rev-parse --verify. Exit 1 means the branch is
//     absent, and nothing else runs (rows R11 and B2-09d). A stopped call counts as a
//     failure, also on Windows, where the kill gives exit code 1. Exit 0, or any
//     failure, keeps the branch. That is claustrum's choice (not measured).
//  5. rev-list, with one ^<refname> stdin line for each other listed record that is
//     not a symref. A printed commit keeps the branch, and so does a failed or
//     stopped rev-list.
//  6. update-ref with the old value. A failure or a stop keeps the branch.
func checkAndDeleteBranch(repo, branch string) branchStepResult {
	target := "refs/heads/" + branch
	ctx, cancel := context.WithTimeout(context.Background(), branchCheckBound)
	defer cancel()
	out, _, err := branchCheckGit(ctx, repo, nil, "for-each-ref", "--count="+strconv.Itoa(branchListLimit),
		"--format=%(objectname)%00%(refname)%00%(symref)", "refs/heads/")
	if err != nil {
		return branchStepResult{kind: branchUnchecked, err: err}
	}
	recs, ok := parseBranchRecords(out)
	if !ok || len(recs) >= branchListLimit {
		return branchStepResult{kind: branchUnchecked}
	}
	tip, listed := "", false
	for _, r := range recs {
		if r.name == target {
			tip, listed = r.object, true
		} else if strings.EqualFold(r.name, target) {
			return branchStepResult{kind: branchUnchecked}
		}
	}
	if !listed {
		_, stopped, err := branchCheckGit(ctx, repo, nil, "rev-parse", "--verify", "--quiet", target+"^{commit}")
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && !stopped {
			return branchStepResult{kind: branchAbsent}
		}
		return branchStepResult{kind: branchUnchecked, err: err}
	}
	out, _, err = branchCheckGit(ctx, repo, strings.NewReader(branchStdin(recs, target)),
		"-c", "core.commitGraph=false", "rev-list", "-n", "1", tip, "--not", "--remotes", "--not", "--stdin", "--")
	if err != nil {
		return branchStepResult{kind: branchUnchecked, err: err}
	}
	if strings.TrimSpace(out) != "" {
		return branchStepResult{kind: branchUnique}
	}
	if err := deleteBranchRef(repo, target, tip); err != nil {
		return branchStepResult{kind: branchUpdateRefFailed, err: err}
	}
	return branchStepResult{kind: branchDeleted}
}

// branchStdin is the stdin of rev-list: one "^<refname>" line for each listed record
// in listing order. The target and a record with a symref target are left out (rows
// R01, R04, R08, R21b and B2-05). The stdin is empty when the target is the only
// listed branch (rows B2-04a and B2-04b).
func branchStdin(recs []branchRecord, target string) string {
	var b strings.Builder
	for _, r := range recs {
		if r.name != target && r.symref == "" {
			b.WriteString("^" + r.name + "\n")
		}
	}
	return b.String()
}

// withGitTimeout adds the D5 deadline to ctx when gitTimeout is on. Each call of the
// branch step, with its config listing, gets its own D5 deadline, as a hardenedGit
// call does. With D5 off, ctx is returned as it is.
func withGitTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if gitTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, gitTimeout)
}

// branchStepCmd builds one call of the branch step: the light -c set, the working
// directory repo, and branchStepEnv with the hooks of the config listing that the
// caller runs first.
func branchStepCmd(ctx context.Context, repo string, hooks []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(false, args...)...)
	cmd.Dir = repo
	cmd.Env = branchStepEnv(commonDirPinEnv(repo), hooks)
	return cmd
}

// branchCheckGit runs the config listing and then one check call under the shared
// bound ctx, and returns the stdout of the call. At the bound the running process
// is killed. stopped is true when the bound or the D5 deadline had ended when the
// call returned. On Windows the kill gives exit code 1, so exit 1 alone does not
// tell a stop from git's answer. Only rev-list gets a stdin. The other calls get the
// null device. 89cb6289 gives them a character device (macOS VM).
func branchCheckGit(ctx context.Context, repo string, stdin io.Reader, args ...string) (out string, stopped bool, err error) {
	ctx, cancel := withGitTimeout(ctx)
	defer cancel()
	// A listing that says "not a git repository" ends the step: the call does not run,
	// and the listing's error stands for it. 89cb6289 runs no for-each-ref after such a
	// listing (rows L14a, L14b, L14d, L14e and N01 to N04 on Linux and macOS VMs).
	pre := hookPrecursor(ctx, repo, false)
	if pre.saysNoRepository() {
		return "", ctx.Err() != nil, pre.err
	}
	cmd := branchStepCmd(ctx, repo, pre.hooks(), args...)
	cmd.Stdin = stdin
	b, err := cmd.Output()
	return string(b), ctx.Err() != nil, err
}

// deleteBranchRef runs the config listing and then `update-ref --no-deref -d <target>
// <tip>`. The old value makes git refuse the delete when the branch moved after the
// check. The stop at updateRefStop counts from the start of update-ref. Then the
// call gets stopUpdateRef. On Linux and macOS a call still running
// updateRefKillAfter later is killed.
func deleteBranchRef(repo, target, tip string) error {
	d5, cancelD5 := withGitTimeout(context.Background())
	defer cancelD5()
	hooks := hookPrecursor(d5, repo, false).hooks()
	ctx, cancel := context.WithTimeout(d5, updateRefStop)
	defer cancel()
	cmd := branchStepCmd(ctx, repo, hooks, "update-ref", "--no-deref", "-d", target, tip)
	cmd.Cancel = func() error { return stopUpdateRef(cmd.Process) }
	cmd.WaitDelay = updateRefKillAfter
	return cmd.Run()
}

// branchLockPresent reports whether refs/heads/<branch>.lock exists in the common git
// dir of repo. The rollback text of a failed delete names such a lock (row C07b).
// claustrum tests for the file after the failure. The rows do not tell whether the
// references test the file or read git's stderr.
func branchLockPresent(repo, branch string) bool {
	_, err := os.Lstat(filepath.Join(verifyGitDir(repo), "refs", "heads", filepath.FromSlash(branch)+".lock"))
	return err == nil
}

// rollbackBranchText is the part of a git.worktree_create undo text that the branch
// step adds. It is "" when the branch went, was absent, or was never made. A branch
// whose name the skip rule leaves alone gets a text too. leafLeft is true when the
// leaf stays. Then the kept text drops its "the worktree itself was removed, but "
// start. After a failed rmdir the leaf part comes first (row C05). A
// leaf that the home or identity guard skipped stays too (claustrum's own path).
// docs/PROTOCOL.md → "The branch step" gives each text with its rows and VMs.
func rollbackBranchText(repo, branch string, res branchStepResult, leafLeft bool) string {
	switch res.kind {
	case branchNotRun:
		// A name that the skip rule leaves alone stays, with its own text. f6010b97
		// and 89cb6289 both give it for "+x" (rows X1 and X2, Linux VM). A name that
		// starts with "-" gets the same text: claustrum's choice (not measured). The
		// create does not reach it, because git refuses `-b -<name>` (host git, not
		// measured on a VM). In attach mode branch is "", so no text comes.
		if branch == "" {
			return ""
		}
		return "branch " + branch + " left in place (its name is unsafe to pass to update-ref); delete it by hand"
	case branchUnique:
		t := "branch " + branch + " was left in place: it now has commits no other branch or " +
			"remote-tracking ref reaches. To keep that work, push the branch or rename it. To " +
			"discard it, run git branch -D " + branch + ". This branch name cannot be reused " +
			"until the branch is renamed or deleted"
		if !leafLeft {
			t = "the worktree itself was removed, but " + t
		}
		return t
	case branchUnchecked:
		t := "branch " + branch + " still exists; delete it by hand (for example: git branch -d " +
			branch + ", which git refuses if that would lose commits) before retrying this branch " +
			"name: could not check whether another branch or remote-tracking ref reaches its commits"
		if res.err != nil {
			t += " (" + res.err.Error() + ")"
		}
		return t
	case branchUpdateRefFailed:
		t := "branch " + branch + " still exists; delete it by hand (for example: git branch -D " +
			branch + ") before retrying this branch name"
		if branchLockPresent(repo, branch) {
			t += ". A lock refs/heads/" + branch + ".lock is also present: if another git process " +
				"is running against this repository, the lock may be live and clears on its own; " +
				"if none is, it is stale debris of the interrupted delete — remove the lock " +
				"file by hand too"
		}
		if res.err != nil {
			t += ": " + res.err.Error()
		}
		return t
	}
	return ""
}
