package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// failCallSeven makes the stub fail call 7 of the start test, the listing of the
// test config from standard input, with exit 1 and the text stderr.
func failCallSeven(t *testing.T, stderr string) {
	t.Helper()
	slowGit(t, "--no-includes,--file,-,--list,-z", "fail", 0, stderr, "")
}

// failVersion makes every `git version` call of the stub exit with code and write
// stderr.
func failVersion(t *testing.T, code, stderr string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "version")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", code)
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", stderr)
}

// startTextShapes are the frames of the five methods of fiveRequests for the
// failure text of a start test, in that order.
func startTextShapes(t *testing.T, f shadowFixture, text string) []string {
	t.Helper()
	q := jsonQuoted(t, text)
	object := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + q + `}}`
	return []string{object, object, object,
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + q + `,"errorCode":"worktree_add_failed"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
			jsonQuoted(t, "failed to remove worktree: could not check whether "+f.wt+" is locked: "+text) + `}}`,
	}
}

// versionsAfterCallSeven counts the plain `git version` calls right after call 7.
func versionsAfterCallSeven(t *testing.T, calls []envCall) int {
	t.Helper()
	n := 0
	for i, c := range calls {
		if i > 0 && c.plainVersion(t) && slices.Contains(calls[i-1].argv, "--no-includes") {
			n++
		}
	}
	return n
}

// TestStartTestCallSevenWithAFailedVersion pins the text of a start test whose call
// 7 fails while `git version` fails too. 5fd08069 runs one plain `git version` call
// and answers its text alone, `git cannot run on this host; git not run: <exit>:
// <line>`, in the frame of each method (cells E1 to E5 of round task083-01, Linux
// VM, E1 and E2 on macOS and Windows VMs). git.worktree_remove deletes nothing there
// (cell E5). The failure is kept: the same request again makes no git call (cell
// E7). Mutation: drop the cannotRunText branch in shadowTestCalls, or wrap its text
// in gate.
func TestStartTestCallSevenWithAFailedVersion(t *testing.T) {
	line := "fatal: bad config line 3 in file /h/.gitconfig"
	text := "git cannot run on this host; git not run: exit status 128: " + line
	for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
		t.Run(method, func(t *testing.T) {
			f := newShadowFixture(t)
			failCallSeven(t, "boom\n")
			failVersion(t, "128", line+"\n")
			wtBefore := entryNames(t, f.wt)
			m := f.fiveRequests(nil)[i]
			raw, calls := f.call(t, m.method, m.params)
			if want := startTextShapes(t, f, text)[i]; raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			if n := versionsAfterCallSeven(t, calls); n != 1 {
				t.Errorf("%d plain version calls after call 7, want 1", n)
			}
			if again, calls := f.call(t, m.method, m.params); again != raw || len(calls) != 0 {
				t.Errorf("again = %s with %d calls, want the kept text and no call", again, len(calls))
			}
			if got := entryNames(t, f.wt); !slices.Equal(got, wtBefore) {
				t.Errorf("the worktree of the fixture holds %v, want %v", got, wtBefore)
			}
			if _, err := os.Stat(filepath.Join(f.gitDir, "worktrees", "wr")); err != nil {
				t.Errorf("the registration of the fixture: %v", err)
			}
			if out := gitOut(t, f.repo, "branch", "--list", "wr"); !strings.Contains(out, "wr") {
				t.Errorf("branch wr is gone: %q", out)
			}
		})
	}
}

