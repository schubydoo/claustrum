package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPaneShapeRefusal pins the shape test of a gitRead spawn: each argv of
// paneShapes passes, and any other argv gets the text with its count of args
// (c9125eb8, measured on Linux, macOS and Windows).
func TestPaneShapeRefusal(t *testing.T) {
	for _, s := range paneShapes {
		if got := PaneShapeRefusal(s.sent); got != "" {
			t.Errorf("PaneShapeRefusal(%q) = %q, want none", s.sent, got)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "not one of the Changes pane's git commands (0 arguments)"},
		{[]string{"status"}, "not one of the Changes pane's git commands (1 arguments)"},
		{[]string{"ls-files", "--others", "--exclude-standard"}, "not one of the Changes pane's git commands (3 arguments)"},
		{[]string{"-c", "core.pager=cat", "status", "--porcelain=v2"}, "not one of the Changes pane's git commands (4 arguments)"},
		{[]string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all"}, "not one of the Changes pane's git commands (5 arguments)"},
	} {
		if got := PaneShapeRefusal(c.args); got != c.want {
			t.Errorf("PaneShapeRefusal(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

// TestPaneShapesList pins the allowed argv by their literal text, so a typo in the
// table is not read back by a test that walks the table (c9125eb8, measured on
// Linux, macOS and Windows).
func TestPaneShapesList(t *testing.T) {
	want := []string{
		"status --porcelain=v2 --branch -z",
		"rev-parse --show-toplevel",
		"rev-parse --abbrev-ref HEAD",
		"rev-parse --is-shallow-repository",
		"rev-parse --verify HEAD",
		"symbolic-ref --short HEAD",
		"ls-files --others --exclude-standard --full-name :/",
		"show --no-textconv --no-ext-diff HEAD:README --",
		"merge-base HEAD HEAD",
		"rev-list --left-right --count HEAD...HEAD",
		"diff --name-status -M HEAD",
	}
	var got []string
	for _, s := range paneShapes {
		got = append(got, strings.Join(s.sent, " "))
	}
	if !slices.Equal(got, want) {
		t.Errorf("paneShapes = %q, want %q", got, want)
	}
}

// paneItems is the argv of the `-c` items of a run, with --no-optional-locks
// after the first part when locks is true.
func paneItems(locks bool) []string {
	var out []string
	for _, c := range paneHardenFirst {
		out = append(out, "-c", c)
	}
	if locks {
		out = append(out, "--no-optional-locks")
	}
	for _, c := range paneHardenLast {
		out = append(out, "-c", c)
	}
	return out
}

// TestPaneArgv pins the argv of the pane's git: 37 `-c` items, then the run argv
// of the call logs (measured on Linux, and the status row on Windows).
func TestPaneArgv(t *testing.T) {
	if n := len(paneHardenFirst) + len(paneHardenLast); n != 37 {
		t.Errorf("the pane's git has %d -c items, want 37", n)
	}
	if paneHardenFirst[0] != "core.fsmonitor=" || paneHardenFirst[1] != "core.hooksPath="+hooksNullDevice ||
		paneHardenLast[len(paneHardenLast)-1] != "advice.graftFileDeprecated=false" {
		t.Errorf("the ends of the -c items = %q, %q", paneHardenFirst[:2], paneHardenLast[len(paneHardenLast)-1])
	}
	for _, c := range []struct {
		sent, run []string
		locks     bool
	}{
		{[]string{"status", "--porcelain=v2", "--branch", "-z"},
			[]string{"status", "--ignore-submodules=dirty", "--porcelain=v2", "--branch", "-z"}, true},
		{[]string{"rev-parse", "--show-toplevel"}, []string{"rev-parse", "--show-toplevel"}, false},
		{[]string{"diff", "--name-status", "-M", "HEAD"},
			[]string{"diff-index", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
				"--name-status", "-M", "HEAD", "--"}, false},
		{[]string{"rev-list", "--left-right", "--count", "HEAD...HEAD"},
			[]string{"rev-list", "--left-right", "--count", "HEAD...HEAD", "--"}, false},
		{[]string{"show", "--no-textconv", "--no-ext-diff", "HEAD:README", "--"},
			[]string{"show", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
				"--no-textconv", "--no-ext-diff", "HEAD:README", "--"}, false},
	} {
		want := append(paneItems(c.locks), c.run...)
		if got := paneArgv(paneShapeOf(c.sent), nil); !slices.Equal(got, want) {
			t.Errorf("paneArgv(%q) =\n%q\nwant\n%q", c.sent, got, want)
		}
	}
}

// TestPaneGitRunShadow runs the steps before the pane's git in a repository in
// the state shadow. The run names the copy in GIT_COMMON_DIR, and the index of
// rev-parse is <copy>/no-index. The env ends with GIT_OPTIONAL_LOCKS=0, an empty
// SSH_AUTH_SOCK and that index (measured on Linux and macOS). The last call before
// the run is the listing in the cwd (measured on Linux). The status shape keeps
// the real index (measured on Windows).
func TestPaneGitRunShadow(t *testing.T) {
	f := newShadowFixture(t)
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := PaneGitRun(f.d, f.repo, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		t.Fatalf("PaneGitRun: %v", err)
	}
	run.Release()
	calls := readEnvCalls(t, f.log)
	if len(calls) == 0 || !calls[len(calls)-1].listing() || calls[len(calls)-1].cwd != f.repo {
		t.Fatalf("the last call before the run = %+v, want the listing in %s", calls[len(calls)-1:], f.repo)
	}
	env := run.Env(append(os.Environ(), "SSH_AUTH_SOCK=/agent", "GIT_OPTIONAL_LOCKS=1"))
	common, _ := envValue(env, "GIT_COMMON_DIR")
	if filepath.Dir(filepath.Dir(common)) != filepath.Join(f.run, "git-shadow") || !isShadowRepoName(filepath.Base(common)) {
		t.Errorf("GIT_COMMON_DIR = %q, want a copy below %s", common, filepath.Join(f.run, "git-shadow"))
	}
	if d, _ := envValue(env, "GIT_DIR"); d != f.gitDir {
		t.Errorf("GIT_DIR = %q, want %q", d, f.gitDir)
	}
	tail := []string{"GIT_OPTIONAL_LOCKS=0", "SSH_AUTH_SOCK=", "GIT_INDEX_FILE=" + filepath.Join(common, "no-index")}
	if len(env) < 3 || !slices.Equal(env[len(env)-3:], tail) {
		t.Errorf("env ends with %q, want %q", env[max(0, len(env)-3):], tail)
	}
	for _, name := range []string{"SSH_AUTH_SOCK", "GIT_OPTIONAL_LOCKS"} {
		n := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, name+"=") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("env holds %d entries of %s, want 1", n, name)
		}
	}
	if v, _ := envValue(env, "GIT_CONFIG_KEY_0"); v != "hook.enabled" {
		t.Errorf("GIT_CONFIG_KEY_0 = %q, want hook.enabled", v)
	}
	if !slices.Equal(run.Args, paneArgv(paneShapeOf([]string{"rev-parse", "--show-toplevel"}), nil)) {
		t.Errorf("Args = %q", run.Args)
	}
	cmd := gitCommand(run.Args...)
	cmd.Dir, cmd.Env = f.repo, env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the pane's git: %v", err)
	}
	if got := filepath.Clean(strings.TrimSpace(string(out))); !sameGitDirPath(got, f.repo) {
		t.Errorf("rev-parse --show-toplevel = %q, want %q", got, f.repo)
	}

	status, err := PaneGitRun(f.d, f.repo, []string{"status", "--porcelain=v2", "--branch", "-z"})
	if err != nil {
		t.Fatalf("PaneGitRun status: %v", err)
	}
	status.Release()
	if _, ok := envValue(status.Env(append(os.Environ(), "GIT_INDEX_FILE=/spawn-index")), "GIT_INDEX_FILE"); ok {
		t.Errorf("the status run has GIT_INDEX_FILE, want the real index")
	}
}

// TestPaneGitRunNoRepo runs the steps in a folder with no repository. The run
// carries GIT_DIR=<null device> and no index, as c9125eb8 does (measured on Linux).
func TestPaneGitRunNoRepo(t *testing.T) {
	f := newShadowFixture(t)
	dir := resolveTestRoot(t, t.TempDir())
	run, err := PaneGitRun(f.d, dir, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		t.Fatalf("PaneGitRun: %v", err)
	}
	run.Release()
	env := run.Env(os.Environ())
	if v, _ := envValue(env, "GIT_DIR"); v != os.DevNull {
		t.Errorf("GIT_DIR = %q, want %q", v, os.DevNull)
	}
	for _, name := range []string{"GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_WORK_TREE"} {
		if _, ok := envValue(env, name); ok {
			t.Errorf("env has %s in a folder with no repository", name)
		}
	}
}
