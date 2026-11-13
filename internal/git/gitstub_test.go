package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
		fail := os.Getenv("CLAUSTRUM_GITSTUB_EXIT0") == "fail"
		// With CLAUSTRUM_GITSTUB_OUTONLY set, the orphan holds stdout only.
		if !fail && os.Getenv("CLAUSTRUM_GITSTUB_OUTONLY") == "" {
			child.Stderr = os.Stderr
		}
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if f := os.Getenv("CLAUSTRUM_GITSTUB_HOLDPID"); f != "" {
			appendLine(f, strconv.Itoa(child.Process.Pid))
		}
		if fail {
			// git fails at once, as after a smudge filter that failed. The orphan
			// holds stdout only. With CLAUSTRUM_GITSTUB_LATEERR set, a second
			// child holds stderr only, and writes one more line 300 ms later.
			fmt.Fprint(os.Stderr, "fatal: a.txt: smudge filter x failed\n")
			if os.Getenv("CLAUSTRUM_GITSTUB_LATEERR") != "" {
				late := exec.Command(exe)
				late.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "late-stderr"})
				late.Stderr = os.Stderr
				if err := late.Start(); err == nil {
					if f := os.Getenv("CLAUSTRUM_GITSTUB_HOLDPID"); f != "" {
						appendLine(f, strconv.Itoa(late.Process.Pid))
					}
				}
			}
			return 128
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
//     "rename" old new, "chmod" path mode (octal), "symlink" target link,
//     "junction" link target (Windows only, with mklink /J), "snap"
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
//   - "logterm": no real git runs. The call catches SIGTERM and appends "ready"
//     to the file CLAUSTRUM_GITSTUB_TERMLOG. Each SIGTERM appends "term". With
//     CLAUSTRUM_GITSTUB_TERMEXIT set it exits with that code at the first one.
//     Otherwise it sleeps CLAUSTRUM_GITSTUB_MS milliseconds and exits with
//     CLAUSTRUM_GITSTUB_EXIT.
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
// CLAUSTRUM_GITSTUB_ENVEND2 set, it matches only a call that passes it, in the
// form of CLAUSTRUM_GITSTUB_ENVEND. With
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
	if m := os.Getenv("CLAUSTRUM_GITSTUB_MATCH2"); m != "" && stubMatches(args, m) && stubAfter2() && stubDir2() && stubEnvEnd2() {
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
		case "logterm":
			return logTermStub(exitCode)
		}
	}
	cmd := exec.Command(os.Getenv("CLAUSTRUM_GITSTUB_REAL"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// CLAUSTRUM_GITSTUB_STDINLOG names a file that gets the stdin of each call
	// whose last argument is --stdin-paths, and of each `cat-file --buffer
	// --batch`, which the real git then reads as it is. A long-running
	// hash-object ends with -w and keeps its stdin.
	if log := os.Getenv("CLAUSTRUM_GITSTUB_STDINLOG"); log != "" && len(args) > 0 &&
		(args[len(args)-1] == "--stdin-paths" || slices.Contains(args, "--buffer") && args[len(args)-1] == "--batch") {
		in, _ := io.ReadAll(os.Stdin)
		appendLine(log, string(in))
		cmd.Stdin = bytes.NewReader(in)
	}
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

// logTermStub is the mode "logterm" of the git stub. exitCode is the code of a
// call that sleeps to its end.
func logTermStub(exitCode int) int {
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	appendLine(os.Getenv("CLAUSTRUM_GITSTUB_TERMLOG"), "ready")
	ms, _ := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_MS"))
	end := time.After(time.Duration(ms) * time.Millisecond)
	for {
		select {
		case <-terms:
			appendLine(os.Getenv("CLAUSTRUM_GITSTUB_TERMLOG"), "term")
			if c, err := strconv.Atoi(os.Getenv("CLAUSTRUM_GITSTUB_TERMEXIT")); err == nil {
				return c
			}
		case <-end:
			return exitCode
		}
	}
}

// holdStubOutput starts the descendant of the modes "hold" and "holdonly". It
// holds the stdout and stderr of this call for CLAUSTRUM_GITSTUB_MS
// milliseconds. With CLAUSTRUM_GITSTUB_HOLDSTDERR=1 it holds stderr alone. It
// returns 1 when the descendant does not start. With CLAUSTRUM_GITSTUB_HOLDFIFO
// set, it holds both until the writer of that FIFO closes it. The open of the
// FIFO waits for the writer.
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
	if os.Getenv("CLAUSTRUM_GITSTUB_HOLDSTDERR") == "1" {
		child.Stdout = nil
	}
	if fifo := os.Getenv("CLAUSTRUM_GITSTUB_HOLDFIFO"); fifo != "" {
		in, err := os.Open(fifo)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = in.Close() }()
		child.Args = []string{exe}
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "cat"})
		child.Stdin = in
	}
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

