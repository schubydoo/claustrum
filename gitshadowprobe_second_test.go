package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// lowerSecondProbeBound lowers the bound of the second run of the call about
// the repository and the wait for its pipes.
func lowerSecondProbeBound(t *testing.T, bound, drain time.Duration) {
	t.Helper()
	oldBound, oldDrain := probeSecondBound, shadowCallDrain
	probeSecondBound, shadowCallDrain = bound, drain
	t.Cleanup(func() { probeSecondBound, shadowCallDrain = oldBound, oldDrain })
}

// exit128Runs is the count of calls about the repository after a first call
// that exited 128 and a second run that exited 0: three on Linux and macOS
// (probeConfigFreeRun), two on Windows. The third run of these tests matches
// the stub rule of the first call, so it exits 128 too.
func exit128Runs() int {
	if probeConfigFreeRun {
		return 3
	}
	return 2
}

// secondProbeCall is the call about the repository with a GIT_DIR, or false.
func secondProbeCall(calls []envCall) (envCall, bool) {
	for _, c := range probeCalls(calls) {
		if _, ok := lookupExact(c.env, "GIT_DIR"); ok {
			return c, true
		}
	}
	return envCall{}, false
}

// TestSecondProbeAfterExit128 pins the second run of c8e2f083 on git.info. A
// first call that exits 128 gets one more run with GIT_DIR=<.probe>, in the
// folder `.probe` (measured on Linux and Windows). Exit 0 there is
// the "no repository" answer. A failed second run gives the error with the
// text of the first call, or its exec error when it wrote none. Only the
// Windows working folder and the exit 0 answer are measured. An exit 1 of the
// first call makes no second run. Mutations: skip the branch of secondProbe in
// askGit (the rows with exit 128 fail), accept any exit code in exitedWith128
// (the row with exit 1 fails), and use the text of the second run in
// secondProbe (the rows with exit 1 of the second run fail).
func TestSecondProbeAfterExit128(t *testing.T) {
	failed := "git could not check this repository on the remote machine: at %q: "
	for _, row := range []struct {
		name                string
		exit                int
		stderr              string
		secondExit          int
		secondStderr        string
		text                string // "" for the "no repository" answer
		probes              int
		second, versionCall bool
	}{
		{name: "exit 128, then 0", exit: 128, stderr: "fatal: first text\n", secondStderr: "fatal: second text\n",
			probes: exit128Runs(), second: true},
		{name: "exit 128, then 1", exit: 128, stderr: "fatal: first text\n", secondExit: 1, secondStderr: "fatal: second text\n",
			text: failed + "fatal: first text", probes: 2, second: true, versionCall: true},
		{name: "exit 128 with no text, then 1", exit: 128, secondExit: 1, secondStderr: "fatal: second text\n",
			text: failed + "exit status 128", probes: 2, second: true, versionCall: true},
		{name: "exit 1", exit: 1, stderr: "fatal: first text\n",
			text: failed + "fatal: first text", probes: 1, versionCall: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			answerProbe(t, row.exit, "", row.stderr)
			answerSecondProbe(t, row.secondExit, row.secondStderr)
			raw, calls := f.info(t, f.repo, nil)
			want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
			if row.text != "" {
				want = probeErrorShapes(t, f, fmt.Sprintf(row.text, f.repo))[0]
			}
			if raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			if got := len(probeCalls(calls)); got != row.probes {
				t.Errorf("%d calls about the repository, want %d", got, row.probes)
			}
			versions := slices.ContainsFunc(calls, func(c envCall) bool { return c.plainVersion(t) })
			if versions != row.versionCall {
				t.Errorf("a plain version call: %v, want %v", versions, row.versionCall)
			}
			c, ok := secondProbeCall(calls)
			if ok != row.second {
				t.Fatalf("a second run: %v, want %v", ok, row.second)
			}
			if !ok {
				return
			}
			first := probeCalls(calls)[0]
			probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
			if v, _ := lookupExact(c.env, "GIT_DIR"); v != probe || c.cwd != probe {
				t.Errorf("the second run has GIT_DIR=%s in %s, want %s for both", v, c.cwd, probe)
			}
			if !slices.Equal(c.argv, first.argv) {
				t.Errorf("the argv of the second run = %q, want that of the first %q", c.argv, first.argv)
			}
			rest := slices.DeleteFunc(slices.Clone(c.env), func(kv string) bool { return envName(kv) == "GIT_DIR" })
			slices.Sort(rest)
			firstEnv := slices.Sorted(slices.Values(first.env))
			if !slices.Equal(rest, firstEnv) {
				t.Errorf("the environment of the second run less GIT_DIR = %q\nwant that of the first %q", rest, firstEnv)
			}
		})
	}
}

// TestSecondProbeBound pins the kill of a second run that does not end, with a
// lowered bound. On c8e2f083 the reply came about 30.2 s after the request,
// with the error and the text of the first call (measured on Linux and
// Windows). Mutation: run the second run with probeCallBound in place of
// probeSecondBound (the reply waits for the hold).
func TestSecondProbeBound(t *testing.T) {
	f := newSeededShadowFixture(t)
	lowerSecondProbeBound(t, 300*time.Millisecond, 100*time.Millisecond)
	answerProbe(t, 128, "", "fatal: first text\n")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "rev-parse,--git-dir")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND2", "GIT_DIR="+string(os.PathSeparator)+shadowProbeName)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", strconv.FormatInt((20*time.Second).Milliseconds(), 10))
	start := time.Now()
	raw, calls := f.info(t, f.repo, nil)
	took := time.Since(start)
	text := fmt.Sprintf("git could not check this repository on the remote machine: at %q: fatal: first text", f.repo)
	if want := probeErrorShapes(t, f, text)[0]; raw != want {
		t.Errorf("reply = %s\n  want %s", raw, want)
	}
	if _, ok := secondProbeCall(calls); !ok {
		t.Error("no second run")
	}
	if took < 300*time.Millisecond || took > 10*time.Second {
		t.Errorf("the reply came after %v, want the lowered bound and not the hold of 20 s", took)
	}
}