// TestStartTestCallSevenWithAWorkingVersion pins the cause of a start test whose
// call 7 fails while `git version` works. 5fd08069 answers `Claude could not test
// git on the remote machine (this repository's git config cannot be used: boom).
// Check free disk space and quota there, then try again.` after one `git version`
// call (cells E9 and E9c of round task083-01, Linux VM). Mutation: name the exec
// error in place of the error lines of call 7.
func TestStartTestCallSevenWithAWorkingVersion(t *testing.T) {
	f := newShadowFixture(t)
	failCallSeven(t, "boom\n")
	text := failedTestText("this repository's git config cannot be used: boom")
	for i, m := range f.fiveRequests(nil)[:4] {
		f.s.gitShadow.mu.Lock()
		f.s.gitShadow.failure = ""
		f.s.gitShadow.mu.Unlock()
		raw, calls := f.call(t, m.method, m.params)
		if want := startTextShapes(t, f, text)[i]; raw != want {
			t.Errorf("%s = %s\n  want %s", m.method, raw, want)
		}
		if n := versionsAfterCallSeven(t, calls); n != 1 {
			t.Errorf("%s: %d plain version calls after call 7, want 1", m.method, n)
		}
	}
}

// TestProbeExit128WithABrokenConfig pins the answer of each method after a shadow
// verdict, when the call about the repository exits 128 with the text of a broken
// ~/.gitconfig. 5fd08069 then runs one plain `git version` call. If that call
// fails, each method answers its text in its frame: -32603 on git.info,
// git.status and git.list_branches, worktree_add_failed on git.worktree_create,
// and the lock-check text on git.worktree_remove, with two such calls and no
// delete (cell E8 of round task083-01 and cells E8s, E8l, E8c and E8r of round
// task083-02, Linux VM). If it works, git.info answers the error of the call, as
// 5fd08069 does for another text with exit 128 (cell P8 of round task073-03,
// Linux VM). Mutation: drop the cannotRunText branch in askGit, or treat every
// text as no repository in probeSaysNoRepo.
func TestProbeExit128WithABrokenConfig(t *testing.T) {
	line := "fatal: bad config line 3 in file /h/.gitconfig"
	text := "git cannot run on this host; git not run: exit status 128: " + line
	countAfterProbe := func(t *testing.T, calls []envCall) (probes, versions int) {
		for i, c := range calls {
			if i > 0 && c.plainVersion(t) && calls[i-1].isProbe() {
				versions++
			}
		}
		return len(probeCalls(calls)), versions
	}
	for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
		t.Run("version fails/"+method, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			answerProbe(t, 128, "", line+"\n")
			failVersion(t, "128", line+"\n")
			wtBefore := entryNames(t, f.wt)
			m := f.fiveRequests(nil)[i]
			raw, calls := f.call(t, m.method, m.params)
			if want := probeErrorShapes(t, f, text)[i]; raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			want := 1
			if method == "git.worktree_remove" {
				want = 2
			}
			if probes, versions := countAfterProbe(t, calls); probes != want || versions != want {
				t.Errorf("%d calls about the repository and %d plain version calls after them, want %d and %d", probes, versions, want, want)
			}
			if got := entryNames(t, f.wt); !slices.Equal(got, wtBefore) {
				t.Errorf("the worktree of the fixture holds %v, want %v", got, wtBefore)
			}
			if _, err := os.Stat(filepath.Join(f.gitDir, "worktrees", "wr")); err != nil {
				t.Errorf("the registration of the fixture: %v", err)
			}
			if out := gitOut(t, f.repo, "branch", "--list", "wr"); !strings.Contains(out, "wr") {
				t.Errorf("branch wr is gone: %q", out)
			}
			if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
				t.Errorf("a worktree was made: %v", err)
			}
		})
	}
	t.Run("version works", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		answerProbe(t, 128, "", line+"\n")
		raw, calls := f.info(t, f.repo, nil)
		if want := probeErrorShapes(t, f, repoCheckFailedText(f.repo, line))[0]; raw != want {
			t.Errorf("git.info = %s\n  want %s", raw, want)
		}
		if probes, versions := countAfterProbe(t, calls); probes != 1 || versions != 1 {
			t.Errorf("%d calls about the repository and %d plain version calls after it, want 1 and 1", probes, versions)
		}
	})
}

