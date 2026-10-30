//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// badIncludeConfig makes GIT_CONFIG_GLOBAL of the test a file with an
// includeIf of the folder repo to a file whose one line is `[[[`. It returns the path of that bad file. With it a call in repo
// that reads the config of the user exits 128 with `fatal: bad config line 1
// in file <bad file>`.
func badIncludeConfig(t *testing.T, repo string) string {
	t.Helper()
	dir := resolveTestRoot(t, t.TempDir())
	bad := filepath.Join(dir, "bad.inc")
	writeFile(t, bad, "[[[\n", 0o644)
	global := filepath.Join(dir, "gitconfig")
	writeFile(t, global, fmt.Sprintf("[includeIf \"gitdir:%s/\"]\n\tpath = %s\n", repo, bad), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	return bad
}

// thirdProbeCall is the call about the repository with
// GIT_CONFIG_GLOBAL=/dev/null and no GIT_DIR, or false. The tests that use it
// give GIT_CONFIG_GLOBAL another value.
func thirdProbeCall(calls []envCall) (envCall, bool) {
	for _, c := range probeCalls(calls) {
		_, dir := lookupExact(c.env, "GIT_DIR")
		if v, _ := lookupExact(c.env, "GIT_CONFIG_GLOBAL"); v == "/dev/null" && !dir {
			return c, true
		}
	}
	return envCall{}, false
}

// TestThirdProbeAfterABadUserConfig pins the third run of 52d82f0a (measured
// on Linux and macOS). A config of the user that includes a file with a bad
// line for the repository makes the first call exit 128. The second run with
// GIT_DIR=<.probe> exits 0. The third run is the first call again, in the root,
// with GIT_CONFIG_COUNT=0, GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1
// and no GIT_DIR. It exits 0, and the reply is the error with the text of the
// first call and the hint, with no `git version` call. Mutations: answer "no
// repository" after the second run (no third run: the reply fails), and drop
// GIT_CONFIG_GLOBAL=/dev/null from the third run (it reads the config of the
// user and exits 128: the reply fails).
func TestThirdProbeAfterABadUserConfig(t *testing.T) {
	for _, method := range []string{"git.info", "git.status"} {
		t.Run(method, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			bad := badIncludeConfig(t, f.repo)
			params := map[string]any{"path": f.repo}
			if method == "git.status" {
				params["baseRepo"] = f.repo
			}
			raw, calls := f.call(t, method, params)
			text := fmt.Sprintf("git could not check this repository on the remote machine: at %q: fatal: bad config line 1 in file %s; if that names a git setting or file, fix it, then try again", f.repo, bad)
			if want := probeErrorShapes(t, f, text)[0]; raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			if n := len(probeCalls(calls)); n != 3 {
				t.Errorf("%d calls about the repository, want 3", n)
			}
			if slices.ContainsFunc(calls, func(c envCall) bool { return c.plainVersion(t) }) {
				t.Error("a plain version call, want none")
			}
			c, ok := thirdProbeCall(calls)
			if !ok {
				t.Fatalf("no third run among %d calls about the repository", len(probeCalls(calls)))
			}
			first := probeCalls(calls)[0]
			if c.cwd != first.cwd || !slices.Equal(c.argv, first.argv) {
				t.Errorf("the third run has %q in %s, want the argv %q and the folder %s of the first", c.argv, c.cwd, first.argv, first.cwd)
			}
			got := map[string]string{}
			for _, name := range configFreeNames {
				got[name], _ = lookupExact(c.env, name)
			}
			if got["GIT_CONFIG_COUNT"] != "0" || got["GIT_CONFIG_GLOBAL"] != "/dev/null" || got["GIT_CONFIG_NOSYSTEM"] != "1" {
				t.Errorf("the third run has %v, want GIT_CONFIG_COUNT=0, GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1", got)
			}
			drop := func(env []string) []string {
				return slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
					return slices.Contains(configFreeNames, envName(kv))
				})))
			}
			if rest, want := drop(c.env), drop(first.env); !slices.Equal(rest, want) {
				t.Errorf("the environment of the third run less the three = %q\nwant that of the first %q", rest, want)
			}
		})
	}
}

