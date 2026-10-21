package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The time limits of the git calls of git.list_branches, git.info, git.status and
// git.worktree_create. Round task010-01 measured them on 5fd08069 (Linux, macOS and
// Windows VMs). git.worktree_remove gets none of these limits. Its first call has
// the bound of 30 s of the call about the repository (cell TR of round
// task010-02, Linux VM). The limits are always on. They are no limiter of an
// opt-in divergence.
var (
	// gitCallLimit ends one git call of those four methods. Cells T1 to T7 show the
	// stop at 120 s. The read-tree call of a create has no such limit (cell T17).
	gitCallLimit = 120 * time.Second
	// gitWideLimit takes the place of gitCallLimit while a request with
	// "shadowDirOff":true runs in the daemon. No call of the daemon then stops at
	// 2 minutes, also on another repository (cells T8 and T9). The 30 minutes,
	// and the end of the window when the last such request answers, are
	// claustrum's own (not measured).
	gitWideLimit = 1800 * time.Second
	// configListingLimit ends each config listing before a hardened call: the
	// first one of a request (cells T10 and T11) and the listing before the
	// checkout (cell TA of round task010-02, Linux VM). The --show-scope
	// listing of a create with the filter rule and no settings copy is no such
	// listing: it has the 2 minute limit and the timeoutMs (claustrum's own).
	configListingLimit = 30 * time.Second
	// includeScanLimit ends the scan of ignored files of the include step of a
	// create (cells T13 and T14).
	includeScanLimit = 600 * time.Second
	// gitStopGrace is the time from the SIGTERM of a stopped call to its SIGKILL
	// on Linux and macOS (cell T2: 2.005 s).
	gitStopGrace = 2 * time.Second
	// gitDrainWait is the time that a call waits for its output pipes after git
	// exited (cells T15 and T16: 2.0 s). The child that holds them gets no
	// signal. A call under untilPipeEOF has no such wait.
	gitDrainWait = 2 * time.Second
)

// gitLimit selects the limit of one call.
type gitLimit int

const (
	// callLimit is gitCallLimit or gitWideLimit. A call that a limit stops stops
	// the request: no later call of gitRun in the request starts.
	callLimit gitLimit = iota
	// listingLimit is configListingLimit. Its error carries the hooks refusal
	// (gitStopped.listing).
	listingLimit
	// includeScan is includeScanLimit.
	includeScan
)

// gitStopped is the error of a git call that its time limit stopped. The text is
// that of 5fd08069, for example `git took too long: Claude stopped git
// for-each-ref after 2 minutes; try again` (cell T1).
type gitStopped struct {
	sub   string
	after string
	// listing marks a stopped config listing. Its text starts with the hooks
	// refusal (cells T10 and TA).
	listing bool
}

func (e *gitStopped) Error() string {
	text := "git took too long: Claude stopped git " + e.sub + " after " + e.after + "; try again"
	if e.listing {
		return hooksRefusalPrefix + text
	}
	return text
}

// isGitStop reports whether err is the error of a stopped call.
func isGitStop(err error) bool {
	var s *gitStopped
	return errors.As(err, &s)
}

// gitStop holds the time limits of the git calls of one request. A nil *gitStop
// sets no limit: each call runs as exec.Cmd runs it.
type gitStop struct {
	// wide is the count of the daemon's requests with "shadowDirOff":true that
	// have not answered yet.
	wide *atomic.Int32

	mu      sync.Mutex
	stopped *gitStopped
}

// limitedGitMethod reports whether the git calls of method get the time limits.
func limitedGitMethod(method string) bool {
	switch method {
	case "git.list_branches", "git.info", "git.status", "git.worktree_create":
		return true
	}
	return false
}

// offSwitchSet reports whether the params of a request hold "shadowDirOff":true.
// A params object that does not parse reports false.
func offSwitchSet(params json.RawMessage) bool {
	var p struct {
		ShadowDirOff jsonTrueSwitch `json:"shadowDirOff"`
	}
	return json.Unmarshal(params, &p) == nil && bool(p.ShadowDirOff)
}

type gitStopKey struct{}
type gitLimitKey struct{}
type pipeEOFKey struct{}

// context is the context of the git calls of the request. gitRun and gitOutput
// read the limits from it.
func (s *gitStop) context() context.Context {
	if s == nil {
		return context.Background()
	}
	return context.WithValue(context.Background(), gitStopKey{}, s)
}

// withGitLimit is ctx with the limit kind for the calls that run under it.
func withGitLimit(ctx context.Context, kind gitLimit) context.Context {
	return context.WithValue(ctx, gitLimitKey{}, kind)
}

// untilPipeEOF is ctx for the calls whose output the daemon reads to its end.
// Such a call goes on only when its output pipes close, also after git exited.
// The child that holds them gets no signal. 5fd08069 does so for the config
// listing before a hardened call (cells A1 and A2) and for the `status` scan of
// git.status (cell A3 of round task101-01, Linux VM). The `ls-files` and
// `diff-index` scans do the same (claustrum's own, not measured).
func untilPipeEOF(ctx context.Context) context.Context {
	return context.WithValue(ctx, pipeEOFKey{}, true)
}

