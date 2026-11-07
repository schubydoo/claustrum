package git

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPaneShapeRefusal pins the shape test of a gitRead spawn: an argv of
// paneShapes passes, and any other argv gets the text with its count of args
// (c9125eb8, measured on Linux, macOS and Windows).
func TestPaneShapeRefusal(t *testing.T) {
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
		"rev-parse --verify <ref>",
		"symbolic-ref --short HEAD",
		"ls-files --others --exclude-standard --full-name :/",
		"show --no-textconv --no-ext-diff <ref> --",
		"merge-base <ref> <ref>",
		"rev-list --left-right --count <rev>",
		"log --no-show-signature --format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e <rev>",
		"hash-object -t tree /dev/null",
		"diff <base>",
		"diff --numstat <base>",
		"diff -z --no-color --name-status -M <base>",
		"diff --inter-hunk-context=3 <base>",
		"diff --src-prefix=a/ --dst-prefix=b/ <base>",
		"diff --name-status -M <base>",
		"diff --name-status -M <base> -- <exclude>",
		"diff --name-status -M <ref> <ref>",
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
		// The slots give the sent refs and paths back in their order.
		{[]string{"rev-parse", "--verify", "main^{commit}"}, []string{"rev-parse", "--verify", "main^{commit}"}, false},
		{[]string{"merge-base", "main", "topic"}, []string{"merge-base", "main", "topic"}, false},
		{[]string{"rev-list", "--left-right", "--count", "main..topic"},
			[]string{"rev-list", "--left-right", "--count", "main..topic", "--"}, false},
		{[]string{"show", "--no-textconv", "--no-ext-diff", "main:sub/x.txt", "--"},
			[]string{"show", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
				"--no-textconv", "--no-ext-diff", "main:sub/x.txt", "--"}, false},
		{[]string{"diff", "--name-status", "-M", "origin/main"},
			[]string{"diff-index", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
				"--name-status", "-M", "origin/main", "--"}, false},
		// claustrum's own, not measured: the run of the two-ref diff.
		{[]string{"diff", "--name-status", "-M", "main", "topic"},
			[]string{"diff-tree", "-r", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
				"--name-status", "-M", "main", "topic", "--"}, false},
	} {
		want := append(paneItems(c.locks), c.run...)
		if got := paneArgv(paneShapeOf(c.sent), c.sent, nil); !slices.Equal(got, want) {
			t.Errorf("paneArgv(%q) =\n%q\nwant\n%q", c.sent, got, want)
		}
	}
}

// TestPaneArgvLogRows pins the run argv of the rows of the call logs of c9125eb8
// (measured on Linux and Windows, row 6 on macOS too): `diff-index` for one ref, `diff-tree -r`
// for a range of two dots, `-p` for a patch, and a trailing `--`. realIndex is true
// for a run with no GIT_INDEX_FILE in the log.
func TestPaneArgvLogRows(t *testing.T) {
	const format = "--format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e"
	opts := []string{"--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv"}
	cat := func(parts ...[]string) []string { return slices.Concat(parts...) }
	w := func(s ...string) []string { return s }
	for _, c := range []struct {
		sent, run []string
		realIndex bool
	}{
		{w("log", "--no-show-signature", format, "main..topic"), cat(w("log"), opts, w("--no-show-signature", format, "main..topic", "--")), false},
		{w("log", "--no-show-signature", format, "HEAD"), cat(w("log"), opts, w("--no-show-signature", format, "HEAD", "--")), false},
		{w("hash-object", "-t", "tree", "/dev/null"), w("hash-object", "-t", "tree", "/dev/null"), false},
		{w("show", "--no-textconv", "--no-ext-diff", "HEAD", "--"), cat(w("show"), opts, w("--no-textconv", "--no-ext-diff", "HEAD", "--")), false},
		{w("rev-list", "--left-right", "--count", "HEAD"), w("rev-list", "--left-right", "--count", "HEAD", "--"), false},
		{w("diff", "HEAD"), cat(w("diff-index", "-p"), opts, w("HEAD", "--")), true},
		{w("diff", "--numstat", "HEAD"), cat(w("diff-index"), opts, w("--numstat", "HEAD", "--")), true},
		{w("diff", "-z", "--no-color", "--name-status", "-M", "HEAD"), cat(w("diff-index"), opts, w("-z", "--no-color", "--name-status", "-M", "HEAD", "--")), true},
		{w("diff", "--name-status", "-M", "main..topic"), cat(w("diff-tree", "-r"), opts, w("--name-status", "-M", "main", "topic", "--")), false},
		{w("diff", "--inter-hunk-context=3", "HEAD"), cat(w("diff-index", "-p"), opts, w("--inter-hunk-context=3", "HEAD", "--")), true},
		{w("diff", "--name-status", "-M", "HEAD", "--", ":(top,exclude)sub"), cat(w("diff-index"), opts, w("--name-status", "-M", "HEAD", "--", ":(top,exclude)sub")), true},
		{w("diff", "--name-status", "-M", "HEAD", "--", ":(top,exclude,literal)sub"), cat(w("diff-index"), opts, w("--name-status", "-M", "HEAD", "--", ":(top,exclude,literal)sub")), true},
		{w("diff", "--src-prefix=a/", "--dst-prefix=b/", "HEAD"), cat(w("diff-index", "-p"), opts, w("--src-prefix=a/", "--dst-prefix=b/", "HEAD", "--")), true},
		{w("rev-parse", "--show-toplevel"), w("rev-parse", "--show-toplevel"), false},
	} {
		s := paneShapeOf(c.sent)
		if s == nil {
			t.Errorf("%q: refused, want a run", c.sent)
			continue
		}
		if got, want := paneArgv(s, c.sent, nil), append(paneItems(false), c.run...); !slices.Equal(got, want) {
			t.Errorf("paneArgv(%q) =\n%q\nwant\n%q", c.sent, got, want)
		}
		if _, _, real := s.runFor(c.sent); real != c.realIndex {
			t.Errorf("%q: realIndex = %v, want %v", c.sent, real, c.realIndex)
		}
	}
}