// TestThirdProbeEndsAtTheBoundOfTheFirstCall pins the time limit of the third
// run, with a lowered bound. On 52d82f0a, with the second and the third run
// held 20 s each, the reply came 30.3 s after the request with the error of a
// call that did not finish, and the third run ended before its hold (measured
// on Linux). So the third run ends at the bound counted from the start of the
// first call. Mutation: give the third run probeCallBound of its own (the
// reply comes after the bound plus the hold of the second run).
func TestThirdProbeEndsAtTheBoundOfTheFirstCall(t *testing.T) {
	f := newSeededShadowFixture(t)
	badIncludeConfig(t, f.repo)
	const bound, hold = 3 * time.Second, 2 * time.Second
	lowerProbeBound(t, bound, 100*time.Millisecond)
	// The second run waits for hold, then the real git answers it.
	slowGit(t, "rev-parse,--git-dir", "pre", hold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_DIR=/"+shadowProbeName)
	// The third run waits 20 s.
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "rev-parse,--git-dir")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND2", "GIT_CONFIG_GLOBAL=/dev/null")
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", strconv.FormatInt((20*time.Second).Milliseconds(), 10))
	timeLog := filepath.Join(t.TempDir(), "times")
	t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
	raw, calls := f.info(t, f.repo, nil)
	replied := time.Now()
	text := fmt.Sprintf("git could not check this repository on the remote machine: it did not finish within 30 seconds in %q; try again", f.repo)
	if want := probeErrorShapes(t, f, text)[0]; raw != want {
		t.Errorf("reply = %s\n  want %s", raw, want)
	}
	if _, ok := thirdProbeCall(calls); !ok || len(probeCalls(calls)) != 3 {
		t.Fatalf("%d calls about the repository, want three with a third run", len(probeCalls(calls)))
	}
	var start time.Time
	for _, line := range strings.Split(mustRead(t, timeLog), "\n") {
		words := strings.Split(line, "\x1f")
		if len(words) > 3 && words[0] == "start" && slices.Contains(words, "core.fsmonitor=") {
			ns, err := strconv.ParseInt(words[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			start = time.Unix(0, ns)
			break
		}
	}
	if start.IsZero() {
		t.Fatal("no start of the first call in the time log")
	}
	// The stub logs its start a little after the daemon started it.
	if took := replied.Sub(start); took < bound-time.Second/2 || took > bound+hold*6/10 {
		t.Errorf("the reply came %v after the start of the first call, want about the bound %v and less than %v", took, bound, bound+hold*6/10)
	}
}

// TestNoThirdProbeInABareRepository pins a bare repository with
// safe.bareRepository=explicit: the first call exits 128, the second run exits
// 0, no third run follows, and git.info answers "no repository" (52d82f0a,
// measured on Linux). claustrum skips the third run when the call runs in the
// git directory itself. Mutation: run the third run there too (it exits 0
// without the setting: the reply is the error).
func TestNoThirdProbeInABareRepository(t *testing.T) {
	f := newSeededShadowFixture(t)
	bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
	runGit(t, filepath.Dir(f.repo), "init", "-q", "--bare", "-b", "main", bare)
	dir := resolveTestRoot(t, t.TempDir())
	global := filepath.Join(dir, "gitconfig")
	writeFile(t, global, "[safe]\n\tbareRepository = explicit\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	raw, calls := f.info(t, bare, nil)
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`; raw != want {
		t.Errorf("reply = %s\n  want %s", raw, want)
	}
	if _, ok := secondProbeCall(calls); !ok || len(probeCalls(calls)) != 2 {
		t.Errorf("%d calls about the repository, want the first call and its second run", len(probeCalls(calls)))
	}
}

// TestProbeAnswerThenRefsNotSearchable pins a git directory whose `refs` gets
// mode 0600 right after a good call about the repository. 52d82f0a answers "no
// repository" with no later git call: git.info, git.status of a linked
// worktree and a create with not_a_repo (measured on Linux). Mutation: drop the
// git-directory test after a good answer (the replies stay, but later git calls
// run).
func TestProbeAnswerThenRefsNotSearchable(t *testing.T) {
	for i, method := range []string{"git.info", "git.status", "", "git.worktree_create"} {
		if method == "" {
			continue
		}
		t.Run(method, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			m := f.fiveRequests(nil)[i]
			refs := filepath.Join(f.gitDir, "refs")
			t.Cleanup(func() { _ = os.Chmod(refs, 0o755) })
			slowGit(t, "rev-parse,--git-dir", "post", 0, "", "")
			t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR=/"+shadowProbeName)
			t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "nosearch")
			t.Setenv("CLAUSTRUM_GITSTUB_LEAF", refs)
			raw, calls := f.call(t, m.method, m.params)
			want := `{"jsonrpc":"2.0","id":1,` + f.noRepositoryAnswers()[i] + `}`
			if i == 0 {
				want = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
			}
			if raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			if n := len(probeCalls(calls)); n != 1 {
				t.Errorf("%d calls about the repository, want 1", n)
			}
			if last := calls[len(calls)-1]; !last.isProbe() {
				t.Errorf("a call after the call about the repository: %q, want none", last.argv)
			}
		})
	}
}