// TestProbeFailureWithAFailedVersion pins cell P14x of round task073-03 (Linux
// VM). The call about the repository exits 1 with `fatal: not a git repository`,
// and the `git version` call after it exits 1 with `boom`. 5fd08069 answers the
// text of that version call on git.info and on git.worktree_create. Mutation:
// return repoCheckFailedText in askGit without the cannotRunText branch.
func TestProbeFailureWithAFailedVersion(t *testing.T) {
	text := gitCannotRunPrefix + "exit status 1: boom"
	// The index of git.info and of git.worktree_create in fiveRequests.
	for _, i := range []int{0, 3} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			f := newSeededShadowFixture(t)
			answerProbe(t, 1, "", "fatal: not a git repository\n")
			failVersion(t, "1", "boom\n")
			m := f.fiveRequests(nil)[i]
			if raw, _ := f.call(t, m.method, m.params); raw != probeErrorShapes(t, f, text)[i] {
				t.Errorf("%s = %s\n  want %s", m.method, raw, probeErrorShapes(t, f, text)[i])
			}
		})
	}
}

// TestGitInfoGoneGitFileRunsTheStartTest pins git.info as the first git request of
// a daemon on a folder whose `.git` file names a missing path. 5fd08069 runs the
// start test there and answers "no repository" with "settingsControl":"shadow" (cell
// V3 of round task083-01, Linux, macOS and Windows VMs). It does the same for a
// missing entry under `worktrees` of a missing repository (cell V4g of round
// task083-02, Linux VM). A missing entry under `worktrees` of an existing git
// directory, a `.git` file with no gitdir line and a request with
// "shadowDirOff":true run no test (cells V4, V2 and V7 of round task083-01). The
// stub answers the listing in the folder with the text of an older git, so the
// rows do not depend on the git of the host. Call 7 of the test stays real.
// Mutation: set goneGitFile to false or true in judgeGitDir, test any stat error
// in place of a missing path, or drop the gate call in gitInfo.
func TestGitInfoGoneGitFileRunsTheStartTest(t *testing.T) {
	// The daemon passes GIT_COMMON_DIR as the `.git` file spells it, and the file
	// has forward slashes on every system. So the suffixes have them too.
	nowhere := "nowhere/gitdir"
	entry := "worktrees/n1"
	for _, row := range []struct {
		name, text, end string
		off             bool
		want            string
		test            bool
	}{
		{"V3 missing path", "gitdir: <DIR>/nowhere/gitdir\n", nowhere, false, noRepoShadow, true},
		{"V4 missing worktree entry", "gitdir: <GITDIR>/worktrees/n1\n", entry, false, noRepoLine, false},
		{"V4g missing entry of a missing repository", "gitdir: <DIR>/gone/.git/worktrees/n1\n", entry, false, noRepoShadow, true},
		{"V2 no gitdir line", "hello\n", "", false, noRepoLine, false},
		{"V7 with the switch", "gitdir: <DIR>/nowhere/gitdir\n", nowhere, true, noRepoOff, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShadowFixture(t)
			dir := resolveTestRoot(t, t.TempDir())
			g := filepath.Join(dir, "G")
			if err := os.MkdirAll(g, 0o755); err != nil {
				t.Fatal(err)
			}
			text := strings.NewReplacer("<DIR>", filepath.ToSlash(dir), "<GITDIR>", filepath.ToSlash(f.gitDir)).Replace(row.text)
			writeFile(t, filepath.Join(g, ".git"), text, 0o644)
			if row.end != "" {
				stubListing(t, 128, "")
				stubStderr(t, "fatal: not a git repository: "+strings.TrimSpace(strings.TrimPrefix(text, "gitdir: "))+"\n", false)
				t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+row.end)
			}
			var extra map[string]any
			if row.off {
				extra = map[string]any{"shadowDirOff": true}
			}
			raw, calls := f.info(t, g, extra)
			if raw != row.want {
				t.Errorf("git.info = %s\n  want %s", raw, row.want)
			}
			if got := len(testCalls(calls)) > 0; got != row.test {
				t.Errorf("start test ran: %v, want %v", got, row.test)
			}
		})
	}
}
