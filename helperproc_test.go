package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
		if err := makeFixtureTemplateRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture template root:", err)
			os.Exit(1)
		}
		code := m.Run()
		removeFixtureTemplates()
		os.Exit(code)
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
		// A real read-tree writes its index before it exits. The daemon fails the
		// create when that file is gone, so the stub writes one.
		if idx := os.Getenv("GIT_INDEX_FILE"); idx != "" {
			_ = os.WriteFile(idx, []byte("stub index\n"), 0o644)
		}
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
		// The registration exists in the repository, as after a real add: the create
		// tests it, and the index has a place. The add runs in the repository.
		// The record names the leaf with its symlinks resolved, as git writes it: the
		// create compares it with the resolved path. On macOS t.TempDir() is under
		// /var, a symlink.
		last := args[len(args)-1]
		resolved := last
		if r, err := filepath.EvalSymlinks(last); err == nil {
			resolved = r
		}
		wd, _ := os.Getwd()
		reg := filepath.Join(wd, ".git", "worktrees", filepath.Base(last))
		_ = os.MkdirAll(reg, 0o755)
		_ = os.WriteFile(filepath.Join(reg, "commondir"), []byte("../..\n"), 0o644)
		_ = os.WriteFile(filepath.Join(reg, "gitdir"), []byte(filepath.Join(resolved, ".git")+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(last, ".git"), []byte("gitdir: "+reg+"\n"), 0o644)
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
//   - "lockparents": the same for each path of a list in the form of PATH.
//   - "nosearch": set its mode to 0600, so nothing below it can be reached.
//   - "rmindex": delete the file that GIT_INDEX_FILE names. The leaf is not used.
//   - "setfile": the leaf is a file here. Write CLAUSTRUM_GITSTUB_VALUE into it.
//   - "lockmany": make rN/f in it for N in the order 3 0 5 1 7 2 6 4, and make each
//     rN read-only. Neither the first nor the last one made is r0.
//   - "steps": the leaf is not used. CLAUSTRUM_GITSTUB_VALUE holds steps, joined
//     by a record separator (0x1e). Each step is a verb and its arguments, joined by
//     a unit separator (0x1f): "write" path content, "rm" path, "mkdir" path,
//     "rename" old new, "chmod" path mode (octal), "symlink" target link, "snap"
//     file copy (once, and only when the file can be read), and
//     "git" with the arguments of one call of the real git, which runs in the
//     folder and with the environment of the stub. They run in that order.
//
// When CLAUSTRUM_GITSTUB_SNAP names a file, the action then writes the leaf's
// top-level names to it, one per line, in the order that the directory read
// returns them (not sorted).
//
// CLAUSTRUM_GITSTUB_MODE decides:
//   - "pre": sleep CLAUSTRUM_GITSTUB_MS milliseconds, then run the real git.
//     With CLAUSTRUM_GITSTUB_GATE set, make that file with ".in" added after the
//     action and before the sleep.
//   - "gate": make the file CLAUSTRUM_GITSTUB_GATE with ".in" added, then wait
//     until the file CLAUSTRUM_GITSTUB_GATE exists, at most CLAUSTRUM_GITSTUB_MS
//     milliseconds. Then make the file with ".out" added, and run the real git.
//   - "gatefail": as "gate", but exit after the ".out" file without running git.
//   - "post": run the real git, then sleep. With CLAUSTRUM_GITSTUB_GATE set, make
//     that file with ".in" added before the sleep.
//   - "fail": sleep, then exit without running git.
//   - "postfail": run the real git, then print the stderr payload (not before) and
//     exit, whatever git answered.
//   - "hold": run the real git, start a descendant that holds this call's stdout
//     and stderr for CLAUSTRUM_GITSTUB_MS milliseconds, and exit at once. When
//     CLAUSTRUM_GITSTUB_HOLDPID names a file, the pid of the descendant goes in it.
//     When CLAUSTRUM_GITSTUB_HOLDSELF names a file, the pid of the call goes in it.
//   - "holdonly": as "hold", but no real git and no action run. The call prints
//     the two texts of the rule and exits 0, or 1 when the descendant does not
//     start.
//   - "holdwait": as "holdonly", but the call sleeps CLAUSTRUM_GITSTUB_MS
//     milliseconds after the start of the descendant, and then exits 0. With
//     CLAUSTRUM_GITSTUB_WAITMS set, the call sleeps that many milliseconds
//     instead, and the descendant keeps CLAUSTRUM_GITSTUB_MS.
//   - "holdfail": as "holdonly", but the call exits with CLAUSTRUM_GITSTUB_EXIT.
//
// "fail", "gatefail", "postfail" and "holdfail" exit with CLAUSTRUM_GITSTUB_EXIT, 1 when it is not set.
//
// When CLAUSTRUM_GITSTUB_STARTED names a file, a slowed call first writes the
// time of its start into it, in nanoseconds since the Unix epoch.
//
// When CLAUSTRUM_GITSTUB_TIMELOG names a file, every call appends two lines to
// it (runGitSlowTimed). The first one comes before any other step of the call:
// "start", the time, the pid and the argv. The second one comes right before the
// call returns its exit code: "end", the time and the pid. The words are joined
// by a unit separator (0x1f), and a time is in nanoseconds since the Unix epoch.
//
// When CLAUSTRUM_GITSTUB_LOG names a file, every call appends its argv to it as
// one line, with the words joined by a unit separator (0x1f). When
// CLAUSTRUM_GITSTUB_CTXLOG names a file, every call appends its working directory,
// a record separator (0x1e), its GIT_INDEX_FILE, a record separator, its argv joined
// as above, a record separator, and three variables of its environment:
// GIT_COMMON_DIR, GIT_NO_REPLACE_OBJECTS and GIT_GRAFT_FILE, joined by 0x1f. A last
// record separator follows, then every GIT_* entry of its environment as KEY=value,
// in the order of the environment, joined by 0x1f.
//
// When CLAUSTRUM_GITSTUB_IDXMODE names a file, every call that has a GIT_INDEX_FILE
// appends the mode of that file to it, after the real git ended.
//
// When CLAUSTRUM_GITSTUB_ENVLOG names a file, every call appends its working
// directory, its argv and its whole environment, as one record that ends "\x1d\n".
// With CLAUSTRUM_GITSTUB_EXPAND=1 the stderr payload expands $NAME from the stub's own
// environment first.
//
// When CLAUSTRUM_GITSTUB_COPYLOG names a file, every call that has a GIT_COMMON_DIR
// appends one record that ends "\x1d\n": its argv, the text of the HEAD and of the
// config in that folder, and whether its GIT_DIR holds a folder `worktrees`.
//
// A second rule, CLAUSTRUM_GITSTUB_MATCH2, picks another call, so that a test can
// fail the checkout with the first rule and change a call of the rollback's branch
// step with the second. A call that it matches first applies CLAUSTRUM_GITSTUB_ACTION2
// to the path CLAUSTRUM_GITSTUB_LEAF2: "rm" deletes the file, and "touch" makes an
// empty one. "steps" runs the steps of CLAUSTRUM_GITSTUB_VALUE2, in the form of
// the "steps" action, and the leaf is not used. With CLAUSTRUM_GITSTUB_MODE2
// "ignoreterm" it then ignores SIGTERM. It sleeps CLAUSTRUM_GITSTUB_MS2
// milliseconds. Then it exits with CLAUSTRUM_GITSTUB_EXIT2 when that is set, after
// it wrote CLAUSTRUM_GITSTUB_STDERR2 to stderr as it is (no escape is expanded there, unlike the first rule), or else
// goes on as a call that the first rule does not match. With CLAUSTRUM_GITSTUB_AFTER2
// set, the second rule matches only once the file that it names exists. With
// CLAUSTRUM_GITSTUB_DIR2 set, it matches only a call in that folder. With
// CLAUSTRUM_GITSTUB_GATE2 set, it makes that file with ".in" added and waits
// until the file exists, at most CLAUSTRUM_GITSTUB_MS2 milliseconds, instead of
// the sleep.
func runGitSlow(args []string) int {
	if log := os.Getenv("CLAUSTRUM_GITSTUB_LOG"); log != "" {
		appendLine(log, strings.Join(args, "\x1f"))
	}
	if log := os.Getenv("CLAUSTRUM_GITSTUB_CTXLOG"); log != "" {
		wd, _ := os.Getwd()
		env := os.Getenv("GIT_COMMON_DIR") + "\x1f" + os.Getenv("GIT_NO_REPLACE_OBJECTS") + "\x1f" + os.Getenv("GIT_GRAFT_FILE")
		var all []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "GIT_") {
				all = append(all, kv)
			}
		}
		appendLine(log, wd+"\x1e"+os.Getenv("GIT_INDEX_FILE")+"\x1e"+strings.Join(args, "\x1f")+"\x1e"+env+"\x1e"+strings.Join(all, "\x1f"))
	}
	if log := os.Getenv("CLAUSTRUM_GITSTUB_ENVLOG"); log != "" {
		wd, _ := os.Getwd()
		appendLine(log, wd+"\x1e"+strings.Join(args, "\x1f")+"\x1e"+strings.Join(os.Environ(), "\x1f")+"\x1d")
	}
	if log := os.Getenv("CLAUSTRUM_GITSTUB_COPYLOG"); log != "" && os.Getenv("GIT_COMMON_DIR") != "" {
		head, _ := os.ReadFile(filepath.Join(os.Getenv("GIT_COMMON_DIR"), "HEAD"))
		config, _ := os.ReadFile(filepath.Join(os.Getenv("GIT_COMMON_DIR"), "config"))
		worktrees := "no worktrees folder"
		if fi, err := os.Lstat(filepath.Join(os.Getenv("GIT_DIR"), "worktrees")); err == nil && fi.IsDir() {
			worktrees = "worktrees folder"
		}
		appendLine(log, strings.Join(args, "\x1f")+"\x1e"+string(head)+"\x1e"+string(config)+"\x1e"+worktrees+"\x1d")
	}
	// CLAUSTRUM_GITSTUB_STAGE names a file of staged answers (stageGit). A call
	// that one of them matches gets that answer, and no real git runs for it.
	if code, staged := stagedGitAnswer(args); staged {
		return code
	}
	// CLAUSTRUM_GITSTUB_FAIL_LISTING acts on listings. It is a comma-separated
	// list. "scoped" is a listing with --show-scope, which the daemon does not
	// make, and "full" is the plain one with --git-dir. Each name fails its
	// listing with exit code 128. "hold-<name>" waits on that listing, so that a
	// deadline of the caller expires during it. The wait is 60 s, or CLAUSTRUM_GITSTUB_HOLD_MS
	// milliseconds when that is set. With CLAUSTRUM_GITSTUB_LOG set, a held listing
	// appends the line "held" and its name, joined by a unit separator. After the
	// wait it appends "held-end" and the name, so a killed stub leaves no such line.
	//
	// A call with --file is the read of one config file (listConfigText). The
	// stub does not act on it.
	if which := strings.Split(os.Getenv("CLAUSTRUM_GITSTUB_FAIL_LISTING"), ","); which[0] != "" && slices.Contains(args, "--list") &&
		!slices.Contains(args, "--file") {
		kind := "other"
		switch {
		case slices.Contains(args, "--show-scope"):
			kind = "scoped"
		case strings.HasPrefix(args[0], "--git-dir="):
			kind = "full"
		}
		if slices.Contains(which, "hold-"+kind) {
			if log := os.Getenv("CLAUSTRUM_GITSTUB_LOG"); log != "" {
				appendLine(log, "held\x1f"+kind)
			}
			hold := 60 * time.Second
			if ms, err := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_HOLD_MS")); err == nil {
				hold = time.Duration(ms) * time.Millisecond
			}
			time.Sleep(hold)
			if log := os.Getenv("CLAUSTRUM_GITSTUB_LOG"); log != "" {
				appendLine(log, "held-end\x1f"+kind)
			}
		}
		if slices.Contains(which, kind) {
			fmt.Fprintln(os.Stderr, "fatal: stub refuses the "+kind+" listing")
			return 128
		}
	}
	if m := os.Getenv("CLAUSTRUM_GITSTUB_MATCH2"); m != "" && stubMatches(args, m) && stubAfter2() && stubDir2() {
		leaf := os.Getenv("CLAUSTRUM_GITSTUB_LEAF2")
		var err error
		switch os.Getenv("CLAUSTRUM_GITSTUB_ACTION2") {
		case "rm":
			err = os.Remove(leaf)
		case "touch":
			err = os.WriteFile(leaf, nil, 0o644)
		case "steps":
			for _, step := range strings.Split(os.Getenv("CLAUSTRUM_GITSTUB_VALUE2"), "\x1e") {
				if err = applyGitStubStep(strings.Split(step, "\x1f")); err != nil {
					break
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		if os.Getenv("CLAUSTRUM_GITSTUB_MODE2") == "ignoreterm" {
			signal.Ignore(syscall.SIGTERM)
		}
		ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_MS2"))
		if gate := os.Getenv("CLAUSTRUM_GITSTUB_GATE2"); gate != "" {
			_ = os.WriteFile(gate+".in", nil, 0o644)
			for end := time.Now().Add(time.Duration(ms) * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(gate); err == nil {
					break
				}
			}
		} else {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		if c, err := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_EXIT2")); err == nil {
			_, _ = os.Stderr.WriteString(os.Getenv("CLAUSTRUM_GITSTUB_STDERR2"))
			return c
		}
		return runGitReal(args)
	}
	slow := false
	if m := os.Getenv("CLAUSTRUM_GITSTUB_MATCH"); m != "" {
		slow = stubMatches(args, m) && stubNarrowed(args) && stubNth()
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
		b := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(os.Getenv("CLAUSTRUM_GITSTUB_STDERR"))
		if os.Getenv("CLAUSTRUM_GITSTUB_EXPAND") == "1" {
			b = os.ExpandEnv(b)
		}
		return []byte(b)
	}
	if slow {
		if started := os.Getenv("CLAUSTRUM_GITSTUB_STARTED"); started != "" {
			_ = os.WriteFile(started, []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o644)
		}
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
			if gate := os.Getenv("CLAUSTRUM_GITSTUB_GATE"); gate != "" {
				_ = os.WriteFile(gate+".in", nil, 0o644)
			}
			pause()
		case "gate", "gatefail":
			gate := os.Getenv("CLAUSTRUM_GITSTUB_GATE")
			_ = os.WriteFile(gate+".in", nil, 0o644)
			ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_MS"))
			for end := time.Now().Add(time.Duration(ms) * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(gate); err == nil {
					break
				}
			}
			_ = os.WriteFile(gate+".out", nil, 0o644)
			if mode == "gatefail" {
				return exitCode
			}
		case "holdonly":
			return holdStubOutput()
		case "holdwait":
			if c := holdStubOutput(); c != 0 {
				return c
			}
			if ms, err := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_WAITMS")); err == nil {
				time.Sleep(time.Duration(ms) * time.Millisecond)
				return 0
			}
			pause()
			return 0
		case "holdfail":
			if c := holdStubOutput(); c != 0 {
				return c
			}
			return exitCode
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
	if log := os.Getenv("CLAUSTRUM_GITSTUB_IDXMODE"); log != "" {
		if fi, err := os.Stat(os.Getenv("GIT_INDEX_FILE")); err == nil {
			appendLine(log, fmt.Sprintf("%#o", fi.Mode().Perm()))
		}
	}
	if slow && (mode == "post" || mode == "postfail" || mode == "hold") {
		if err := gitStubAction(os.Getenv("CLAUSTRUM_GITSTUB_ACTION"), os.Getenv("CLAUSTRUM_GITSTUB_LEAF")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
	}
	if slow && mode == "post" {
		if gate := os.Getenv("CLAUSTRUM_GITSTUB_GATE"); gate != "" {
			_ = os.WriteFile(gate+".in", nil, 0o644)
		}
		pause()
	}
	if slow && mode == "postfail" {
		_, _ = os.Stderr.Write(payload())
		return exitCode
	}
	if slow && mode == "hold" {
		if c := holdStubOutput(); c != 0 {
			return c
		}
	}
	return code
}

// holdStubOutput starts the descendant of the modes "hold" and "holdonly". It
// holds the stdout and stderr of this call for CLAUSTRUM_GITSTUB_MS
// milliseconds. It returns 1 when the descendant does not start.
func holdStubOutput() int {
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
	if f := os.Getenv("CLAUSTRUM_GITSTUB_HOLDPID"); f != "" {
		appendLine(f, strconv.Itoa(child.Process.Pid))
	}
	if f := os.Getenv("CLAUSTRUM_GITSTUB_HOLDSELF"); f != "" {
		appendLine(f, strconv.Itoa(os.Getpid()))
	}
	return 0
}

// stubDir2 reports whether the working directory of the call passes the second
// rule. With CLAUSTRUM_GITSTUB_DIR2 set, only a call in that folder does.
func stubDir2() bool {
	dir := os.Getenv("CLAUSTRUM_GITSTUB_DIR2")
	if dir == "" {
		return true
	}
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(wd), filepath.Clean(dir))
	}
	return wd == dir
}

// stubAfter2 reports whether the second rule is on. With CLAUSTRUM_GITSTUB_AFTER2
// set, it is on only once the file that it names exists.
func stubAfter2() bool {
	f := os.Getenv("CLAUSTRUM_GITSTUB_AFTER2")
	if f == "" {
		return true
	}
	_, err := os.Stat(f)
	return err == nil
}

// stubNarrowed reports whether a call passes the narrowing options of the first
// rule. CLAUSTRUM_GITSTUB_ARGEND keeps only a call with an argument that ends with
// that text. CLAUSTRUM_GITSTUB_ENVEND has the form NAME=text and keeps only a call
// whose variable NAME ends with text. A call with no such variable does not pass.
// CLAUSTRUM_GITSTUB_ARGLAST keeps only a call whose last argument is that word.
func stubNarrowed(args []string) bool {
	if last := os.Getenv("CLAUSTRUM_GITSTUB_ARGLAST"); last != "" && (len(args) == 0 || args[len(args)-1] != last) {
		return false
	}
	if end := os.Getenv("CLAUSTRUM_GITSTUB_ARGEND"); end != "" &&
		!slices.ContainsFunc(args, func(a string) bool { return strings.HasSuffix(a, end) }) {
		return false
	}
	if name, end, ok := strings.Cut(os.Getenv("CLAUSTRUM_GITSTUB_ENVEND"), "="); ok {
		if v, set := os.LookupEnv(name); !set || !strings.HasSuffix(v, end) {
			return false
		}
	}
	return true
}

// stubMatches reports whether every word of the comma-separated match is an exact
// argument in args.
// stubNth counts the calls that the first rule matches in the file
// CLAUSTRUM_GITSTUB_NTHFILE, and reports whether this call is call number
// CLAUSTRUM_GITSTUB_NTH. With no such file every matched call counts.
func stubNth() bool {
	f := os.Getenv("CLAUSTRUM_GITSTUB_NTHFILE")
	if f == "" {
		return true
	}
	b, _ := os.ReadFile(f)
	n := len(b) + 1
	_ = os.WriteFile(f, append(b, 'x'), 0o644)
	return strconv.Itoa(n) == os.Getenv("CLAUSTRUM_GITSTUB_NTH")
}

func stubMatches(args []string, match string) bool {
	for _, w := range strings.Split(match, ",") {
		if !slices.Contains(args, w) {
			return false
		}
	}
	return true
}

// runGitReal runs the real git with args and the stub's own stdio, and returns its
// exit code.
func runGitReal(args []string) int {
	cmd := exec.Command(os.Getenv("CLAUSTRUM_GITSTUB_REAL"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		return ee.ExitCode()
	}
	return 0
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
	case "lockparents":
		for _, p := range filepath.SplitList(leaf) {
			if err := os.Chmod(filepath.Dir(p), 0o555); err != nil {
				return err
			}
		}
		return nil
	case "nosearch":
		return os.Chmod(leaf, 0o600)
	case "rmindex":
		return os.Remove(os.Getenv("GIT_INDEX_FILE"))
	case "setfile":
		// leaf is a file here. It gets the bytes of CLAUSTRUM_GITSTUB_VALUE.
		return os.WriteFile(leaf, []byte(os.Getenv("CLAUSTRUM_GITSTUB_VALUE")), 0o644)
	case "steps":
		for _, step := range strings.Split(os.Getenv("CLAUSTRUM_GITSTUB_VALUE"), "\x1e") {
			if err := applyGitStubStep(strings.Split(step, "\x1f")); err != nil {
				return err
			}
		}
		return nil
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

// applyGitStubStep runs one step of the "steps" action. See runGitSlow.
func applyGitStubStep(w []string) error {
	switch {
	case len(w) == 3 && w[0] == "write":
		return os.WriteFile(w[1], []byte(w[2]), 0o644)
	case len(w) == 2 && w[0] == "rm":
		return os.Remove(w[1])
	case len(w) == 2 && w[0] == "mkdir":
		return os.Mkdir(w[1], 0o755)
	case len(w) == 3 && w[0] == "rename":
		return os.Rename(w[1], w[2])
	case len(w) == 3 && w[0] == "chmod":
		mode, err := strconv.ParseUint(w[2], 8, 32)
		if err != nil {
			return err
		}
		return os.Chmod(w[1], os.FileMode(mode))
	case len(w) == 3 && w[0] == "symlink":
		return os.Symlink(w[1], w[2])
	case len(w) == 3 && w[0] == "snap":
		// Copy the file w[1] to w[2] once: not when w[2] is there, and not
		// when w[1] cannot be read.
		if _, err := os.Lstat(w[2]); err == nil {
			return nil
		}
		b, err := os.ReadFile(w[1])
		if err != nil {
			return nil
		}
		return os.WriteFile(w[2], b, 0o644)
	case len(w) > 1 && w[0] == "git":
		out, err := exec.Command(os.Getenv("CLAUSTRUM_GITSTUB_REAL"), w[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("step git %q: %v: %s", w[1:], err, out)
		}
		return nil
	}
	return fmt.Errorf("unknown step %q of CLAUSTRUM_GITSTUB_ACTION steps", w)
}

// runWrapLauncher implements the "wrap" helper mode. See runHelper.
func runWrapLauncher(args []string) int {
	if log := os.Getenv("CLAUSTRUM_TEST_WRAP_LOG"); log != "" {
		appendLine(log, "LAUNCH "+strings.Join(args, " "))
	}
	fmt.Fprint(os.Stderr, "WRAP:"+strings.Join(args, " ")+"\n")
	if len(args) == 0 {
		return 2
	}
	// An unset next mode does not leave the helper mode empty, or this binary
	// runs the whole suite. An unknown mode exits 2 instead.
	next := os.Getenv("CLAUSTRUM_TEST_WRAP_NEXT")
	if next == "" {
		next = "wrap-next-unset"
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER="+next)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	return 0
}

// runCLIStub implements the "cli-stub" helper mode, the shape of the stub of the
// VM rows. Its environment steers it:
//
//   - CLAUSTRUM_TEST_STUB_LOG names a file. The stub appends a START line with
//     its argv[0], its pid, the process ids of stubProcIDs and the launcher gate
//     value. Then it appends a DIR line with the sorted names of the folder of
//     argv[0], joined by commas.
//   - CLAUSTRUM_TEST_STUB_CHILD starts a "sleep" child of this binary that lives
//     30 s (a leak budget) and writes its pid to CLAUSTRUM_TEST_STUB_CHILDPID.
//     "group" keeps the child in the process group of the stub. "session" puts it
//     in a new session. "hold" does that too, and the child keeps the stdout and
//     stderr of the stub.
//   - CLAUSTRUM_TEST_STUB_IGNORETERM=1 makes the stub ignore SIGTERM.
//   - CLAUSTRUM_TEST_STUB_MS is how long the stub sleeps, in milliseconds.
//   - CLAUSTRUM_TEST_STUB_EXIT is its exit code, 0 when it is not set.
func runCLIStub() int {
	if os.Getenv("CLAUSTRUM_TEST_STUB_IGNORETERM") == "1" {
		ignoreSigterm()
	}
	if log := os.Getenv("CLAUSTRUM_TEST_STUB_LOG"); log != "" {
		appendLine(log, fmt.Sprintf("START argv0=%s pid=%d %s gate=%s", os.Args[0], os.Getpid(),
			stubProcIDs(), os.Getenv("CLAUDE_SSH_MANAGED_LAUNCHER")))
		var names []string
		if ents, err := os.ReadDir(filepath.Dir(os.Args[0])); err == nil {
			for _, e := range ents {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		appendLine(log, "DIR "+strings.Join(names, ","))
	}
	if kind := os.Getenv("CLAUSTRUM_TEST_STUB_CHILD"); kind != "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "30")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		if kind != "group" {
			stubNewSession(child)
		}
		if kind == "hold" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(os.Getenv("CLAUSTRUM_TEST_STUB_CHILDPID"), []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_STUB_MS"))
	time.Sleep(time.Duration(ms) * time.Millisecond)
	code, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_STUB_EXIT"))
	return code
}

// runGitSlowTimed runs runGitSlow. With CLAUSTRUM_GITSTUB_TIMELOG set, it logs
// the start and the end of the call there. See runGitSlow.
func runGitSlowTimed(args []string) int {
	log := os.Getenv("CLAUSTRUM_GITSTUB_TIMELOG")
	pid := strconv.Itoa(os.Getpid())
	if log != "" {
		appendLine(log, "start\x1f"+strconv.FormatInt(time.Now().UnixNano(), 10)+"\x1f"+pid+"\x1f"+strings.Join(args, "\x1f"))
	}
	code := runGitSlow(args)
	if log != "" {
		appendLine(log, "end\x1f"+strconv.FormatInt(time.Now().UnixNano(), 10)+"\x1f"+pid)
	}
	return code
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
	case "term-exit0":
		// Exit-line fixture (Unix): on SIGTERM, exit with code 0. Announce readiness
		// on stdout so the test only signals once the handler is installed.
		exitZeroOnSigterm()
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exit0-gate":
		// Reap fixture (Unix): on SIGTERM, wait until the file args[0] exists, then
		// exit with code 0. So the test decides the moment at which the child ends.
		exitZeroOnSigtermAfter(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "term-exec":
		// Reap fixture (Unix): on SIGTERM, replace this program with args[0], run as
		// the "sleep 60" helper. The pid and the start value stay, the program changes.
		execOnSigterm(args[0])
		fmt.Print("ready\n")
		time.Sleep(60 * time.Second)
	case "filter-mark":
		// A stand-in git filter. It appends one line to the file args[0], to prove
		// that it ran, and then copies stdin to stdout as cat does.
		appendLine(args[0], "ran")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "cat-mark":
		// A stand-in `cat`, reached through a PATH entry named cat. It appends one
		// line to the file that CLAUSTRUM_TEST_CAT_MARK names, and then copies stdin
		// to stdout.
		appendLine(os.Getenv("CLAUSTRUM_TEST_CAT_MARK"), "ran")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "pwd": // /bin/pwd -P: the physical working directory
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(wd + "\n")
	case "printenv": // print <name>=<value> for args[0] ("" when absent)
		fmt.Print(args[0] + "=" + os.Getenv(args[0]) + "\n")
	case "git-envlog":
		// Stand-in `git` that records two variables of its environment and fails.
		// Reached through a PATH symlink named `git`. Each call appends one line
		// to the file that CLAUSTRUM_GITSTUB_ENVLOG names.
		line := ""
		for _, name := range strings.Fields(os.Getenv("CLAUSTRUM_GITSTUB_ENVNAMES")) {
			if v, ok := os.LookupEnv(name); ok {
				line += name + " set " + v + ";"
			} else {
				line += name + " absent;"
			}
		}
		appendLine(os.Getenv("CLAUSTRUM_GITSTUB_ENVLOG"), line)
		return 1
	case "lookupenv": // print "<name> set <value>" or "<name> absent" for args[0]
		if v, ok := os.LookupEnv(args[0]); ok {
			fmt.Print(args[0] + " set " + v + "\n")
		} else {
			fmt.Print(args[0] + " absent\n")
		}
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
	case "tree-stdio":
		// Like "tree-stdout", but the grandchild inherits stdout AND stderr of this
		// process. A kill of this process alone then leaves both pipes of the daemon
		// open until the exit drain gives up.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
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
		return runGitSlowTimed(args)
	case "open-repo-file":
		// Opens args[0] as the config read does (openRepoFile). Then it prints
		// "terminal" if this process has a controlling terminal, and "no terminal"
		// if it has none: an open of /dev/tty answers that. The test starts it in
		// a new session, so it has none before the open.
		f, err := openRepoFile(args[0])
		if err != nil || f == nil {
			fmt.Fprintln(os.Stderr, "open:", err)
			return 2
		}
		defer func() { _ = f.Close() }()
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			_ = tty.Close()
			fmt.Println("terminal")
		} else {
			fmt.Println("no terminal")
		}
		return 0
	case "wrap":
		// A managed-launcher stand-in, the shape of the VM rows' W: it writes
		// "WRAP:<args>" to stderr, then runs args[0] with args[1:] and exits with
		// its code. The run gets CLAUSTRUM_TEST_HELPER=$CLAUSTRUM_TEST_WRAP_NEXT, so
		// the command is this binary in another mode. When CLAUSTRUM_TEST_WRAP_LOG
		// names a file, a "LAUNCH <args>" line is appended to it first, to prove the
		// launcher ran.
		return runWrapLauncher(args)
	case "fail-launcher":
		// A launcher that fails: it writes CLAUSTRUM_TEST_LAUNCH_STDERR to stderr and
		// exits with CLAUSTRUM_TEST_LAUNCH_EXIT. It appends to CLAUSTRUM_TEST_WRAP_LOG
		// like "wrap", and never runs its args.
		if log := os.Getenv("CLAUSTRUM_TEST_WRAP_LOG"); log != "" {
			appendLine(log, "LAUNCH "+strings.Join(args, " "))
		}
		fmt.Fprint(os.Stderr, os.Getenv("CLAUSTRUM_TEST_LAUNCH_STDERR"))
		n, err := strconv.Atoi(os.Getenv("CLAUSTRUM_TEST_LAUNCH_EXIT"))
		if err != nil {
			return 1
		}
		return n
	case "selfterm":
		// A launcher that ends itself with SIGTERM (Unix. Windows has no such signal).
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		time.Sleep(10 * time.Second)
		return 1
	case "cli-log":
		// A stand-in CLI that appends "CLI <args> gate=<value>" to
		// CLAUSTRUM_TEST_CLI_LOG and exits 0, to prove that it ran, with which args,
		// and whether CLAUDE_SSH_MANAGED_LAUNCHER reached it.
		appendLine(os.Getenv("CLAUSTRUM_TEST_CLI_LOG"),
			"CLI "+strings.Join(args, " ")+" gate="+os.Getenv("CLAUDE_SSH_MANAGED_LAUNCHER"))
	case "cli-stub":
		// A stand-in CLI for the -install bound and order tests. See runCLIStub.
		return runCLIStub()
	case "printenvs":
		// print <name>=<value> for each arg, one per line, "" when absent.
		for _, name := range args {
			fmt.Print(name + "=" + os.Getenv(name) + "\n")
		}
	case "environ":
		// print the whole environment, one entry per line, in its order.
		for _, e := range os.Environ() {
			fmt.Print(e + "\n")
		}
	case "detach-launch":
		// A stand-in for the -serve launcher. It waits for the go-ahead on stdin, so
		// the test can put this process in a job first. It then starts a detached
		// sleeper the way daemonizeWithToken starts the daemon child
		// (detachSysProcAttr and startDetached), writes its pid to args[0] and exits.
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, "60")
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.SysProcAttr = detachSysProcAttr()
		if err := startDetached(child); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "spawn-hold":
		// A stand-in for a daemon that owns one child: spawn a sleeper through the
		// production process manager, write its pid to args[0], then linger until the
		// test ends this process hard.
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		p, err := newProcManager().spawn(&conn{nc: client}, "held", exe, []string{"60"}, "", map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"}, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[0], []byte(strconv.Itoa(p.pid)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(60 * time.Second)
	case "runlock-hold":
		// Run-dir eviction fixture (Unix): open the lock file, take the flock, write
		// an owner record naming this process as a serve daemon, announce readiness by
		// creating a ready-file, then linger so lockRunFolder must evict it. args[0] =
		// lock path, args[1] = "term-ignore" to swallow SIGTERM (escalation case),
		// args[2] = ready-file path.
		return runlockHoldFixture(args)
	case "runlock-hold-empty":
		// Late-lock fixture (Unix): hold the flock on an empty lock file, as a process
		// that is not a daemon does. args[0] = lock path, args[1] = ready-file path,
		// args[2] = a file whose creation makes the fixture let go and exit 0.
		return runlockHoldEmptyFixture(args)
	case "peer-netns":
		// Peer check fixture (Linux): a daemon whose caller has another network
		// namespace. See peerNetnsFixture.
		return peerNetnsFixture()
	case "peer-dial":
		// Peer check fixture: dial the unix socket args[0]. With args[1] = "exit" it
		// exits at once, with the connection open. Otherwise it writes args[1] and a
		// newline, and copies what it reads to stdout until EOF or an error.
		nc, err := net.Dial("unix", args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if args[1] == "exit" {
			return 0
		}
		_, _ = nc.Write([]byte(args[1] + "\n"))
		_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.Copy(os.Stdout, nc)
	default:
		// "slow:N": sleep N seconds, then exit 0. This is the honest-but-slow CLI
		// shape: correct output, correct exit code, just not fast. It is the
		// Windows counterpart of the sh script that slowCLI writes elsewhere.
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
