package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests pin how git.worktree_create answers when the caller's timeoutMs
// expires. The rules were measured against f6010b97 and 90fca6e6 on a macOS VM:
//
//   - T1: the deadline does not kill `git worktree add`. The reply waits for git,
//     then answers timeout "before the checkout started" and rolls back.
//   - T2: a deadline during the read-tree checkout kills it and rolls back. The
//     registrations directory stays. A branch the call created is deleted. An
//     attached existingBranch is kept. A retry at the same path succeeds.
//   - T3: the text after "during the checkout): " is the stderr of the killed git
//     on one line. With no stderr it is the exec error. Stdout is not used.
//   - T4: the deadline does not kill the copy step. The reply waits for it, then
//     answers timeout "after the checkout finished" and rolls back.
//
// Every git call goes through the "git-slow" stub (runGitSlow in
// helperproc_test.go). It runs the real git and slows the one call under test.

// wtFixture is a repository with a commit and a tracked .claude/settings.json.
// baseRepo also holds an ignored, untracked .claude/settings.local.json, so the
// copy step has work.
type wtFixture struct {
	realGit string
	top     string // the main working tree
	base    string // baseRepo: top, or a linked worktree of top
	regDir  string // <common git dir>/worktrees
}

func newWTFixture(t *testing.T, linked bool) wtFixture {
	t.Helper()
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	top := filepath.Join(root, "T")
	if err := os.MkdirAll(filepath.Join(top, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, top, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(top, "t.txt"), "t\n", 0o644)
	writeFile(t, filepath.Join(top, ".claude", "settings.json"), "{}\n", 0o644)
	writeFile(t, filepath.Join(top, ".gitignore"), ".claude/worktrees/\n.claude/settings.local.json\n", 0o644)
	runGit(t, top, "add", ".")
	runGit(t, top, "commit", "-q", "-m", "c0")
	runGit(t, top, "branch", "ex")
	f := wtFixture{realGit: realGit, top: top, base: top, regDir: filepath.Join(top, ".git", "worktrees")}
	if linked {
		f.base = filepath.Join(root, "LW")
		runGit(t, top, "worktree", "add", "-q", "-b", "lw", f.base)
	}
	writeFile(t, filepath.Join(f.base, ".claude", "settings.local.json"), "{\"local\":true}\n", 0o644)
	return f
}

// installGitSlowStub puts this test binary first on PATH as `git`, in the
// "git-slow" helper mode, with realGit as the git it passes calls through to.
func installGitSlowStub(t *testing.T, realGit string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin := t.TempDir()
	if runtime.GOOS == "windows" {
		// A copy, because a symlink needs a privilege on Windows.
		src, err := os.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = src.Close() }()
		dst, err := os.OpenFile(filepath.Join(bin, "git.exe"), os.O_CREATE|os.O_WRONLY, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		if err := dst.Close(); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(exe, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("symlink git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUSTRUM_TEST_HELPER", "git-slow")
	t.Setenv("CLAUSTRUM_GITSTUB_REAL", realGit)
	for _, k := range []string{"LOG", "CTXLOG", "STDERR_FILE", "STDERR2_FILE", "EXIT", "ACTION", "LEAF", "SNAP"} {
		t.Setenv("CLAUSTRUM_GITSTUB_"+k, "")
	}
	slowGit(t, "", "", 0, "", "")
}

// stubStderr makes the slowed call write exactly b to stderr. With second set, it
// is the payload of the attach fallback add (the call that holds --no-track).
func stubStderr(t *testing.T, b string, second bool) {
	t.Helper()
	name := "CLAUSTRUM_GITSTUB_STDERR_FILE"
	if second {
		name = "CLAUSTRUM_GITSTUB_STDERR2_FILE"
	}
	file := filepath.Join(t.TempDir(), "stderr.bin")
	if err := os.WriteFile(file, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(name, file)
}

// stubAction makes the slowed call change the leaf first. See gitStubAction.
func (f wtFixture) stubAction(t *testing.T, action string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", action)
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF", f.leaf())
}

// wantError fails the test unless raw carries exactly this error and errorCode.
func wantError(t *testing.T, raw, errText, code string) {
	t.Helper()
	want := `"error":` + jsonString(t, errText) + `,"errorCode":"` + code + `"`
	if !strings.Contains(raw, want) {
		t.Fatalf("reply = %s\nwant %s", raw, want)
	}
}

// leafEntries lists the names in the leaf, or nil when the leaf is gone.
func (f wtFixture) leafEntries(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(f.leaf())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// regs lists the registrations directory, or nil when it is gone.
func (f wtFixture) regs(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(f.regDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// slowGit chooses the call to slow. match is a comma-separated list of exact
// argv words. An empty match slows nothing.
func slowGit(t *testing.T, match, mode string, d time.Duration, stderr, stdout string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH", match)
	t.Setenv("CLAUSTRUM_GITSTUB_MODE", mode)
	t.Setenv("CLAUSTRUM_GITSTUB_MS", strconv.FormatInt(d.Milliseconds(), 10))
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR", stderr)
	t.Setenv("CLAUSTRUM_GITSTUB_STDOUT", stdout)
}

// create dispatches one git.worktree_create and returns the raw reply and its
// wall-clock time.
func (f wtFixture) create(t *testing.T, s *server, branch, existing string, timeoutMs int) (string, time.Duration) {
	t.Helper()
	extra := map[string]any{}
	if existing != "" {
		extra["existingBranch"] = existing
	}
	return f.createWith(t, s, branch, timeoutMs, extra)
}

// createWith is create with extra params, for example sourceBranch.
func (f wtFixture) createWith(t *testing.T, s *server, branch string, timeoutMs int, extra map[string]any) (string, time.Duration) {
	t.Helper()
	params := map[string]any{
		"baseRepo":     f.base,
		"branchName":   branch,
		"worktreePath": f.leaf(),
		"timeoutMs":    timeoutMs,
	}
	for k, v := range extra {
		params[k] = v
	}
	start := time.Now()
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", params))
	return raw, time.Since(start)
}

func (f wtFixture) leaf() string { return filepath.Join(f.base, ".claude", "worktrees", "w1") }

// hasRef reports whether refs/heads/<name> exists, asked of the real git.
func (f wtFixture) hasRef(t *testing.T, name string) bool {
	t.Helper()
	cmd := exec.Command(f.realGit, "-C", f.top, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return cmd.Run() == nil
}

// assertRolledBack checks the measured end state of a timeout rollback. The leaf
// is gone. The registrations directory exists and holds only the entries it held
// before the call. refs/heads/w1 is gone. In attach mode ex stays.
func (f wtFixture) assertRolledBack(t *testing.T, wantRegs []string, attach bool) {
	t.Helper()
	if _, err := os.Lstat(f.leaf()); !os.IsNotExist(err) {
		t.Errorf("leaf %s still exists after the rollback (Lstat err %v)", f.leaf(), err)
	}
	// The rollback keeps the leaf's parent, <baseRepo>/.claude/worktrees (measured).
	if fi, err := os.Stat(filepath.Dir(f.leaf())); err != nil || !fi.IsDir() {
		t.Errorf("%s after the rollback: %v, want it kept", filepath.Dir(f.leaf()), err)
	}
	ents, err := os.ReadDir(f.regDir)
	if err != nil {
		t.Errorf("registrations directory %s: %v, want it kept", f.regDir, err)
	}
	var regs []string
	for _, e := range ents {
		regs = append(regs, e.Name())
	}
	if !slices.Equal(regs, wantRegs) {
		t.Errorf("registrations = %q, want %q", regs, wantRegs)
	}
	if f.hasRef(t, "w1") {
		t.Errorf("refs/heads/w1 still exists after the rollback")
	}
	if attach && !f.hasRef(t, "ex") {
		t.Errorf("attach mode: refs/heads/ex was deleted, want it kept")
	}
}

// assertRetrySucceeds runs the same create again with nothing slowed and no
// deadline. The rollback leaves nothing that blocks it.
func (f wtFixture) assertRetrySucceeds(t *testing.T, s *server, branch, existing, wantBranch string) {
	t.Helper()
	slowGit(t, "", "", 0, "", "")
	raw, _ := f.create(t, s, branch, existing, 0)
	if !strings.Contains(raw, `"success":true`) || !strings.Contains(raw, `"branch":"`+wantBranch+`"`) {
		t.Fatalf("retry at the same path = %s, want success on branch %s", raw, wantBranch)
	}
	local := filepath.Join(f.leaf(), ".claude", "settings.local.json")
	if _, err := os.Stat(local); err != nil {
		t.Errorf("retry: copy step did not run (%v)", err)
	}
}

// killedSuffix is the exec error of a git that the deadline killed.
func killedSuffix() string {
	if runtime.GOOS == "windows" {
		return "exit status 1"
	}
	return "signal: killed"
}

// calibrate returns the slowest of two full creates through the stub, with
// nothing slowed. A deadline placed at twice this, plus a margin, falls after the
// add and the checkout on the same host under the same load.
func calibrate(t *testing.T, realGit string) time.Duration {
	t.Helper()
	var worst time.Duration
	for i := 0; i < 2; i++ {
		f := newWTFixture(t, false)
		s := newTestServer(t)
		raw, d := f.create(t, s, "w1", "", 0)
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("calibration create = %s, want success", raw)
		}
		worst = max(worst, d)
	}
	return worst
}

func TestWorktreeCreateCallerTimeout(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0 // D5 off: the caller's timeoutMs is the only deadline.

	installGitSlowStub(t, realGit)
	full := calibrate(t, realGit)
	// The deadline for the checkout and copy cases. The add and the checkout
	// finish well before it.
	lateMs := int((2*full + 500*time.Millisecond).Milliseconds())
	t.Logf("one full create takes %s, checkout/copy deadline %dms", full, lateMs)

	// T1: the add is slow. The deadline fires during the add. The add is not
	// killed, so the reply comes only after the add's sleep.
	t.Run("T1 add outlives the deadline", func(t *testing.T) {
		const addSleep = 1500 * time.Millisecond
		for _, tc := range []struct {
			name     string
			linked   bool
			existing string
			wantRegs []string
			retryBr  string
		}{
			{"plain", false, "", []string{}, "w1"},
			{"linked", true, "", []string{"LW"}, "w1"},
			{"attach", false, "ex", []string{}, "ex"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, tc.linked)
				s := newTestServer(t)
				slowGit(t, "worktree,add", "pre", addSleep, "", "")
				raw, elapsed := f.create(t, s, "w1", tc.existing, 100)
				want := `"error":"git worktree add timed out after 100ms (deadline expired before the checkout started)","errorCode":"timeout"`
				if !strings.Contains(raw, want) {
					t.Fatalf("reply = %s, want %s", raw, want)
				}
				if elapsed < addSleep {
					t.Errorf("reply after %s, before the add's %s sleep ended: the deadline killed the add", elapsed, addSleep)
				}
				f.assertRolledBack(t, tc.wantRegs, tc.existing != "")
				f.assertRetrySucceeds(t, s, "w1", tc.existing, tc.retryBr)
			})
		}
	})

	// T1, failed add: an add that fails after the deadline answers the normal
	// add-failure frame, not timeout. The reply still waits for the add. A branch
	// that existed before the call survives.
	t.Run("T1 failed add keeps a branch it did not create", func(t *testing.T) {
		const addSleep = 500 * time.Millisecond
		for _, tc := range []struct {
			name, mode, stderr string
			want               string // the whole error member, or a part of it
		}{
			{"synthetic", "fail", `fatal: synthetic add failure\n`,
				`"error":"git worktree add failed: fatal: synthetic add failure","errorCode":"worktree_add_failed"`},
			// The real git refuses, because w1 exists. Its wording depends on the git version.
			{"real", "pre", "", "a branch named 'w1' already exists"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				runGit(t, f.top, "branch", "w1")
				slowGit(t, "worktree,add", tc.mode, addSleep, tc.stderr, "")
				raw, elapsed := f.create(t, s, "w1", "", 100)
				if !strings.Contains(raw, tc.want) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
					t.Fatalf("reply = %s, want worktree_add_failed with %s", raw, tc.want)
				}
				if elapsed < addSleep {
					t.Errorf("reply after %s, before the add's %s sleep ended", elapsed, addSleep)
				}
				if !f.hasRef(t, "w1") {
					t.Errorf("the pre-existing branch w1 was deleted by the rollback")
				}
				if _, err := os.Lstat(f.leaf()); !os.IsNotExist(err) {
					t.Errorf("leaf %s still exists after the rollback (Lstat err %v)", f.leaf(), err)
				}
			})
		}
	})

	// The attach fallback. If `worktree add --no-checkout <leaf> <existingBranch>`
	// fails, the daemon runs `worktree add --no-track --no-checkout -b <branchName>
	// <leaf>` with no start point and goes on. The natural trigger is an
	// existingBranch that is checked out in baseRepo.
	t.Run("attach fallback", func(t *testing.T) {
		f := newWTFixture(t, false)
		s := newTestServer(t)
		log := filepath.Join(t.TempDir(), "git.log")
		t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
		raw, _ := f.create(t, s, "w1", "main", 0)
		want := `{"success":true,"path":` + jsonString(t, f.leaf()) + `,"sourceBranch":"main","branch":"w1"}`
		if !strings.Contains(raw, want) {
			t.Fatalf("reply = %s, want %s", raw, want)
		}
		if !f.hasRef(t, "w1") || !f.hasRef(t, "main") {
			t.Errorf("want both main and the new w1 after the fallback")
		}
		if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
			t.Errorf("fallback: checkout did not fill the worktree (%v)", err)
		}
		if _, err := os.Stat(filepath.Join(f.leaf(), ".claude", "settings.local.json")); err != nil {
			t.Errorf("fallback: copy step did not run (%v)", err)
		}
		adds := gitCalls(t, log, "worktree", "add")
		wantAdds := [][]string{
			{"worktree", "add", "--no-checkout", f.leaf(), "main"},
			{"worktree", "add", "--no-track", "--no-checkout", "-b", "w1", f.leaf()},
		}
		if len(adds) != len(wantAdds) {
			t.Fatalf("worktree add calls = %q, want %d", adds, len(wantAdds))
		}
		for i, w := range wantAdds {
			if !argvEndsWith(adds[i], w) {
				t.Errorf("add %d = %q, want it to end with %q", i, adds[i], w)
			}
		}
		// Both adds carry the same -C and -c options.
		if a, b := adds[0][:len(adds[0])-5], adds[1][:len(adds[1])-7]; !slices.Equal(a, b) {
			t.Errorf("the two adds differ before the subcommand: %q and %q", a, b)
		}
		rt := gitCalls(t, log, "read-tree")
		if len(rt) != 1 || rt[0][len(rt[0])-1] != "refs/heads/w1" {
			t.Errorf("read-tree calls = %q, want one that reads refs/heads/w1", rt)
		}
	})

	// The attach fallback fails too. The frame quotes the fallback's output, then
	// the refused attach's output. Nothing the call did not create is deleted.
	t.Run("attach fallback fails", func(t *testing.T) {
		f := newWTFixture(t, false)
		s := newTestServer(t)
		runGit(t, f.top, "branch", "w1")
		raw, _ := f.create(t, s, "w1", "main", 0)
		// The wording of git's two refusals depends on the git version. Each output
		// can start with git's graft-file hint, which holds escaped quotes.
		re := regexp.MustCompile(`"error":"git worktree add failed: (?:[^"\\]|\\.)*a branch named 'w1' already exists \(attaching to the existing branch main was refused first: (?:[^"\\]|\\.)*'main' is already (?:[^"\\]|\\.)*\)","errorCode":"worktree_add_failed"`)
		if !re.MatchString(raw) {
			t.Fatalf("reply = %s, want the two-output add failure", raw)
		}
		if !f.hasRef(t, "w1") || !f.hasRef(t, "main") {
			t.Errorf("a branch that existed before the call was deleted")
		}
		if _, err := os.Lstat(f.leaf()); !os.IsNotExist(err) {
			t.Errorf("leaf %s still exists after the rollback (Lstat err %v)", f.leaf(), err)
		}
	})

	// Both adds fail after the deadline. The frame is the two-output add failure,
	// not timeout, and the reply waits for both adds.
	t.Run("attach fallback fails past the deadline", func(t *testing.T) {
		const addSleep = 500 * time.Millisecond
		f := newWTFixture(t, false)
		s := newTestServer(t)
		slowGit(t, "worktree,add", "fail", addSleep, `fatal: synthetic add failure\n`, "")
		raw, elapsed := f.create(t, s, "w1", "ex", 100)
		want := `"error":"git worktree add failed: fatal: synthetic add failure (attaching to the existing branch ex was refused first: fatal: synthetic add failure)","errorCode":"worktree_add_failed"`
		if !strings.Contains(raw, want) {
			t.Fatalf("reply = %s, want %s", raw, want)
		}
		if elapsed < 2*addSleep {
			t.Errorf("reply after %s, before both adds' sleeps (%s) ended", elapsed, 2*addSleep)
		}
		if !f.hasRef(t, "ex") || f.hasRef(t, "w1") {
			t.Errorf("want ex kept and no w1")
		}
		if _, err := os.Lstat(f.leaf()); !os.IsNotExist(err) {
			t.Errorf("leaf %s still exists after the rollback (Lstat err %v)", f.leaf(), err)
		}
	})

	// The attach is refused, the fallback add succeeds, and the deadline expired
	// during the adds. The frame is timeout "before the checkout started". The
	// rollback deletes w1, which the fallback created, and keeps main.
	t.Run("attach fallback outlives the deadline", func(t *testing.T) {
		const addSleep = 500 * time.Millisecond
		f := newWTFixture(t, false)
		s := newTestServer(t)
		slowGit(t, "worktree,add", "pre", addSleep, "", "")
		raw, elapsed := f.create(t, s, "w1", "main", 100)
		want := `"error":"git worktree add timed out after 100ms (deadline expired before the checkout started)","errorCode":"timeout"`
		if !strings.Contains(raw, want) {
			t.Fatalf("reply = %s, want %s", raw, want)
		}
		if elapsed < 2*addSleep {
			t.Errorf("reply after %s, before both adds' sleeps (%s) ended", elapsed, 2*addSleep)
		}
		f.assertRolledBack(t, []string{}, false)
		if !f.hasRef(t, "main") {
			t.Errorf("refs/heads/main was deleted, want it kept")
		}
	})

	// T2 and T3: the read-tree checkout outlives the deadline and is killed.
	t.Run("T2 checkout killed", func(t *testing.T) {
		const never = 60 * time.Second
		for _, tc := range []struct {
			name, stderr, stdout string
			linked               bool
			existing             string
			wantRegs             []string
			wantDetail           string
			retryBr              string
		}{
			// T3: stdout is not used, and empty stderr falls back to the exec error.
			{"plain, stdout only", "", `STDOUT-LINE\n`, false, "", []string{}, killedSuffix(), "w1"},
			// T3: stderr on one line. Newlines become spaces, and the last one is trimmed.
			{"plain, stderr", `first line\nhint:\nsecond line\n`, `STDOUT-LINE\n`, false, "", []string{}, "first line hint: second line", "w1"},
			// T3: each \r, \n and \t becomes one space. Runs are not collapsed.
			{"plain, crlf", `A\r\nB\rC\tD\n`, "", false, "", []string{}, "A  B C D", "w1"},
			// T3: the 512-byte cap cuts the 2-byte é. Its first byte is dropped.
			{"plain, cut rune", strings.Repeat("a", 511) + "é" + `TAIL\n`, "", false, "", []string{}, strings.Repeat("a", 511), "w1"},
			{"linked", "", "", true, "", []string{"LW"}, killedSuffix(), "w1"},
			{"attach", "", "", false, "ex", []string{}, killedSuffix(), "ex"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, tc.linked)
				s := newTestServer(t)
				slowGit(t, "read-tree", "pre", never, tc.stderr, tc.stdout)
				raw, elapsed := f.create(t, s, "w1", tc.existing, lateMs)
				want := `"error":"git worktree add timed out after ` + strconv.Itoa(lateMs) +
					`ms (deadline expired during the checkout): ` + tc.wantDetail + `","errorCode":"timeout"`
				if !strings.Contains(raw, want) {
					t.Fatalf("reply = %s, want %s", raw, want)
				}
				if elapsed >= never/2 {
					t.Errorf("reply after %s: the deadline did not kill the checkout", elapsed)
				}
				f.assertRolledBack(t, tc.wantRegs, tc.existing != "")
				f.assertRetrySucceeds(t, s, "w1", tc.existing, tc.retryBr)
			})
		}
	})

	// T2: after the rollback, a retry at the same path with a new branch name
	// also succeeds.
	t.Run("T2 retry with a fresh branch", func(t *testing.T) {
		f := newWTFixture(t, false)
		s := newTestServer(t)
		slowGit(t, "read-tree", "post", 60*time.Second, "", "")
		raw, _ := f.create(t, s, "w1", "", lateMs)
		if !strings.Contains(raw, "(deadline expired during the checkout)") {
			t.Fatalf("reply = %s, want the during-the-checkout timeout", raw)
		}
		f.assertRolledBack(t, []string{}, false)
		f.assertRetrySucceeds(t, s, "w2", "", "w2")
		if f.hasRef(t, "w1") {
			t.Errorf("refs/heads/w1 exists after the fresh retry")
		}
	})

	// T4: the copy step (ls-files) outlives the deadline. It is not killed.
	t.Run("T4 copy step outlives the deadline", func(t *testing.T) {
		copySleep := time.Duration(lateMs)*time.Millisecond + time.Second
		for _, tc := range []struct {
			name     string
			linked   bool
			existing string
			wantRegs []string
			retryBr  string
		}{
			{"plain", false, "", []string{}, "w1"},
			{"linked", true, "", []string{"LW"}, "w1"},
			{"attach", false, "ex", []string{}, "ex"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, tc.linked)
				s := newTestServer(t)
				slowGit(t, "ls-files", "pre", copySleep, "", "")
				raw, elapsed := f.create(t, s, "w1", tc.existing, lateMs)
				want := `"error":"git worktree add timed out after ` + strconv.Itoa(lateMs) +
					`ms (deadline expired after the checkout finished)","errorCode":"timeout"`
				if !strings.Contains(raw, want) {
					t.Fatalf("reply = %s, want %s", raw, want)
				}
				if elapsed < copySleep {
					t.Errorf("reply after %s, before the copy step's %s sleep ended", elapsed, copySleep)
				}
				f.assertRolledBack(t, tc.wantRegs, tc.existing != "")
				f.assertRetrySucceeds(t, s, "w1", tc.existing, tc.retryBr)
			})
		}
	})

	// The checkout exits 0, but a descendant holds its output past the drain cap,
	// and the deadline expires during the drain. The copy step still runs. Then the
	// reply is timeout "after the checkout finished", and the rollback keeps the
	// registrations directory: no `git worktree remove` runs.
	t.Run("drain overrun runs the copy step, then rolls back", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the process-group reap of the holding descendant is Unix-only")
		}
		// The drain cap starts when git exits, before the deadline. It ends after it.
		drainCap := time.Duration(lateMs)*time.Millisecond + time.Second
		oldCap := worktreeCreateDrainCap
		t.Cleanup(func() { worktreeCreateDrainCap = oldCap })
		worktreeCreateDrainCap = drainCap
		for _, tc := range []struct {
			name     string
			linked   bool
			existing string
			wantRegs []string
		}{
			{"plain", false, "", []string{}},
			{"linked", true, "", []string{"LW"}},
			{"attach", false, "ex", []string{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, tc.linked)
				s := newTestServer(t)
				log := filepath.Join(t.TempDir(), "git.log")
				t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
				slowGit(t, "read-tree", "hold", drainCap+30*time.Second, "", "")
				raw, elapsed := f.create(t, s, "w1", tc.existing, lateMs)
				want := `"error":"git worktree add timed out after ` + strconv.Itoa(lateMs) +
					`ms (deadline expired after the checkout finished)","errorCode":"timeout"`
				if !strings.Contains(raw, want) {
					t.Fatalf("reply = %s, want %s", raw, want)
				}
				if elapsed < drainCap || elapsed >= drainCap+20*time.Second {
					t.Errorf("reply after %s, want it after the %s drain cap and before the holder ends", elapsed, drainCap)
				}
				f.assertRolledBack(t, tc.wantRegs, tc.existing != "")
				var after []string
				for _, c := range gitCalls(t, log) {
					if len(after) > 0 || slices.Contains(c, "read-tree") {
						after = append(after, strings.Join(c, " "))
					}
				}
				copied := false
				for _, c := range after {
					copied = copied || strings.Contains(c, " ls-files ")
					if strings.Contains(c, " worktree remove ") {
						t.Errorf("the drain rollback ran %q, want no git worktree remove", c)
					}
				}
				if !copied {
					t.Errorf("git calls after the checkout = %q, want the copy step's ls-files", after)
				}
			})
		}
	})
	// Q5: the killed checkout's text drops invalid bytes anywhere, and a text with
	// no printable rune falls back to the kill's exec error. The payloads and texts
	// were measured against f6010b97 and 90fca6e6 on a macOS VM.
	t.Run("Q5 killed checkout text", func(t *testing.T) {
		for _, tc := range []struct{ name, stderr, want string }{
			{"inval_mid", "abc\xffdef\n", "abcdef"},
			{"inval_trunc3", "ab\xe2\x82 cd\n", "ab cd"},
			{"ctl", "X\x01Y\x1bZ\x7fW\n", "X Y Z W"},
			{"ctlonly", "\x01\x02\x1b\n", killedSuffix()},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				stubStderr(t, tc.stderr, false)
				slowGit(t, "read-tree", "pre", 60*time.Second, "", "")
				raw, _ := f.create(t, s, "w1", "", lateMs)
				wantError(t, raw, "git worktree add timed out after "+strconv.Itoa(lateMs)+
					"ms (deadline expired during the checkout): "+tc.want, "timeout")
				f.assertRolledBack(t, []string{}, false)
			})
		}
	})
}

