package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// The managed launcher in -install and -probe-cli (89cb6289). Both use it only when
// CLAUDE_SSH_MANAGED_LAUNCHER is exactly "1". Then they resolve the launcher for the
// CLI path, and:
//
//   - none: the CLI runs directly, as without the gate.
//   - usable: the CLI runs once as `<launcher argv...> <cli> --version`. There is
//     no direct run. The launcher's stdout is not reported.
//   - unusable or unreadable: the CLI does not run at all.
//
// All measured for -install on Linux. The none, usable, unusable and unreadable
// rows of -install are measured on macOS too. For -probe-cli the none path is not
// measured. On Windows the resolve answers none (managedSettingsDir).

// managedLauncherGateOn reports whether the -install and -probe-cli gate is on.
// Of the values tried on -install, only "1" turns it on (measured). claustrum's
// choice (not measured): every other value is off too.
func managedLauncherGateOn() bool { return os.Getenv(managedLauncherGateEnv) == "1" }

// managedRunBound stops a launcher run of -install and -probe-cli. The reference
// stopped a launcher that slept 60 s at 33.057 s (-install) and one that slept
// 150 s at 33.069 s (-probe-cli). The -install row is a cache hit. This is
// reference behaviour. It is not the opt-in -cli-probe-timeout (D11), which bounds
// only the direct -install run.
// A var only so tests shrink it.
var managedRunBound = 33 * time.Second

// managedFirstRunBound stops the launcher run of -install right after a fresh
// install. The reference stopped a launcher that slept 300 s at 123.048 s after a
// -cli-url install and at 123.066 s after a -cli-zst install. It let a launcher
// that took 100 s finish (Linux VM). A var only so tests shrink it.
var managedFirstRunBound = 123 * time.Second

// managedRunStderrCap is how much launcher stderr -install reports (measured: 10000
// bytes gave 8192 bytes and a tail naming the other 1808).
const managedRunStderrCap = 8192

// Launcher run texts, copied from VM captures.
const (
	managedStatusProbeFailed  = "probe_failed"
	managedStatusUnresponsive = "unresponsive"
	managedRunStopped         = "did not exit within 33s and was stopped"
	managedFirstRunStopped    = "did not exit within 123s and was stopped"
)

// managedUnresponsiveText is the cliError of a stopped launcher run, and the
// -probe-cli stderr after "claude-ssh: ". claustrum's choice (not measured): the
// text names the launcher's argv[0] only, as the measured one-element launchers do.
func managedUnresponsiveText(a0 string) string {
	return "cli unresponsive: the installed Claude Code binary was started through the host's managed launcher " + a0 +
		" and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish"
}

// managedRun is the outcome of one launcher run.
type managedRun struct {
	hung   bool   // stopped at its bound
	first  bool   // the run right after a fresh install
	err    error  // nil when the launcher exited 0
	stderr string // the launcher's stderr, capped
}

// runViaManagedLauncher runs `<argv...> <cli> --version` under bound. The
// run gets the daemon env without the launcher variables, so the gate does not reach
// it (measured). claustrum's choice (not measured): CLAUDE_CODE_PROCESS_WRAPPER and
// the E2E variable are dropped too, as for a launched spawn.
//
// claustrum's choice (not measured): the stop kills the run's whole process group
// with SIGKILL. The output drain then waits at most probeCLIKillGrace. That is the
// teardown of the direct -probe-cli run. The reference left no process behind
// (measured).
func runViaManagedLauncher(argv []string, cli string, bound time.Duration) managedRun {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	args := append(append(append([]string{}, argv[1:]...), cli), "--version")
	cmd := exec.CommandContext(ctx, argv[0], args...)
	cmd.Env = stripManagedLauncherEnv(os.Environ(), true)
	cmd.SysProcAttr = newSysProcAttr()
	cmd.Cancel = func() error {
		reapProcessGroup(cmd.Process)
		_ = cmd.Process.Kill()
		return nil
	}
	cmd.WaitDelay = probeCLIKillGrace
	eb := &cappedText{max: managedRunStderrCap}
	cmd.Stderr = eb
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return managedRun{hung: true, stderr: eb.String()}
	}
	return managedRun{err: err, stderr: eb.String()}
}

// managedRunReason is the launcherReason of a failed run: "exit status N", or
// "terminated by SIGTERM" for a signal (both measured). claustrum's choice (not
// measured): a signal without a known name, or a start failure, is the Go error
// text.
func managedRunReason(err error) string {
	if sig := exitSignalName(err); sig != "" {
		return "terminated by " + sig
	}
	return err.Error()
}

// cappedText keeps the first max bytes written to it and counts the rest. Its text
// is the kept bytes, then "\n[… N more bytes]" when some were dropped (measured with
// 10000 bytes of stderr). claustrum's choices (not measured): the head is kept, not
// the tail, and the "\n" is added even when the kept part ends in a newline.
type cappedText struct {
	max     int
	buf     []byte
	dropped int64
}

func (c *cappedText) Write(p []byte) (int, error) {
	room := c.max - len(c.buf)
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		c.buf = append(c.buf, p[:room]...)
	} else {
		room = 0
	}
	c.dropped += int64(len(p) - room)
	return len(p), nil
}

func (c *cappedText) String() string {
	if c.dropped == 0 {
		return string(c.buf)
	}
	return string(c.buf) + "\n[… " + strconv.FormatInt(c.dropped, 10) + " more bytes]"
}

