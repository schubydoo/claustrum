package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cleanOne runs cleanGitChildEnv on one entry and returns what is left of it:
// the whole entry, or "" when it went.
func cleanOne(kv string, windows bool) string {
	out := cleanGitChildEnv([]string{kv}, windows)
	if len(out) == 0 {
		return ""
	}
	return out[0]
}

// The rows of Linux and macOS, rounds task004-01 and task004-02 (Linux and macOS
// VMs). W is the folder of the logging wrapper there.
func TestCleanGitChildEnvUnixRows(t *testing.T) {
	const w = "/home/u/t4/W"
	for _, tc := range []struct{ in, want string }{
		{"PATH=.::rel/bin:" + w + `:/usr/bin:/bin:/usr/bin:/a;b:/c"d`, "PATH=" + w + ":/usr/bin:/bin:/usr/bin"},
		{"PATH=" + w + ":/usr/bin:/bad\ndir:/bin", "PATH=" + w + ":/usr/bin:/bin"},
		{"LD_LIBRARY_PATH=rel:/usr/lib:", "LD_LIBRARY_PATH=/usr/lib"},
		{"LD_LIBRARY_PATH=/a:/b", "LD_LIBRARY_PATH=/a:/b"},
		{"DYLD_LIBRARY_PATH=/abs/x:rel/y", "DYLD_LIBRARY_PATH=/abs/x"},
		{"DYLD_INSERT_LIBRARIES=/abs/a.dylib:rel/b.dylib", "DYLD_INSERT_LIBRARIES=/abs/a.dylib"},
		{"DYLD_FALLBACK_LIBRARY_PATH=/abs/z::", "DYLD_FALLBACK_LIBRARY_PATH=/abs/z"},
		{"GIT_EXEC_PATH=rel/exec", ""},
		{"GIT_EXEC_PATH=", ""},
		{"DYLD_FRAMEWORK_PATH=rel", ""},
		{"DYLD_FALLBACK_FRAMEWORK_PATH=.", ""},
		{"path=rel:/abs", "path=rel:/abs"},
		{"LD_PRELOAD=./nonexist.so libdoesnotexist.so", "LD_PRELOAD=libdoesnotexist.so"},
		{"LD_PRELOAD=/abs/a.so:libb.so c/d.so libe.so", "LD_PRELOAD=/abs/a.so:libb.so:libe.so"},
		{"LD_AUDIT=libx.so liby.so:/abs/z.so", "LD_AUDIT=libx.so liby.so:/abs/z.so"},
		{"LD_AUDIT=a/b.so", ""},
		{"GIT_SSH=/abs/ssh", "GIT_SSH=/abs/ssh"},
		{"GIT_SSH=ssh", "GIT_SSH=ssh"},
		{"GIT_SSH=", "GIT_SSH="},
		{"GIT_SSH=bin/ssh", ""},
		{`GIT_SSH=bin\ssh`, ""},
		{"MARKER_KEEP=rel:./x", "MARKER_KEEP=rel:./x"},
		{"GIT_SSH_COMMAND=rel/ssh", "GIT_SSH_COMMAND=rel/ssh"},
		{"GIT_FOO=x", "GIT_FOO=x"},
	} {
		if got := cleanOne(tc.in, false); got != tc.want {
			t.Errorf("Linux and macOS: %q gives %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The rows of Windows, rounds task004-01 and task004-02 (Windows VM). This test
// runs the rules of Windows on every system.
func TestCleanGitChildEnvWindowsRows(t *testing.T) {
	const w = `C:\o\wrap`
	seven := strings.Join([]string{
		`C:\WINDOWS\system32`, `C:\WINDOWS`, `C:\WINDOWS\System32\Wbem`,
		`C:\WINDOWS\System32\WindowsPowerShell\v1.0\`, `C:\WINDOWS\System32\OpenSSH\`,
		`C:\Program Files\Git\cmd`, `C:\Users\u\AppData\Local\Microsoft\WindowsApps`,
	}, ";")
	for _, tc := range []struct{ in, want string }{
		{`Path=` + w + `;relative\bin;;\nodrive;C:x;C:\a"b;` + seven, `Path=` + w},
		{`Path=` + w + `;C:\abs1;C:\a"b;` + seven, `Path=` + w + `;C:\abs1`},
		{`Path=` + w + `;"C:\x;y";` + seven, `Path=` + w + `;` + seven},
		{`Path=` + w + `;"C:\p q";` + seven, `Path=` + w + `;C:\p q;` + seven},
		{`Path=` + w + `;C:\a"b"c;` + seven, `Path=` + w + `;C:\abc;` + seven},
		{`Path=` + w + `;` + seven, `Path=` + w + `;` + seven},
		{`pAtH=` + w + `;rel;` + seven, `pAtH=` + w + `;` + seven},
		{`GIT_EXEC_PATH=rel\exec`, ""},
		{`DYLD_LIBRARY_PATH=C:\abs;rel`, `DYLD_LIBRARY_PATH=C:\abs`},
		{"LD_PRELOAD=./x.so libok.dll", "LD_PRELOAD=libok.dll"},
		{"LD_PRELOAD=/abs/a.so:libb.so c/d.so libe.so", "LD_PRELOAD=libb.so:libe.so"},
		{`LD_AUDIT=C:\abs\a.dll`, `LD_AUDIT=C:\abs\a.dll`},
		{"LD_AUDIT=libx.so liby.so:/abs/z.so", "LD_AUDIT=libx.so liby.so"},
		{"GIT_SSH=/abs/ssh", ""},
		{`GIT_SSH=C:\abs\ssh.exe`, `GIT_SSH=C:\abs\ssh.exe`},
		{"GIT_SSH=ssh", "GIT_SSH=ssh"},
		{"GIT_SSH=", "GIT_SSH="},
		{`GIT_SSH=bin\ssh`, ""},
		{`MARKER_KEEP=rel;.\x`, `MARKER_KEEP=rel;.\x`},
	} {
		if got := cleanOne(tc.in, true); got != tc.want {
			t.Errorf("Windows: %q gives %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every name keeps its place, and a name that goes leaves the others in order.
func TestCleanGitChildEnvKeepsOrder(t *testing.T) {
	in := []string{"A=1", "PATH=rel:/usr/bin", "GIT_SSH=bin/ssh", "B=2", "LD_AUDIT=/a.so", "C=3"}
	want := []string{"A=1", "PATH=/usr/bin", "B=2", "LD_AUDIT=/a.so", "C=3"}
	if got := cleanGitChildEnv(in, false); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The env builders of class H calls clean the daemon's environment, and the
// builders of the excludes read and the version calls do not. The names here
// are removed on every system and start no child.
func TestGitChildEnvReachesTheBuilders(t *testing.T) {
	t.Setenv("GIT_EXEC_PATH", "rel/exec")
	t.Setenv("DYLD_FRAMEWORK_PATH", "rel")
	t.Setenv("GIT_SSH", "bin/ssh")
	t.Setenv("CLAUSTRUM_MARKER_KEEP", "rel:./x")
	gone := []string{"GIT_EXEC_PATH", "DYLD_FRAMEWORK_PATH", "GIT_SSH"}
	for _, b := range []struct {
		name string
		env  []string
	}{
		{"hardenedGitEnv", hardenedGitEnv(true, nil, nil)},
		{"gitUse.hardenedGitEnv", gitUse{}.hardenedGitEnv(false, nil, nil)},
		{"branchStepEnv", branchStepEnv(nil, nil)},
		{"precursorEnv", precursorEnv(false, nil)},
		{"gitUse.precursorEnv", gitUse{}.precursorEnv(true, nil)},
		{"repoConfigListEnv", repoConfigListEnv()},
		{"probeEnv", probeEnv(t.TempDir(), filepath.Join(t.TempDir(), "repo"))},
		{"shadowTestEnv", shadowTestEnv()},
	} {
		for _, n := range gone {
			if v, ok := lookupExact(b.env, n); ok {
				t.Errorf("%s keeps %s=%q", b.name, n, v)
			}
		}
		if _, ok := lookupExact(b.env, "CLAUSTRUM_MARKER_KEEP"); !ok {
			t.Errorf("%s lost CLAUSTRUM_MARKER_KEEP", b.name)
		}
	}
	if v, _ := lookupExact(shadowDaemonEnv(), "GIT_EXEC_PATH"); v != "rel/exec" {
		t.Errorf("the plain version calls lost GIT_EXEC_PATH: %q", v)
	}
}

// The calls of the start test get no GIT_ name of the daemon. A class H call
// keeps them. The names that the test sets stay.
func TestShadowTestEnvHasNoDaemonGitNames(t *testing.T) {
	t.Setenv("GIT_FOO", "x")
	t.Setenv("GIT_SSH_COMMAND", "rel/ssh")
	t.Setenv("GIT_TRACE2_EVENT_NESTING", "2")
	t.Setenv("GIT_SSH", "ssh")
	env := shadowTestEnv()
	for _, n := range []string{"GIT_FOO", "GIT_SSH_COMMAND", "GIT_TRACE2_EVENT_NESTING", "GIT_SSH"} {
		if v, ok := lookupExact(env, n); ok {
			t.Errorf("a start test call keeps %s=%q", n, v)
		}
		if _, ok := lookupExact(hardenedGitEnv(true, nil, nil), n); !ok {
			t.Errorf("a class H call lost %s", n)
		}
	}
	if v, _ := lookupExact(env, "GIT_CONFIG_GLOBAL"); v != "/dev/null" {
		t.Errorf("the start test lost its own GIT_CONFIG_GLOBAL: %q", v)
	}
}

// A hardened call reads its answer from stdout only. The stub writes a loader
// line to stderr and runs the real git. git.info then answers the branch of
// stdout, and isRepo sees "true". 5fd08069 answers "branch":"main" and creates
// the worktree there (round task004-01, cells M1 and M4, Linux VM).
func TestHardenedAnswerIsStdoutOnly(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	runGit(t, filepath.Dir(repo), "init", "-q", "-b", "main", repo)
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	const line = `ERROR: ld.so: object 'libdoesnotexist.so' from LD_PRELOAD cannot be preloaded (cannot open shared object file): ignored.\n`

	slowGit(t, "branch,--show-current", "pre", 0, line, "")
	s := newTestServer(t)
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": repo})); !strings.Contains(got, `"branch":"main"`) {
		t.Errorf("git.info = %s, want \"branch\":\"main\"", got)
	}

	slowGit(t, "rev-parse,--is-inside-work-tree", "pre", 0, line, "")
	if !isRepo(gitUse{}, repo, configListing{}) {
		t.Error("isRepo read stderr into the answer of rev-parse --is-inside-work-tree")
	}

	slowGit(t, "rev-parse,--absolute-git-dir", "pre", 0, line, "")
	dir, answered, err := worktreeBaseGitDir(gitUse{}, repo)
	want, _ := os.Stat(filepath.Join(repo, ".git"))
	if got, serr := os.Stat(dir); err != nil || !answered || serr != nil || !os.SameFile(got, want) {
		t.Errorf("worktreeBaseGitDir = %q, %v, %v, want the folder %s", dir, answered, err, filepath.Join(repo, ".git"))
	}
}

// Every git call of the five git methods gets the cleaned environment, except the
// excludes read and the version calls, which keep the names as they are. The two
// names here are removed on every system and do no harm to git. git.status reads
// a config.worktree file of the entry, so its own read of that file runs too.
func TestGitMethodsCleanTheChildEnv(t *testing.T) {
	f := newListingFixture(t)
	t.Setenv("GIT_SSH", "bin/ssh")
	t.Setenv("DYLD_FRAMEWORK_PATH", "rel")
	cleaned, raw := 0, 0
	for _, m := range []struct {
		name   string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.top}},
		{"git.list_branches", map[string]any{"path": f.top}},
		{"git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
		{"git.status", map[string]any{"path": f.wt(), "baseRepo": f.top}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
	} {
		if m.name == "git.status" {
			writeFile(t, filepath.Join(f.top, ".git", "worktrees", "w1", "config.worktree"), "[core]\n\tsparseCheckout = false\n", 0o644)
		}
		reply, calls := f.call(t, m.name, m.params)
		if strings.Contains(reply, `"error"`) || strings.Contains(reply, `"success":false`) {
			t.Fatalf("%s = %s", m.name, reply)
		}
		for _, c := range calls {
			_, ssh := lookupExact(c.env, "GIT_SSH")
			_, dyld := lookupExact(c.env, "DYLD_FRAMEWORK_PATH")
			keeps := c.excludesRead() || c.argv[len(c.argv)-1] == "version"
			switch {
			case keeps && (!ssh || !dyld):
				t.Errorf("%s: call %q lost a name of the daemon", m.name, c.argv)
			case !keeps && (ssh || dyld):
				t.Errorf("%s: call %q keeps GIT_SSH or DYLD_FRAMEWORK_PATH", m.name, c.argv)
			case keeps:
				raw++
			default:
				cleaned++
			}
		}
	}
	if cleaned == 0 || raw == 0 {
		t.Fatalf("cleaned %d calls and kept %d: the stub saw too little", cleaned, raw)
	}
}

// With "shadowDirOff":true the config.worktree read of git.status has the form of
// the read of a config file: the working directory gitVersionDir, GIT_DIR at the
// null device, LC_ALL=C and LANGUAGE=C. 5fd08069 runs it so (cell V3 of round
// task004-03, Linux, macOS and Windows VMs).
func TestStatusEntryConfigReadWithMember(t *testing.T) {
	f := newListingFixture(t)
	if _, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}); len(calls) == 0 {
		t.Fatal("the create made no git call")
	}
	writeFile(t, filepath.Join(f.top, ".git", "worktrees", "w1", "config.worktree"), "[core]\n\tsparseCheckout = false\n", 0o644)
	reply, calls := f.call(t, "git.status", map[string]any{"path": f.wt(), "baseRepo": f.top, "shadowDirOff": true})
	if strings.Contains(reply, `"error"`) {
		t.Fatalf("git.status = %s", reply)
	}
	wantDir := gitVersionDir
	if wantDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		wantDir = wd
	}
	seen := false
	for _, c := range calls {
		if !c.configRead() {
			continue
		}
		seen = true
		for name, want := range map[string]string{"GIT_DIR": os.DevNull, "LC_ALL": "C", "LANGUAGE": "C"} {
			if v, ok := lookupExact(c.env, name); !ok || v != want {
				t.Errorf("the config.worktree read has %s=%q (set %v), want %q", name, v, ok, want)
			}
		}
		if a, _ := filepath.EvalSymlinks(c.cwd); a != wantDir {
			if b, _ := filepath.EvalSymlinks(wantDir); a != b {
				t.Errorf("the config.worktree read runs in %q, want %q", c.cwd, wantDir)
			}
		}
	}
	if !seen {
		t.Fatal("git.status made no config.worktree read")
	}
}