// gitCalls reads the stub's argv log and returns the calls that hold every word
// in words as an exact argument.
func gitCalls(t *testing.T, log string, words ...string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("git stub log: %v", err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		argv := strings.Split(line, "\x1f")
		keep := true
		for _, w := range words {
			keep = keep && slices.Contains(argv, w)
		}
		if keep {
			calls = append(calls, argv)
		}
	}
	return calls
}

// argvEndsWith reports whether argv ends with tail.
func argvEndsWith(argv, tail []string) bool {
	return len(argv) >= len(tail) && slices.Equal(argv[len(argv)-len(tail):], tail)
}

// jsonString returns s as a JSON string literal, as the daemon encodes it.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// out runs the real git in dir and returns its trimmed stdout.
func (f wtFixture) out(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command(f.realGit, append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %q: %v", args, err)
	}
	return strings.TrimSpace(string(b))
}

// lastCallHolds reports whether the last git call in the stub's argv log holds
// every word in words. After a failed add with no fallback, that call is the add.
func lastCallHolds(t *testing.T, log string, words ...string) bool {
	t.Helper()
	calls := gitCalls(t, log)
	if len(calls) == 0 {
		return false
	}
	last := calls[len(calls)-1]
	for _, w := range words {
		if !slices.Contains(last, w) {
			return false
		}
	}
	return true
}