// installManaged carries the launcher through one -install run. runInstall sets it
// when the gate is on and there is a CLI path, and installCLICheck reads it. nil
// means that no launcher resolve ran.
var installManaged *installManagedState

type installManagedState struct {
	res managedLauncherResult
	run *managedRun // the launcher run, nil until it runs
}

// errCLINotRunnable is a direct `<cli> --version` run that failed.
var errCLINotRunnable = errors.New("cli is not runnable")

// managedUnresponsiveError is a launcher run that its bound stopped.
type managedUnresponsiveError struct{ launcher string }

func (e *managedUnresponsiveError) Error() string { return managedUnresponsiveText(e.launcher) }

// installCLICheck is the -install check that the CLI at path runs. Without the gate,
// or with a none answer, it is the direct isRunnable run. With a usable launcher it
// is one launcher run. A failed launcher run still counts as runnable: the CLI stays,
// cliWasPresent stays true and the facts say probe_failed (measured on a cache hit).
// A stopped run is managedUnresponsiveError. An unusable or unreadable answer runs
// nothing and counts as runnable (measured on a cache hit).
//
// first marks the run right after a fresh install. That run gets
// managedFirstRunBound, and its launcherReason names 123s (measured). It passes the
// staged file, as the direct run does (an older gap: the reference passes the final
// path).
//
// Right after a fresh -cli-url install, a failed run and an unusable answer follow
// the cache-hit rules (measured). claustrum's choice (not measured): an unreadable
// answer does too, and so do all three after a -cli-zst install.
func installCLICheck(path string, first bool) error {
	st := installManaged
	if st == nil || st.res.Status == managedStatusNone {
		if isRunnable(path) {
			return nil
		}
		return errCLINotRunnable
	}
	if st.res.Status != managedStatusUsable {
		return nil
	}
	bound := managedRunBound
	if first {
		bound = managedFirstRunBound
	}
	r := runViaManagedLauncher(st.res.Argv, path, bound)
	r.first = first
	st.run = &r
	if r.hung {
		return &managedUnresponsiveError{launcher: st.res.Argv[0]}
	}
	return nil
}

// fillFacts adds the launcher fields to the -install facts. The measured field order
// is in installFacts. A stderr of zero bytes omits launcherStderr (measured).
func (st *installManagedState) fillFacts(f *installFacts) {
	r := st.res
	f.LauncherStatus = r.Status
	switch r.Status {
	case managedStatusUsable:
		f.Launcher, f.LauncherSource = r.Argv, r.Source
		switch {
		case st.run == nil:
		case st.run.hung:
			f.LauncherStatus, f.LauncherReason = managedStatusUnresponsive, managedRunStopped
			if st.run.first {
				f.LauncherReason = managedFirstRunStopped
			}
		case st.run.err != nil:
			f.LauncherStatus = managedStatusProbeFailed
			f.LauncherReason, f.LauncherStderr = managedRunReason(st.run.err), st.run.stderr
		}
	case managedStatusUnusable:
		f.LauncherSource, f.LauncherReason = r.Source, r.Reason
	case managedStatusUnreadable:
		f.LauncherPath, f.LauncherReason = r.Path, r.Reason
	}
}

// runProbeCLI is the -probe-cli mode. Without the gate it is the direct probe
// (measured). With the gate and a none answer it is the direct probe too (not
// measured). With the gate it prints __CLI_LAUNCHER__ when the launcher is
// unusable, the settings are unreadable or the launcher run failed. It prints
// __CLI_HUNG__ when the run was stopped. Each of those puts a reason on stderr. A
// usable launcher whose run exits 0 prints nothing. The mode always exits 0. All
// of that is measured on Linux.
//
// claustrum's choices (not measured) follow. An empty launcher stderr leaves out
// the "printed:" block. The stderr cap of -install applies. The text names the
// launcher's argv[0] only.
func runProbeCLI(stdout, stderr io.Writer, path string) {
	if managedLauncherGateOn() {
		if res := resolveManagedLauncher(path); res.Status != managedStatusNone {
			probeViaManagedLauncher(stdout, stderr, path, res)
			return
		}
	}
	writeProbeCLIResult(stdout, probeCLIRunnable(path))
}

func probeViaManagedLauncher(stdout, stderr io.Writer, path string, res managedLauncherResult) {
	switch res.Status {
	case managedStatusUnusable:
		fmt.Fprintln(stdout, "__CLI_LAUNCHER__")
		fmt.Fprintf(stderr, "claude-ssh: %s was not run: managed launcher unusable: %s\n", path, res.Reason)
		return
	case managedStatusUnreadable:
		fmt.Fprintln(stdout, "__CLI_LAUNCHER__")
		fmt.Fprintf(stderr, "claude-ssh: %s was not run: managed settings unreadable: %s: %s\n", path, res.Path, res.Reason)
		return
	}
	r := runViaManagedLauncher(res.Argv, path, managedRunBound)
	switch {
	case r.hung:
		fmt.Fprintln(stdout, "__CLI_HUNG__")
		fmt.Fprintf(stderr, "claude-ssh: %s\n", managedUnresponsiveText(res.Argv[0]))
	case r.err != nil:
		fmt.Fprintln(stdout, "__CLI_LAUNCHER__")
		if r.stderr != "" {
			fmt.Fprintf(stderr, "claude-ssh: the managed launcher's run printed:\n%s\n", r.stderr)
		}
		fmt.Fprintf(stderr, "claude-ssh: %s --version through the managed launcher %s did not succeed: %s\n",
			path, res.Argv[0], managedRunReason(r.err))
	}
}