// TestPaneArgvClaustrumOwn pins the runs that no row shows (claustrum's own, not
// measured): a range in a patch diff runs as diff-tree -r -p.
func TestPaneArgvClaustrumOwn(t *testing.T) {
	for _, c := range []struct {
		sent, run []string
	}{
		{[]string{"diff", "main..topic"},
			[]string{"diff-tree", "-r", "-p", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv", "main", "topic", "--"}},
	} {
		want := append(paneItems(false), c.run...)
		if got := paneArgv(paneShapeOf(c.sent), c.sent, nil); !slices.Equal(got, want) {
			t.Errorf("paneArgv(%q) =\n%q\nwant\n%q", c.sent, got, want)
		}
	}
}

// TestPaneShapeRows walks the argv rows of c9125eb8 with variable refs and paths
// (measured on Linux). An allowed row passes the shape test, and a refused row gets
// the text with its count of args. The last rows are claustrum's own, not measured.
func TestPaneShapeRows(t *testing.T) {
	const sha = "e4ee5331c3b2fa10f838ded96064b55b6ae6f028"
	allowed := []string{
		"rev-parse --verify HEAD", "rev-parse --verify main", "rev-parse --verify topic",
		"rev-parse --verify origin/main", "rev-parse --verify v1", "rev-parse --verify " + sha,
		"rev-parse --verify e4ee533", "rev-parse --verify HEAD^", "rev-parse --verify HEAD~1",
		"rev-parse --verify main^{commit}", "rev-parse --verify " + sha + "^{commit}", "rev-parse --verify refs/heads/main",
		"merge-base HEAD main", "merge-base main topic", "merge-base origin/main HEAD", "merge-base " + sha + " " + sha,
		"rev-list --left-right --count main...topic", "rev-list --left-right --count origin/main...HEAD",
		"rev-list --left-right --count " + sha + "..." + sha, "rev-list --left-right --count main..topic",
		"diff --name-status -M HEAD", "diff --name-status -M main", "diff --name-status -M " + sha,
		"diff --name-status -M origin/main", "diff --name-status -M main topic",
		"show --no-textconv --no-ext-diff HEAD:README --", "show --no-textconv --no-ext-diff main:sub/x.txt --",
		"show --no-textconv --no-ext-diff " + sha + ":README --", "show --no-textconv --no-ext-diff HEAD:missing --",
		"show --no-textconv --no-ext-diff HEAD:../x --", "show --no-textconv --no-ext-diff HEAD:/etc/passwd --",
		"show --no-textconv --no-ext-diff :README --",
		"status --porcelain=v2 --branch -z",
		// The rows of c9125eb8 with the branches main and topic (measured on Linux,
		// macOS and Windows).
		"log --no-show-signature --format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e main..topic",
		"log --no-show-signature --format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e HEAD",
		"hash-object -t tree /dev/null", "show --no-textconv --no-ext-diff HEAD --",
		"rev-list --left-right --count HEAD", "diff HEAD", "diff --numstat HEAD",
		"diff -z --no-color --name-status -M HEAD", "diff --name-status -M main..topic",
		"diff --inter-hunk-context=3 HEAD", "diff --name-status -M HEAD -- :(top,exclude)sub",
		"diff --name-status -M HEAD -- :(top,exclude,literal)sub", "diff --src-prefix=a/ --dst-prefix=b/ HEAD",
		"rev-list --left-right --count main",
	}
	refused := []string{
		"rev-parse --verify --output=/tmp/r", "rev-parse --verify -q",
		"merge-base --all main", "merge-base main --octopus",
		"rev-list --left-right --count main...topic --",
		"diff --name-status -M HEAD -- sub", "diff --name-status -M HEAD -- .",
		"diff --name-status -M HEAD --output=/tmp/r", "diff --name-status -M50% HEAD",
		"show --no-textconv --no-ext-diff HEAD:README", "show --no-textconv --no-ext-diff HEAD:README HEAD:sub/x.txt --",
		"show --no-textconv --no-ext-diff HEAD:README -- sub/x.txt",
		"log --no-show-signature --format=%H main..topic", "log --no-show-signature --format=%H%x00%s main..topic",
		"log --no-show-signature --format=%an origin/main..HEAD",
		"log --no-show-signature --format=%H " + sha + ".." + sha, "log --no-show-signature --format=%H HEAD..HEAD -n 5",
		"log --no-show-signature --format=%H main...topic",
		"ls-files -v -z --full-name :/", "ls-files -v -z --full-name -c :/", "ls-files -v -z --full-name -m :/",
		"ls-files -v -z --full-name -d -m :/", "ls-files -v -z --full-name --exclude-standard -o :/",
		"ls-files -z --full-name :/",
		"status --porcelain=v2 --branch -z --untracked-files=no", "status --porcelain=v2 --branch -z --ignored",
		"status --porcelain=v2 --branch -z -uall", "status --porcelain=v2 --branch -z --no-renames",
		"status", "log -1", "commit --allow-empty -m x -q --no-verify",
		"status --porcelain=v2 --branch -z --ignored -uall --no-renames",
		// The refused rows of c9125eb8 with the branches main and topic (measured on
		// Linux, macOS and Windows).
		"diff --name-status -M main...topic", "diff --inter-hunk-context=10 HEAD",
		"diff --name-status HEAD main topic",
		// claustrum's own, not measured.
		"rev-list --left-right --count ..topic", "rev-list --left-right --count main...-x",
		"diff --name-status -M main..", "diff --name-status -M a..b..c",
		"diff --name-status -M HEAD -- :(top,exclude)", "diff --name-status -M HEAD -- :(exclude)sub",
	}
	for _, row := range allowed {
		if got := PaneShapeRefusal(strings.Fields(row)); got != "" {
			t.Errorf("PaneShapeRefusal(%q) = %q, want none", row, got)
		}
	}
	for _, row := range refused {
		args := strings.Fields(row)
		want := fmt.Sprintf("not one of the Changes pane's git commands (%d arguments)", len(args))
		if got := PaneShapeRefusal(args); got != want {
			t.Errorf("PaneShapeRefusal(%q) = %q, want %q", row, got, want)
		}
	}
	// Two rows hold a value that strings.Fields cannot carry: an empty ref is
	// refused, and a path with a space runs.
	if got := PaneShapeRefusal([]string{"rev-parse", "--verify", ""}); got != "not one of the Changes pane's git commands (3 arguments)" {
		t.Errorf("an empty ref: %q, want the refusal", got)
	}
	if got := PaneShapeRefusal([]string{"show", "--no-textconv", "--no-ext-diff", "HEAD:a b.txt", "--"}); got != "" {
		t.Errorf("a path with a space: %q, want none", got)
	}
}

// TestPaneRefBytes pins the ref test of c9125eb8: a ref with a TAB, LF, DEL or
// SOH byte and a ref of 4097 bytes get the shape refusal with 3 arguments, and a
// ref of 4096 bytes runs (measured on Linux, and on macOS and Windows but SOH). The other
// control bytes are claustrum's own, not measured.
func TestPaneRefBytes(t *testing.T) {
	refusal := "not one of the Changes pane's git commands (3 arguments)"
	for _, c := range []struct {
		ref  string
		want string
	}{
		{"HEAD", ""},
		{"HEAD^", ""},
		{strings.Repeat("a", 4096), ""},
		{"HEAD\tx", refusal},
		{"a\nb", refusal},
		{"x\x7f", refusal},
		{"HEAD\x01", refusal},
		{strings.Repeat("a", 4097), refusal},
		{"HEAD\x00", refusal},
		{"HEAD\x1f", refusal},
	} {
		if got := PaneShapeRefusal([]string{"rev-parse", "--verify", c.ref}); got != c.want {
			t.Errorf("rev-parse --verify %q (%d bytes) = %q, want %q", c.ref, len(c.ref), got, c.want)
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
	// The run holds its copy until Release: the eviction skips a busy folder.
	common0, _ := envValue(run.Env(os.Environ()), "GIT_COMMON_DIR")
	busy := func() int {
		f.d.Shadow.repoMu.Lock()
		defer f.d.Shadow.repoMu.Unlock()
		return f.d.Shadow.repoBusy[filepath.Base(common0)]
	}
	if n := busy(); n != 1 {
		t.Errorf("holds of the copy before Release = %d, want 1", n)
	}
	run.Release()
	if n := busy(); n != 0 {
		t.Errorf("holds of the copy after Release = %d, want 0", n)
	}
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
	if top := []string{"rev-parse", "--show-toplevel"}; !slices.Equal(run.Args, paneArgv(paneShapeOf(top), top, nil)) {
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