// stubEnvEnd2 reports whether a call passes the second rule's narrowing by a
// variable. CLAUSTRUM_GITSTUB_ENVEND2 has the form NAME=text, as
// CLAUSTRUM_GITSTUB_ENVEND, and keeps only a call whose variable NAME ends with
// text.
func stubEnvEnd2() bool {
	name, end, ok := strings.Cut(os.Getenv("CLAUSTRUM_GITSTUB_ENVEND2"), "=")
	if !ok {
		return true
	}
	v, set := os.LookupEnv(name)
	return set && strings.HasSuffix(v, end)
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
	case len(w) == 2 && w[0] == "sleep":
		ms, err := strconv.Atoi(w[1])
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return err
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
	case len(w) == 3 && w[0] == "junction":
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", w[1], w[2]).CombinedOutput(); err != nil {
			return fmt.Errorf("step junction %q: %v: %s", w[1:], err, out)
		}
		return nil
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

// runGitHelper runs the git stub modes of runHelper. It reports false for a mode
// that is not one of them.
func runGitHelper(mode string, args []string) (int, bool) {
	switch mode {
	case "cat-mark":
		// A stand-in `cat`, reached through a PATH entry named cat. It appends one
		// line to the file that CLAUSTRUM_TEST_CAT_MARK names, and then copies stdin
		// to stdout.
		appendLine(os.Getenv("CLAUSTRUM_TEST_CAT_MARK"), "ran")
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return 0, true
	case "git-lingering":
		// Stand-in `git` for git.worktree_create's lingering-descendant tests. Reached
		// when this binary is invoked through a PATH symlink named `git` (see
		// stubLingeringGit): the config prechecks and the add/rollback probes succeed
		// fast, and the read-tree checkout spawns a `sleep` child (this binary re-exec'd)
		// that INHERITS this process's stdout+stderr, the daemon's output pipes,
		// and outlives git, exactly reproducing the P1 smudge-filter orphan. Whether git
		// itself exits 0 (leaving the orphan to hold the pipe) or blocks past the deadline
		// is chosen by CLAUSTRUM_GITSTUB_EXIT0.
		return runGitLingering(args), true
	case "late-stderr":
		// The second child of a failed git-lingering read-tree. It holds stderr
		// for 300 ms and then writes one line there.
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(os.Stderr, "fatal: late line of the filter\n")
		return 0, true
	case "git-slow":
		// Stand-in `git` for the git.worktree_create caller-timeout tests. It passes
		// every call through to the real git and slows one chosen call. See runGitSlow.
		return runGitSlowTimed(args), true
	case "open-repo-file":
		// Opens args[0] as the config read does (openRepoFile). Then it prints
		// "terminal" if this process has a controlling terminal, and "no terminal"
		// if it has none: an open of /dev/tty answers that. The test starts it in
		// a new session, so it has none before the open.
		f, err := openRepoFile(args[0])
		if err != nil || f == nil {
			fmt.Fprintln(os.Stderr, "open:", err)
			return 2, true
		}
		defer func() { _ = f.Close() }()
		printTerminal()
		return 0, true
	case "git-dir-unusable":
		// Runs gitDirUnusable on args[0], which opens its HEAD. Then it prints as
		// open-repo-file does.
		_ = gitDirUnusable(args[0])
		printTerminal()
		return 0, true
	}
	return 0, false
}

// printTerminal prints "terminal" if this process has a controlling terminal, and
// "no terminal" if it has none: an open of /dev/tty answers that.
func printTerminal() {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		_ = tty.Close()
		fmt.Println("terminal")
	} else {
		fmt.Println("no terminal")
	}
}