func gitStopOf(ctx context.Context) (*gitStop, gitLimit) {
	s, _ := ctx.Value(gitStopKey{}).(*gitStop)
	kind, _ := ctx.Value(gitLimitKey{}).(gitLimit)
	return s, kind
}

// halted is the error of the stopped call of the request, or nil.
func (s *gitStop) halted() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped == nil {
		return nil
	}
	return s.stopped
}

// limit is the duration of kind and its words in the stop text.
func (s *gitStop) limit(kind gitLimit) (time.Duration, string) {
	switch kind {
	case listingLimit:
		return configListingLimit, "30 seconds"
	case includeScan:
		return includeScanLimit, "10 minutes"
	}
	if s.wide != nil && s.wide.Load() > 0 {
		// This limit and its text are claustrum's own (not measured).
		return gitWideLimit, "30 minutes"
	}
	return gitCallLimit, "2 minutes"
}

// gitRun runs cmd under the limits of ctx. With no limits in ctx it is cmd.Run.
//
// After a stopped call or listing of the request no call of gitRun starts, and
// the error is that of the stopped call. Other calls, such as those of the
// branch step of a rollback, still run (cell TA of round task010-02). At the limit the call gets the stop: on Linux and macOS
// SIGTERM, and SIGKILL gitStopGrace later (cell T2). On Windows the process is
// ended at once (cells T1, T3h and T5).
//
// The wait for the output pipes after git exited ends at gitDrainWait. A call
// that exited 0 then counts as a call that exited 0 (cells T15 and T16). Under
// untilPipeEOF the wait has no end. A call that exited 0 gives its result when
// the pipes close, also past its limit. A call that the limit stopped gives the
// stop text then (cells A1 and A2 of round task101-01, Linux VM).
//
// Each call runs under the hold of its repository folder (runHeld).
func gitRun(ctx context.Context, cmd *exec.Cmd) error {
	s, kind := gitStopOf(ctx)
	if s == nil {
		return runHeld(cmd, cmd.Run)
	}
	if err := s.halted(); err != nil {
		return err
	}
	toEOF, _ := ctx.Value(pipeEOFKey{}).(bool)
	return runHeld(cmd, func() error { return gitRunLimited(s, kind, toEOF, cmd) })
}

// gitRunLimited is the start and the wait of cmd under the limit kind of s.
// With toEOF the wait for the output pipes has no end (untilPipeEOF).
func gitRunLimited(s *gitStop, kind gitLimit, toEOF bool, cmd *exec.Cmd) error {
	limit, after := s.limit(kind)
	if !toEOF {
		cmd.WaitDelay = gitDrainWait
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var mu sync.Mutex
	done, fired := false, false
	var kill *time.Timer
	timer := time.AfterFunc(limit, func() {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		// The call counts as stopped also when git exited before the limit
		// and only a child holds its pipes (under untilPipeEOF, or within
		// gitDrainWait of the limit). A failed call then
		// gives the stop text, and a call that exited 0 stays a success, on
		// every system (claustrum's own, not measured).
		fired = true
		_ = stopGitProcess(cmd.Process)
		kill = time.AfterFunc(gitStopGrace, func() {
			mu.Lock()
			defer mu.Unlock()
			if !done {
				_ = cmd.Process.Kill()
			}
		})
	})
	err := cmd.Wait()
	mu.Lock()
	done = true
	timer.Stop()
	if kill != nil {
		kill.Stop()
	}
	stopped := fired
	mu.Unlock()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	if stopped && err != nil {
		e := &gitStopped{sub: gitSubcommand(cmd.Args), after: after, listing: kind == listingLimit}
		s.mu.Lock()
		if s.stopped == nil {
			s.stopped = e
		}
		s.mu.Unlock()
		return e
	}
	return err
}

// gitOutput is cmd.Output under the limits of ctx (gitRun). The stderr of git is
// not in the error: no caller reads it there.
func gitOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if s, _ := gitStopOf(ctx); s == nil {
		var out []byte
		err := runHeld(cmd, func() error {
			var err error
			out, err = cmd.Output()
			return err
		})
		return out, err
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := gitRun(ctx, cmd)
	return out.Bytes(), err
}

// gitSubcommand is the git subcommand in the argv args: the first word after
// "git" that is no option and no value of -c or -C. The stop text names it, for
// example `worktree` for `worktree add` (cell T5).
func gitSubcommand(args []string) string {
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// reply is the answer of a request whose call a limit stopped, or resp.
// git.list_branches, git.info and git.status answer -32603 with the stop text
// (cells T1, T3, T4 and T10). git.worktree_create keeps the frame of the step
// whose call stopped, with the stop text as its cause (cells T5 and TB).
func (s *gitStop) reply(req *request, resp response) response {
	err := s.halted()
	if err == nil || req.Method == "git.worktree_create" {
		return resp
	}
	return errResult(req.ID, codeInternal, err.Error())
}
