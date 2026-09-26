package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles the test binary as a tiny cross-platform fixture command
// (the stdlib helper-process pattern): when CLAUSTRUM_TEST_HELPER is set, the
// binary acts as an echo/cat/sleep/… stand-in instead of running the tests.
// This removes the suite's dependency on /bin/sh-style fixtures so it also
// runs on the Windows CI leg, and keeps the streamed bytes byte-identical
// across OSes (no CRLF translation, no cmd.exe quoting).
func TestMain(m *testing.M) {
	// Intercept the exec-child trampoline first, exactly as main does: a spawn under
	// a run/<clientId>/ socket re-execs this test binary as `--exec-child …`, which
	// must run the trampoline (stamp the child identity, restore held env, exec the
	// real target) rather than the suite. A no-op on non-linux and for any other argv.
	maybeRunExecChild()

	// Every helper re-exec is a copy of THIS binary, and under -race the tsan
	// runtime sleeps atexit_sleep_ms (default 1000ms) in the main thread before
	// the process exits — so each fixture spawn costs a wall-clock second no
	// matter how trivial the fixture is. The suite spawns dozens of them
	// (TestWorktreeCreateLingeringDescendant alone spawns ~30 stub gits), which
	// measured as 48s of the 109s `go test -race ./...` run. Publishing the knob
	// here hands it to every child through buildEnv's os.Environ() base, and
	// leaves the parent's own tsan settings untouched (tsan parses GORACE at
	// startup, and we are already past it). The children run a dozen lines of
	// fixture code and hold no threads at exit, so the sleep buys no detection.
	// An operator-supplied GORACE still wins.
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}

	mode := os.Getenv("CLAUSTRUM_TEST_HELPER")

	// A re-exec'd daemon child must never run the suite. runServe's parent half
	// re-execs os.Executable() with daemonChildEnv=1 — and under `go test` that
	// executable IS this binary. So a test which reaches the parent path without
	// steering the child into a helper mode forks a detached copy of the whole
	// suite, which reaches the same test and forks again. The copies are
	// setsid-detached with their output redirected to a log, so they outlive the
	// `go test` run that started them and nothing reaps them: a fork bomb that
	// looks like a passing test. That is not hypothetical — it happened on
	// 2026-08-02 and took the host down hard.
	//
	// Every legitimate re-exec either sets a helper mode (see helperCommand) or
	// strips daemonChildEnv (see removeEnvKey in server_daemonize_unix_test.go),
	// so reaching here with the child marker and no mode means a test forgot the
	// seam. Refuse loudly: the cost is one dead process instead of a generation.
	if mode == "" && os.Getenv(daemonChildEnv) == "1" {
		fmt.Fprintln(os.Stderr, "test binary re-exec'd as a daemon child without CLAUSTRUM_TEST_HELPER: refusing to run the suite (a test reached runServe's parent path without the helper seam)")
		os.Exit(1)
	}

	if mode == "" {
		// Spawning a fixture must not start the user's login shell to look for an
		// SSH agent. The tests that cover the hand-off swap in their own stub.
		shellAgentSocket = func() string { return "" }
		os.Exit(m.Run())
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// runGitLingering implements the "git-lingering" stub: it dispatches on the git
// subcommand present in args and, for the read-tree checkout, backgrounds a
// pipe-holding orphan. It never uses /bin/sh (greptile P2) — the orphan is this test
// binary re-exec'd in "sleep" mode.
func runGitLingering(args []string) int {
	joined := strings.Join(args, " ")
	has := func(s string) bool { return strings.Contains(joined, s) }
	switch {
	case has("read-tree"):
		orphan := os.Getenv("CLAUSTRUM_GITSTUB_ORPHAN")
		if orphan == "" {
			orphan = "8"
		}
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// The orphan inherits our stdout+stderr (the daemon's combined output pipe) and
		// outlives us, so the pipe stays open after git exits.
		child := exec.Command(exe, orphan)
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if os.Getenv("CLAUSTRUM_GITSTUB_EXIT0") == "1" {
			return 0 // git exits 0; the orphan keeps holding the pipe (git-exit-0 case)
		}
		// Still running at the caller deadline: killed DURING the checkout.
		secs, err := strconv.Atoi(orphan)
		if err != nil {
			secs = 8
		}
		time.Sleep(time.Duration(secs) * time.Second)
		return 0
	case has("worktree") && has("add"):
		// Write the linked `.git` back-pointer into the (already-created) leaf — the last
		// positional arg — so worktreeAdminDir resolves and the checkout runs.
		last := args[len(args)-1]
		_ = os.WriteFile(filepath.Join(last, ".git"),
			[]byte("gitdir: "+filepath.Join(last, ".gitadmin")+"\n"), 0o644)
		return 0
	case has("is-inside-work-tree"):
		fmt.Print("true\n")
		return 0
	case has("abbrev-ref"):
		fmt.Print("main\n")
		return 0
	default:
		// config prechecks, ls-files (populate), and update-ref / worktree remove /
		// branch (rollback): succeed quietly.
		return 0
	}
}

// runGitSlow implements the "git-slow" stub. It runs the real git named by
// CLAUSTRUM_GITSTUB_REAL with the same argv. It slows a call only when every word
// in CLAUSTRUM_GITSTUB_MATCH (comma-separated) is an exact argument of that call.
// A word is never matched as a substring, so "read-tree" does not match a path.
//
// A slowed call first prints its stderr payload and CLAUSTRUM_GITSTUB_STDOUT to
// stdout. The stderr payload is CLAUSTRUM_GITSTUB_STDERR, in which a literal \n, \r
// or \t becomes that control character. When CLAUSTRUM_GITSTUB_STDERR_FILE names a
// file, its bytes are the payload instead, so a test can send any byte. When the
// call holds the word --no-track and CLAUSTRUM_GITSTUB_STDERR2_FILE names a file,
// that file is used, so the attach fallback add gets its own stderr.
//
// CLAUSTRUM_GITSTUB_ACTION, if set, changes the directory named by
// CLAUSTRUM_GITSTUB_LEAF. It runs before the real git in the modes "pre" and
// "fail", and after it in the modes "post", "postfail" and "hold":
//   - "rm": delete it.
//   - "repl": delete it and make a new empty directory in its place.
//   - "replparent": delete its parent, then make the parent and a new empty leaf.
//   - "fill": write junk.txt into it.
//   - "lock": make hsub/f and zz.txt in it, then make hsub read-only, so a
//     delete of hsub fails for a user that is not root.
//   - "lockparent": make its parent read-only, so the leaf cannot be removed.
//   - "lockmany": make rN/f in it for N in the order 3 0 5 1 7 2 6 4, and make each
//     rN read-only. Neither the first nor the last one made is r0.
//
// When CLAUSTRUM_GITSTUB_SNAP names a file, the action then writes the leaf's
// top-level names to it, one per line, in the order that the directory read
// returns them (not sorted).
//
// CLAUSTRUM_GITSTUB_MODE decides:
//   - "pre": sleep CLAUSTRUM_GITSTUB_MS milliseconds, then run the real git.
//   - "post": run the real git, then sleep.
//   - "fail": sleep, then exit without running git.
//   - "postfail": run the real git, then print the stderr payload (not before) and
//     exit, whatever git answered.
//   - "hold": run the real git, start a descendant that holds this call's stdout
//     and stderr for CLAUSTRUM_GITSTUB_MS milliseconds, and exit at once.
//
// "fail" and "postfail" exit with CLAUSTRUM_GITSTUB_EXIT, 1 when it is not set.
//
// When CLAUSTRUM_GITSTUB_LOG names a file, every call appends its argv to it as
// one line, with the words joined by a unit separator (0x1f). When
// CLAUSTRUM_GITSTUB_CTXLOG names a file, every call appends its working directory,
// a record separator (0x1e), its GIT_INDEX_FILE, a record separator, and its argv
// joined as above.
func runGitSlow(args []string) int {
	if log := os.Getenv("CLAUSTRUM_GITSTUB_LOG"); log != "" {
		appendLine(log, strings.Join(args, "\x1f"))
	}
	if log := os.Getenv("CLAUSTRUM_GITSTUB_CTXLOG"); log != "" {
		wd, _ := os.Getwd()
		appendLine(log, wd+"\x1e"+os.Getenv("GIT_INDEX_FILE")+"\x1e"+strings.Join(args, "\x1f"))
	}
	slow := false
	if m := os.Getenv("CLAUSTRUM_GITSTUB_MATCH"); m != "" {
		slow = true
		for _, w := range strings.Split(m, ",") {
			if !slices.Contains(args, w) {
				slow = false
				break
			}
		}
	}
	mode := os.Getenv("CLAUSTRUM_GITSTUB_MODE")
	pause := func() {
		ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_MS"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	exitCode := 1
	if c, err := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_EXIT")); err == nil {
		exitCode = c
	}
	payload := func() []byte {
		file := os.Getenv("CLAUSTRUM_GITSTUB_STDERR_FILE")
		if f2 := os.Getenv("CLAUSTRUM_GITSTUB_STDERR2_FILE"); f2 != "" && slices.Contains(args, "--no-track") {
			file = f2
		}
		if file != "" {
			b, _ := os.ReadFile(file)
			return b
		}
		return []byte(strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(os.Getenv("CLAUSTRUM_GITSTUB_STDERR")))
	}
	if slow {
		if mode != "postfail" {
			_, _ = os.Stderr.Write(payload())
		}
		unescape := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace
		_, _ = os.Stdout.WriteString(unescape(os.Getenv("CLAUSTRUM_GITSTUB_STDOUT")))
		if mode == "pre" || mode == "fail" {
			if err := gitStubAction(os.Getenv("CLAUSTRUM_GITSTUB_ACTION"), os.Getenv("CLAUSTRUM_GITSTUB_LEAF")); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 127
			}
		}
		switch mode {
		case "fail":
			pause()
			return exitCode
		case "pre":
			pause()
		}
	}
	cmd := exec.Command(os.Getenv("CLAUSTRUM_GITSTUB_REAL"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		code = ee.ExitCode()
	}
	if slow && (mode == "post" || mode == "postfail" || mode == "hold") {
		if err := gitStubAction(os.Getenv("CLAUSTRUM_GITSTUB_ACTION"), os.Getenv("CLAUSTRUM_GITSTUB_LEAF")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
	}
	if slow && mode == "post" {
		pause()
	}
	if slow && mode == "postfail" {
		_, _ = os.Stderr.Write(payload())
		return exitCode
	}
	if slow && mode == "hold" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_MS"))
		child := exec.Command(exe, strconv.Itoa((ms+999)/1000))
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return code
}

// gitStubAction applies a CLAUSTRUM_GITSTUB_ACTION to leaf, then writes the
// CLAUSTRUM_GITSTUB_SNAP snapshot. See runGitSlow.
func gitStubAction(action, leaf string) error {
	if err := applyGitStubAction(action, leaf); err != nil {
		return err
	}
	snap := os.Getenv("CLAUSTRUM_GITSTUB_SNAP")
	if snap == "" || action == "" {
		return nil
	}
	d, err := os.Open(leaf)
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	return os.WriteFile(snap, []byte(strings.Join(names, "\n")), 0o644)
}

// applyGitStubAction makes the change that gitStubAction names.
func applyGitStubAction(action, leaf string) error {
	switch action {
	case "":
		return nil
	case "rm":
		return os.RemoveAll(leaf)
	case "repl":
		if err := os.RemoveAll(leaf); err != nil {
			return err
		}
		return os.Mkdir(leaf, 0o755)
	case "replparent":
		if err := os.RemoveAll(filepath.Dir(leaf)); err != nil {
			return err
		}
		return os.MkdirAll(leaf, 0o755)
	case "fill":
		return os.WriteFile(filepath.Join(leaf, "junk.txt"), []byte("junk\n"), 0o644)
	case "lock":
		if err := os.MkdirAll(filepath.Join(leaf, "hsub"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(leaf, "hsub", "f"), []byte("f\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(leaf, "zz.txt"), []byte("zz\n"), 0o644); err != nil {
			return err
		}
		return os.Chmod(filepath.Join(leaf, "hsub"), 0o555)
	case "lockparent":
		return os.Chmod(filepath.Dir(leaf), 0o555)
	case "lockmany":
		for _, i := range []int{3, 0, 5, 1, 7, 2, 6, 4} {
			dir := filepath.Join(leaf, "r"+strconv.Itoa(i))
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "f"), []byte("f\n"), 0o644); err != nil {
				return err
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown CLAUSTRUM_GITSTUB_ACTION %q", action)
}

// appendLine appends line and a newline to the file at path.
func appendLine(path, line string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

// helperCommand returns this test binary's path plus the env overlay that
// makes a spawned child run it in the given helper mode. The overlay rides
// process.spawn's env-merge (request env layered over os.Environ()).
func helperCommand(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe, map[string]string{"CLAUSTRUM_TEST_HELPER": mode}
}

// runHelper implements the fixture commands. Each mode's output is fixed and
// newline-exact so stream assertions and goldens hold on every OS.
func runHelper(mode string, args []string) int {
	switch mode {
	case "echo": // /bin/echo: args joined by spaces, trailing newline
		fmt.Print(strings.Join(args, " ") + "\n")
	case "cat": // /bin/cat: copy stdin to stdout until EOF
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "stall-quiet":
		// A stand-in for an lsof that hangs: it ignores every argument, writes
		// nothing, and outlives any bound a test would set. The argument-ignoring
		// part matters, because the runner under test prepends its own flags.
		time.Sleep(60 * time.Second)
	case "print-quiet":
		// The control for stall-quiet: same argument handling, answers at once.
		fmt.Print("p1\n")
	case "ignore-term":
		// Escalation fixture (Unix): ignore SIGTERM so killAndWait's graceful
		// signal is a no-op and it must escalate to SIGKILL. Announce readiness on
		// stdout so the test only kills once the handler is installed.
		ignoreSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "pwd": // /bin/pwd -P: the physical working directory
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(wd + "\n")
	case "printenv": // print <name>=<value> for args[0] ("" when absent)
		fmt.Print(args[0] + "=" + os.Getenv(args[0]) + "\n")
	case "sigpipe":
		// Behavior fixture for TestIgnoreSigpipeSurvivesClosedStdout: ignore SIGPIPE,
		// wait for the parent's go-ahead (sent only after it has closed the stdout read
		// end), then write to stdout. With the ignore in place the write fails with
		// EPIPE and we exit 0; without it SIGPIPE kills us (exit 141).
		ignoreSigpipeDefault()
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		_, _ = os.Stdout.Write([]byte("to-a-closed-pipe"))
	case "stdout3": // integration fixture: three stdout lines, exit 0
		fmt.Print("l0\nl1\nl2\n")
	case "stderr-exit5": // integration fixture: one stderr line, exit 5
		fmt.Fprint(os.Stderr, "err\n")
		return 5
	case "tree":
		// Fixture for the whole-tree kill tests: wait for the parent's
		// go-ahead on stdin (so confineProcess runs before any grandchild
		// exists), spawn a grandchild sleeper, record its PID in args[0],
		// then linger so the tree stays alive until it is killed.
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	case "tree-stdout":
		// Like "tree", but the grandchild INHERITS this process's stdout and this
		// process lingers instead of exiting. That combination is what the
		// escalation test needs: the leader dies on the graceful signal while the
		// grandchild keeps the stdout pipe open, so the exit drain stays pending
		// past the grace and the leader is already reaped by the time the
		// escalation fires.
		//
		// No stdin go-ahead, unlike "tree": this mode is used by Unix-only tests
		// that set the process group at fork, so there is nothing to wait for.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	case "orphan-stdout":
		// Fixture for the bounded exit drain: start a grandchild that INHERITS
		// this process's stdout (child.Stdout, unlike "tree" above), print one
		// line, then exit. The spawned process is gone but the pipe stays open
		// for args[0] seconds, which is the only way the drain window is
		// reachable at all.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, args[0])
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print("early\n")
		return 7
	case "git-lingering":
		// Stand-in `git` for git.worktree_create's lingering-descendant tests. Reached
		// when this binary is invoked through a PATH symlink named `git` (see
		// stubLingeringGit): the config prechecks and the add/rollback probes succeed
		// fast, and the read-tree checkout spawns a `sleep` child (this binary re-exec'd)
		// that INHERITS this process's stdout+stderr, the daemon's output pipes,
		// and outlives git, exactly reproducing the P1 smudge-filter orphan. Whether git
		// itself exits 0 (leaving the orphan to hold the pipe) or blocks past the deadline
		// is chosen by CLAUSTRUM_GITSTUB_EXIT0.
		return runGitLingering(args)
	case "git-slow":
		// Stand-in `git` for the git.worktree_create caller-timeout tests. It passes
		// every call through to the real git and slows one chosen call. See runGitSlow.
		return runGitSlow(args)
	case "runlock-hold":
		// Run-dir eviction fixture (Unix): open the lock file, take the flock, write
		// an owner record naming this process as a serve daemon, announce readiness by
		// creating a ready-file, then linger so claimRunDir must evict it. args[0] =
		// lock path, args[1] = "term-ignore" to swallow SIGTERM (escalation case),
		// args[2] = ready-file path.
		return runlockHoldFixture(args)
	default:
		// "slow:N": sleep N seconds, then exit 0. The honest-but-slow CLI shape
		// divergence D11 is about — correct output, correct exit code, just not
		// fast. Windows counterpart of the `sleep N; exit 0` sh script slowCLI
		// writes elsewhere.
		if secs, ok := strings.CutPrefix(mode, "slow:"); ok {
			n, err := strconv.Atoi(secs)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			time.Sleep(time.Duration(n) * time.Second)
			return 0
		}
		// "exit:N": exit with code N (a stand-in CLI for the -install tests,
		// where the invocation is fixed at `<cli> --version`).
		if code, ok := strings.CutPrefix(mode, "exit:"); ok {
			n, err := strconv.Atoi(code)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return n
		}
		fmt.Fprintf(os.Stderr, "unknown CLAUSTRUM_TEST_HELPER mode %q\n", mode)
		return 2
	}
	return 0
}