// TestWorktreeCreateMeasuredRules pins the git.worktree_create rules measured
// against f6010b97 and 90fca6e6 on macOS and Windows VMs, where both builds agree:
//
//   - Q1: the attach fallback add gets the start point that sourceBranch resolved
//     to, and the checkout reads it.
//   - Q2: no fallback when the failed attach add deleted or replaced the leaf.
//   - Q3/Q4: the failure texts follow worktreeGitText, each part of the
//     two-part frame on its own.
//   - Q6: after a failed add with no fallback, no git call runs, and the leaf is
//     removed only if it is empty. The registration and the branch stay.
//   - U1: the read-tree runs in the leaf with no -C, --git-dir of baseRepo, and a
//     temporary GIT_INDEX_FILE, whose index then becomes the worktree's own.
//
// Every git call goes through the "git-slow" stub, as in
// TestWorktreeCreateCallerTimeout.
func TestWorktreeCreateMeasuredRules(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)

	t.Run("Q1 fallback keeps the resolved start point", func(t *testing.T) {
		f := newWTFixture(t, false)
		s := newTestServer(t)
		// c1 on feat adds feat.txt. main (c0) stays checked out in baseRepo, so the
		// attach to main is refused and the fallback runs.
		runGit(t, f.top, "checkout", "-q", "-b", "feat")
		writeFile(t, filepath.Join(f.top, "feat.txt"), "feat\n", 0o644)
		runGit(t, f.top, "add", "feat.txt")
		runGit(t, f.top, "commit", "-q", "-m", "c1")
		runGit(t, f.top, "checkout", "-q", "main")
		c1 := f.out(t, f.top, "rev-parse", "feat")
		log := filepath.Join(t.TempDir(), "git.log")
		t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
		raw, _ := f.createWith(t, s, "w1", 0, map[string]any{"existingBranch": "main", "sourceBranch": "feat"})
		want := `{"success":true,"path":` + jsonString(t, f.leaf()) + `,"sourceBranch":"feat","branch":"w1"}`
		if !strings.Contains(raw, want) {
			t.Fatalf("reply = %s, want %s", raw, want)
		}
		if got := f.out(t, f.top, "rev-parse", "refs/heads/w1"); got != c1 {
			t.Errorf("w1 = %s, want the source commit %s", got, c1)
		}
		if _, err := os.Stat(filepath.Join(f.leaf(), "feat.txt")); err != nil {
			t.Errorf("the worktree misses feat.txt of the source commit: %v", err)
		}
		adds := gitCalls(t, log, "worktree", "add")
		if len(adds) != 2 || !argvEndsWith(adds[1], []string{"--no-track", "--no-checkout", "-b", "w1", f.leaf(), c1}) {
			t.Errorf("worktree add calls = %q, want the fallback to end with -b w1 <leaf> %s", adds, c1)
		}
		rt := gitCalls(t, log, "read-tree")
		if len(rt) != 1 || rt[0][len(rt[0])-1] != c1 {
			t.Errorf("read-tree calls = %q, want one that reads %s", rt, c1)
		}
	})

	// Q2: the failed attach add deleted the leaf, or put a new empty directory in
	// its place. No fallback runs. The frame quotes the attach add alone, and the
	// leaf is absent after. A leaf left as it was gets the fallback (control).
	// "replparent" replaces the leaf's parent and makes a new empty leaf in it. The
	// references keep that new leaf (Q2_replpfail, 20 of 20 runs on Linux and macOS
	// VMs). f6010b97 also kept it in 10 of 10 runs on a Windows VM.
	t.Run("Q2 no fallback after the leaf changed", func(t *testing.T) {
		for _, tc := range []struct {
			action   string
			fallback bool
		}{{"rm", false}, {"repl", false}, {"replparent", false}, {"", true}} {
			t.Run("action "+tc.action, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				log := filepath.Join(t.TempDir(), "git.log")
				t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
				t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
				f.stubAction(t, tc.action)
				// Only the attach add holds the word ex.
				slowGit(t, "worktree,add,ex", "fail", 0, `fatal: synthetic add failure\n`, "")
				raw, _ := f.create(t, s, "w1", "ex", 0)
				adds := gitCalls(t, log, "worktree", "add")
				if tc.fallback {
					if !strings.Contains(raw, `"success":true`) || len(adds) != 2 {
						t.Fatalf("control: reply = %s, adds = %q, want the fallback to succeed", raw, adds)
					}
					return
				}
				wantError(t, raw, "git worktree add failed: fatal: synthetic add failure", "worktree_add_failed")
				if len(adds) != 1 {
					t.Errorf("worktree add calls = %q, want the attach add only", adds)
				}
				if tc.action == "replparent" {
					if got := f.leafEntries(t); got == nil || len(got) != 0 {
						t.Errorf("leaf entries = %q, want the new empty leaf kept", got)
					}
				} else if f.leafEntries(t) != nil {
					t.Errorf("leaf %s exists after the reply, want it absent", f.leaf())
				}
				if f.hasRef(t, "w1") || !f.hasRef(t, "ex") {
					t.Errorf("want no w1 and ex kept")
				}
			})
		}
	})

	// Q3: the two-part frame. Each part is made by worktreeGitText on its own. The
	// attach add prints the first payload and the fallback add the second.
	t.Run("Q3 two-part frame", func(t *testing.T) {
		for _, tc := range []struct{ name, attach, fallback, want string }{
			{"ctl", "A\r\nB\tC\x01D\n", "F\rG\tH\x1bI\n", "git worktree add failed: F G H I (attaching to the existing branch ex was refused first: A  B C D)"},
			{"inval", "att\xffA\xc3(\n", "fb\xe2\x82B\n", "git worktree add failed: fbB (attaching to the existing branch ex was refused first: attA()"},
			{"empty", "", "", "git worktree add failed: exit status 128 (attaching to the existing branch ex was refused first: exit status 128)"},
			{"long", "ATT:" + strings.Repeat("a", 600) + "\n", "FB:" + strings.Repeat("b", 600) + "\n",
				"git worktree add failed: FB:" + strings.Repeat("b", 509) + " (attaching to the existing branch ex was refused first: ATT:" + strings.Repeat("a", 508) + ")"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
				stubStderr(t, tc.attach, false)
				stubStderr(t, tc.fallback, true)
				slowGit(t, "worktree,add", "fail", 0, "", "")
				raw, _ := f.create(t, s, "w1", "ex", 0)
				wantError(t, raw, tc.want, "worktree_add_failed")
			})
		}
	})

	// Q4: the add-failure and checkout-failure frames. stdout is not quoted.
	t.Run("Q4 failure texts", func(t *testing.T) {
		for _, tc := range []struct{ name, match, prefix, stderr, stdout, want string }{
			{"add inval", "worktree,add", "git worktree add failed: ", "abc\xffdef\xc3(ghi\xe2\x82 jkl\n", "", "abcdef(ghi jkl"},
			{"add stdout", "worktree,add", "git worktree add failed: ", "ERR-LINE\n", `OUT-LINE\n`, "ERR-LINE"},
			{"add empty", "worktree,add", "git worktree add failed: ", "", "", "exit status 128"},
			{"checkout stdout", "read-tree", "git worktree add failed (checkout): ", "ERR-LINE\n", `OUT-LINE\n`, "ERR-LINE"},
			{"checkout uni", "read-tree", "git worktree add failed (checkout): ", "a\u0085b\u00a0c\u200bd\u2028e\u009bf\n", "", "a b c d e f"},
			{"checkout ctlonly", "read-tree", "git worktree add failed (checkout): ", "\x01\x02\x1b\n", "", "exit status 128"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
				stubStderr(t, tc.stderr, false)
				slowGit(t, tc.match, "fail", 0, "", tc.stdout)
				raw, _ := f.create(t, s, "w1", "", 0)
				wantError(t, raw, tc.prefix+tc.want, "worktree_add_failed")
				if f.leafEntries(t) != nil {
					t.Errorf("leaf %s exists after the rollback", f.leaf())
				}
			})
		}
	})

	// Q6: a failed add with no fallback runs no git call after it and removes the
	// leaf only if it is empty. What the failed add made stays.
	t.Run("Q6 failed add keeps what it made", func(t *testing.T) {
		for _, tc := range []struct {
			name, mode, action, existing string
			wantLeaf, wantRegs           []string
			wantW1                       bool
		}{
			// The add registered the worktree and made w1, then exited 128.
			{"postfail", "postfail", "", "", []string{".git"}, []string{"w1"}, true},
			// The leaf holds a file the add left.
			{"fillfail", "fail", "fill", "", []string{"junk.txt"}, nil, false},
			// The same with the attach add and its fallback: both fail.
			{"fillfail attach", "fail", "fill", "ex", []string{"junk.txt"}, nil, false},
			// The leaf is empty: it goes.
			{"empty", "fail", "", "", nil, nil, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				log := filepath.Join(t.TempDir(), "git.log")
				t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
				t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
				f.stubAction(t, tc.action)
				slowGit(t, "worktree,add", tc.mode, 0, `fatal: synthetic add failure\n`, "")
				raw, _ := f.create(t, s, "w1", tc.existing, 0)
				// In postfail the real add runs first. Its graft-file hints can fill
				// the 512-byte cap before the stub's own line. The other rows still
				// carry the stub's line.
				if !strings.Contains(raw, `"error":"git worktree add failed: `) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
					t.Fatalf("reply = %s, want the add failure", raw)
				}
				if tc.mode != "postfail" && !strings.Contains(raw, "fatal: synthetic add failure") {
					t.Errorf("reply = %s, want git's stderr in the frame", raw)
				}
				if !lastCallHolds(t, log, "worktree", "add") {
					t.Errorf("git calls = %q, want none after the failed add", gitCalls(t, log))
				}
				if got := f.leafEntries(t); !slices.Equal(got, tc.wantLeaf) {
					t.Errorf("leaf entries = %q, want %q", got, tc.wantLeaf)
				}
				if got := f.regs(t); !slices.Equal(got, tc.wantRegs) {
					t.Errorf("registrations = %q, want %q", got, tc.wantRegs)
				}
				if f.hasRef(t, "w1") != tc.wantW1 {
					t.Errorf("refs/heads/w1 exists = %v, want %v", !tc.wantW1, tc.wantW1)
				}
			})
		}
	})

	// U1: the shape of the read-tree checkout and its config precursor.
	t.Run("U1 checkout shape", func(t *testing.T) {
		for _, linked := range []bool{false, true} {
			t.Run("linked="+strconv.FormatBool(linked), func(t *testing.T) {
				f := newWTFixture(t, linked)
				s := newTestServer(t)
				ctxlog := filepath.Join(t.TempDir(), "ctx.log")
				t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
				raw, _ := f.create(t, s, "w1", "", 0)
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("reply = %s, want success", raw)
				}
				wantGitDir := filepath.Join(f.top, ".git")
				if linked {
					wantGitDir = filepath.Join(f.top, ".git", "worktrees", "LW")
				}
				wantGitDir = canonicalPath(wantGitDir)
				b, err := os.ReadFile(ctxlog)
				if err != nil {
					t.Fatal(err)
				}
				var rt, pre, rtEnv, preEnv []string
				var rtCwd, preCwd, index string
				for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
					parts := strings.SplitN(line, "\x1e", 5)
					argv := strings.Split(parts[2], "\x1f")
					env := strings.Split(parts[3], "\x1f")
					if slices.Contains(argv, "read-tree") {
						rt, rtCwd, index, rtEnv = argv, parts[0], parts[1], env
					} else if len(argv) == 5 && argv[1] == "config" && strings.HasPrefix(argv[0], "--git-dir=") {
						pre, preCwd, preEnv = argv, parts[0], env
					}
				}
				if rt == nil || pre == nil {
					t.Fatalf("calls:\n%s\nwant a read-tree and its --git-dir config precursor", b)
				}
				for _, cwd := range []string{rtCwd, preCwd} {
					if canonicalPath(cwd) != canonicalPath(f.leaf()) {
						t.Errorf("working directory = %s, want the leaf %s", cwd, f.leaf())
					}
				}
				gitDirArg := func(a string) string { return canonicalPath(strings.TrimPrefix(a, "--git-dir=")) }
				if !slices.Equal(pre[1:], []string{"config", "-z", "--list", "--name-only"}) || gitDirArg(pre[0]) != wantGitDir {
					t.Errorf("precursor = %q, want --git-dir=%s config -z --list --name-only", pre, wantGitDir)
				}
				profile := hardenedProfileArgs(false)
				tail := []string{"-c", "core.splitIndex=false", "-c", "core.commitGraph=false",
					"--git-dir=GITDIR", "--work-tree=" + f.leaf(),
					"read-tree", "-u", "--reset", "--no-recurse-submodules", "refs/heads/w1"}
				got := slices.Clone(rt)
				gd := len(profile) + 4
				if len(got) == len(profile)+len(tail) && strings.HasPrefix(got[gd], "--git-dir=") && gitDirArg(got[gd]) == wantGitDir {
					got[gd] = "--git-dir=GITDIR"
				}
				if !slices.Equal(got, append(slices.Clone(profile), tail...)) {
					t.Errorf("read-tree argv = %q\nwant %q (GITDIR = %s), with no -C", rt, append(profile, tail...), wantGitDir)
				}
				if filepath.Base(index) != "index" || !strings.HasPrefix(filepath.Base(filepath.Dir(index)), checkoutIndexTempPrefix) {
					t.Errorf("GIT_INDEX_FILE = %q, want <temp>/%s*/index", index, checkoutIndexTempPrefix)
				}
				if _, err := os.Lstat(filepath.Dir(index)); !os.IsNotExist(err) {
					t.Errorf("the temporary index directory %s remains (Lstat err %v)", filepath.Dir(index), err)
				}
				if _, err := os.Stat(filepath.Join(f.regDir, "w1", "index")); err != nil {
					t.Errorf("the registration has no index: %v", err)
				}
				if st := f.out(t, f.leaf(), "status", "--porcelain"); st != "" {
					t.Errorf("git status in the new worktree = %q, want clean", st)
				}
				// Both calls carry the GIT_COMMON_DIR pin of the trust check: the main
				// repository's git directory, for a plain and a linked baseRepo. The
				// read-tree also turns off replace objects and grafts. f6010b97 sets all
				// three on its read-tree (Linux and Windows argv captures).
				wantCommon := canonicalPath(filepath.Join(f.top, ".git"))
				if canonicalPath(rtEnv[0]) != wantCommon || canonicalPath(preEnv[0]) != wantCommon {
					t.Errorf("GIT_COMMON_DIR = %q (read-tree), %q (precursor), want %s", rtEnv[0], preEnv[0], wantCommon)
				}
				if rtEnv[1] != "1" || rtEnv[2] != os.DevNull {
					t.Errorf("read-tree GIT_NO_REPLACE_OBJECTS = %q, GIT_GRAFT_FILE = %q, want 1 and %s", rtEnv[1], rtEnv[2], os.DevNull)
				}
			})
		}
	})
}
