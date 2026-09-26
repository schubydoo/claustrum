package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// shapeCall is one git call as the CTXLOG of the "git-slow" stub records it.
type shapeCall struct {
	cwd  string
	argv []string
	env  []string // the GIT_* entries that the daemon adds, in environment order
	all  []string // every GIT_* entry, in environment order
}

// daemonGitKeys are the GIT_* variables the daemon sets on a git call. The test's
// own GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM are not among them.
var daemonGitKeys = []string{
	"GIT_ALLOW_PROTOCOL", "GIT_TERMINAL_PROMPT", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE",
	"GIT_NO_LAZY_FETCH", "GIT_ASKPASS", "GIT_COMMON_DIR", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
	"GIT_INDEX_FILE", "GIT_OPTIONAL_LOCKS", "GIT_DIR",
}

func readShapeCalls(t *testing.T, log string) []shapeCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls []shapeCall
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		parts := strings.Split(line, "\x1e")
		if len(parts) != 5 {
			t.Fatalf("CTXLOG line %q has %d fields, want 5", line, len(parts))
		}
		c := shapeCall{cwd: parts[0], argv: strings.Split(parts[2], "\x1f")}
		c.all = strings.Split(parts[4], "\x1f")
		for _, kv := range c.all {
			if slices.Contains(daemonGitKeys, strings.SplitN(kv, "=", 2)[0]) {
				c.env = append(c.env, kv)
			}
		}
		calls = append(calls, c)
	}
	return calls
}

func (c shapeCall) listing() bool {
	return argvEndsWith(c.argv, []string{"config", "-z", "--list", "--name-only"})
}

func (c shapeCall) hardened() bool {
	return slices.Contains(c.argv, "core.hooksPath=/dev/null")
}

func (c shapeCall) heavy() bool {
	return slices.Contains(c.argv, "core.attributesFile=/dev/null")
}

func (c shapeCall) has(words ...string) bool {
	for _, w := range words {
		if !slices.Contains(c.argv, w) {
			return false
		}
	}
	return true
}

// keys returns the variable names of c.env, with GIT_COMMON_DIR's value checked
// apart: it is a path that the OS can spell another way.
func (c shapeCall) keysAndValues() []string {
	var out []string
	for _, kv := range c.env {
		if strings.HasPrefix(kv, "GIT_COMMON_DIR=") {
			out = append(out, "GIT_COMMON_DIR")
			continue
		}
		out = append(out, kv)
	}
	return out
}

// The profile variables, written out here, not taken from profileEnv. The null
// device is NUL on Windows, as f6010b97 passes it on a Windows VM.
var (
	shapeNull  = map[bool]string{false: "/dev/null", true: "NUL"}[runtime.GOOS == "windows"]
	shapeLight = []string{"GIT_ALLOW_PROTOCOL=https:ssh", "GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + shapeNull}
	shapeHeavy = []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
		"GIT_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}
	shapePins = []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=hook.enabled",
		"GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_KEY_1=hook.event", "GIT_CONFIG_VALUE_1="}
)

// sameShapeEnv compares a call's env with the wanted one. On Windows Go's os/exec
// sorts the environment block, so there the two are compared as sets. The order is
// checked on Linux and macOS.
func sameShapeEnv(got, want []string) bool {
	if runtime.GOOS == "windows" {
		got, want = slices.Clone(got), slices.Clone(want)
		slices.Sort(got)
		slices.Sort(want)
	}
	return slices.Equal(got, want)
}

// wantShapeEnv is the daemon's GIT_* set, in order, for a listing (pins false) or a
// hardened call (pins true), with extra appended.
func wantShapeEnv(heavy, pins bool, extra ...string) []string {
	want := slices.Clone(shapeLight)
	if heavy {
		want = slices.Clone(shapeHeavy)
	}
	want = append(want, "GIT_COMMON_DIR")
	if pins {
		want = append(want, shapePins...)
	}
	return append(want, extra...)
}

// checkAlternation checks that every hardened call follows exactly one listing, and
// that each listing carries the profile of the call after it and no hook pins. Each
// call runs in the directory that cwdOf names, with no -C.
func checkAlternation(t *testing.T, what string, calls []shapeCall, cwdOf func(shapeCall) string) {
	t.Helper()
	for i, c := range calls {
		if !c.listing() && !c.hardened() {
			continue
		}
		if slices.Contains(c.argv, "-C") {
			t.Errorf("%s call %d %q passes -C", what, i, c.argv)
		}
		if want := cwdOf(c); canonicalPath(c.cwd) != canonicalPath(want) {
			t.Errorf("%s call %d %q runs in %s, want %s", what, i, c.argv, c.cwd, want)
		}
		if c.listing() {
			if i+1 >= len(calls) || !calls[i+1].hardened() {
				t.Errorf("%s call %d is a listing not followed by a hardened call: %q", what, i, c.argv)
				continue
			}
			if got, want := c.keysAndValues(), wantShapeEnv(calls[i+1].heavy(), false); !sameShapeEnv(got, want) {
				t.Errorf("%s listing %d env = %q\nwant %q (the profile of %q)", what, i, got, want, calls[i+1].argv)
			}
			continue
		}
		if i == 0 || !calls[i-1].listing() {
			t.Errorf("%s hardened call %d %q has no listing right before it", what, i, c.argv)
		}
	}
}

// checkHardenedEnv checks the env of every hardened call: its profile, the pin,
// the hook pins, then the one extra variable of a read-tree or a status call.
func checkHardenedEnv(t *testing.T, what string, calls []shapeCall) {
	t.Helper()
	for i, c := range calls {
		if !c.hardened() {
			continue
		}
		var extra []string
		for _, kv := range c.env {
			if strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
				extra = append(extra, kv)
			}
		}
		// The status call spells the null device /dev/null on every OS (D16).
		excludes := shapeNull
		if c.has("status") {
			extra = append(extra, "GIT_OPTIONAL_LOCKS=0")
			excludes = "/dev/null"
		}
		if got, want := c.keysAndValues(), wantShapeEnv(c.heavy(), true, extra...); !sameShapeEnv(got, want) {
			t.Errorf("%s call %d %q env = %q\nwant %q", what, i, c.argv, got, want)
		}
		if !slices.Contains(c.argv, "core.excludesFile="+excludes) {
			t.Errorf("%s call %d %q, want -c core.excludesFile=%s", what, i, c.argv, excludes)
		}
	}
}

// TestHardenedGitCallShape pins the cwd, the argv and the env of the git calls that
// the hardened helpers make, and their order, as f6010b97 makes them on Linux, macOS
// and Windows VMs:
//
//   - Every hardened call and every config listing runs in the repository directory
//     and passes no -C.
//   - One listing runs right before each hardened call. In git.info,
//     git.list_branches, git.worktree_create and git.worktree_remove, the hooks
//     refusal check is the listing of the first call, not an extra one.
//   - A listing carries the profile variables of the call after it, and no hook pins.
//   - The light profile sets GIT_NO_REPLACE_OBJECTS and GIT_GRAFT_FILE=<null
//     device>. The heavy profile, used by `rev-parse --absolute-git-dir` too, does not.
//     GIT_OPTIONAL_LOCKS=0 is on the status call only.
//   - The excludes read runs first, in the temporary directory, with GIT_DIR=<null
//     device> as its only variable. The --attr-source probe adds no variable.
//   - The status call starts with --attr-source and runs in the worktree.
func TestHardenedGitCallShape(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	f := newWTFixture(t, false)
	s := newTestServer(t)

	// No global excludes anywhere, so the resolver answers the null device.
	empty := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	t.Setenv("USERPROFILE", empty)
	// go test hands the test binary GIT_TERMINAL_PROMPT=0. Take it out, so the env
	// the daemon adds is all that the stub sees.
	t.Setenv("GIT_TERMINAL_PROMPT", "")
	if err := os.Unsetenv("GIT_TERMINAL_PROMPT"); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	resetAttr := func() {
		attrSourceOnce = sync.Once{}
		attrSourceOK = false
	}
	resetAttr()
	t.Cleanup(resetAttr)

	ctxlog := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
	run := func(method string, params map[string]any) (string, []shapeCall) {
		t.Helper()
		if err := os.WriteFile(ctxlog, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		raw := dispatchRaw(t, s, rpcLine(t, method, params))
		return raw, readShapeCalls(t, ctxlog)
	}
	inTop := func(shapeCall) string { return f.top }

	t.Run("info", func(t *testing.T) {
		raw, calls := run("git.info", map[string]any{"path": f.top})
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("reply = %s", raw)
		}
		x := calls[0]
		if !slices.Equal(x.argv, []string{"config", "--includes", "--path", "core.excludesFile"}) {
			t.Fatalf("first call = %q, want the excludes read", x.argv)
		}
		if canonicalPath(x.cwd) != canonicalPath(os.TempDir()) {
			t.Errorf("excludes read runs in %s, want %s", x.cwd, os.TempDir())
		}
		if !slices.Equal(x.env, []string{"GIT_DIR=" + shapeNull}) {
			t.Errorf("excludes read env = %q, want only GIT_DIR=%s", x.env, shapeNull)
		}
		calls = calls[1:]
		if len(calls) < 2 || !calls[0].listing() || !calls[1].has("rev-parse", "--git-dir") {
			t.Fatalf("calls = %q, want one listing, then rev-parse --git-dir", calls)
		}
		checkAlternation(t, "info", calls, inTop)
		checkHardenedEnv(t, "info", calls)
	})

	t.Run("list_branches", func(t *testing.T) {
		raw, calls := run("git.list_branches", map[string]any{"path": f.top})
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("reply = %s", raw)
		}
		if len(calls) != 4 || !calls[1].has("rev-parse", "--git-dir") || !calls[3].has("for-each-ref") {
			t.Fatalf("calls = %q, want listing, rev-parse --git-dir, listing, for-each-ref", calls)
		}
		checkAlternation(t, "list_branches", calls, inTop)
		checkHardenedEnv(t, "list_branches", calls)
	})

	t.Run("worktree_create", func(t *testing.T) {
		raw, calls := run("git.worktree_create", map[string]any{
			"baseRepo": f.top, "branchName": "w1", "worktreePath": f.leaf()})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("reply = %s", raw)
		}
		if len(calls) < 2 || !calls[0].listing() || !calls[1].has("rev-parse", "--is-inside-work-tree") {
			t.Fatalf("calls = %q, want one listing, then rev-parse --is-inside-work-tree", calls)
		}
		checkAlternation(t, "worktree_create", calls, func(c shapeCall) string {
			if c.has("read-tree") || (c.listing() && strings.HasPrefix(c.argv[0], "--git-dir=")) {
				return f.leaf()
			}
			return f.top
		})
		checkHardenedEnv(t, "worktree_create", calls)
		found := false
		for _, c := range calls {
			if c.has("rev-parse", "--absolute-git-dir") {
				found = true
				if !c.heavy() {
					t.Errorf("rev-parse --absolute-git-dir %q, want the heavy profile", c.argv)
				}
			}
		}
		if !found {
			t.Errorf("calls = %q, want a rev-parse --absolute-git-dir", calls)
		}
	})

	t.Run("status", func(t *testing.T) {
		raw, calls := run("git.status", map[string]any{"baseRepo": f.top, "path": f.leaf()})
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("reply = %s", raw)
		}
		if len(calls) < 3 {
			t.Fatalf("calls = %q", calls)
		}
		first := calls[0]
		if !first.listing() || canonicalPath(first.cwd) != canonicalPath(f.top) || slices.Contains(first.argv, "-C") {
			t.Errorf("first call = %q in %s, want the listing in baseRepo with no -C", first.argv, first.cwd)
		}
		if got, want := first.keysAndValues(), wantShapeEnv(true, false); !sameShapeEnv(got, want) {
			t.Errorf("first listing env = %q, want %q", got, want)
		}
		probed := false
		for _, c := range calls {
			if c.argv[len(c.argv)-1] == "version" {
				probed = true
				if len(c.env) != 0 {
					t.Errorf("--attr-source probe env = %q, want no added variable", c.env)
				}
			}
		}
		if !probed {
			t.Errorf("calls = %q, want the --attr-source probe", calls)
		}
		pre, st := calls[len(calls)-2], calls[len(calls)-1]
		if !st.has("status") {
			t.Fatalf("last call = %q, want status", st.argv)
		}
		if !pre.listing() || !strings.HasPrefix(pre.argv[0], "--git-dir=") || pre.argv[0] != st.argv[slices.IndexFunc(st.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })] {
			t.Errorf("status precursor = %q, want --git-dir=<the status call's temp git dir> config -z --list --name-only", pre.argv)
		}
		checkAlternation(t, "status", calls[len(calls)-2:], func(shapeCall) string { return f.leaf() })
		checkHardenedEnv(t, "status", calls[len(calls)-1:])
		wantFirst := "-c"
		if attrSourceOK {
			wantFirst = "--attr-source=" + gitEmptyTree
		}
		if st.argv[0] != wantFirst {
			t.Errorf("status argv starts %q, want %q", st.argv[0], wantFirst)
		}
		if gd := st.argv[slices.IndexFunc(st.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })]; !strings.HasPrefix(filepath.Base(gd), statusGitDirTempPrefix) {
			t.Errorf("status %s, want a temp git dir named %s*", gd, statusGitDirTempPrefix)
		}
		// The pin of the precursor and of the status call is a clean path. On Windows
		// git prints the common dir with forward slashes, and f6010b97 pins it with
		// backslashes.
		for _, c := range []shapeCall{pre, st} {
			i := slices.IndexFunc(c.env, func(kv string) bool { return strings.HasPrefix(kv, "GIT_COMMON_DIR=") })
			if i < 0 {
				t.Errorf("%q has no GIT_COMMON_DIR", c.argv)
				continue
			}
			v := strings.TrimPrefix(c.env[i], "GIT_COMMON_DIR=")
			if v != filepath.Clean(v) || canonicalPath(v) != canonicalPath(filepath.Join(f.top, ".git")) {
				t.Errorf("%q pins GIT_COMMON_DIR=%s, want the clean path of %s", c.argv, v, filepath.Join(f.top, ".git"))
			}
		}
		if i := slices.Index(st.argv, "status"); i < 2 || !strings.HasPrefix(st.argv[i-1], "--work-tree=") || !strings.HasPrefix(st.argv[i-2], "--git-dir=") {
			t.Errorf("status argv = %q, want --git-dir and --work-tree right before status", st.argv)
		}
	})

	t.Run("worktree_remove", func(t *testing.T) {
		raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.leaf()})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("reply = %s", raw)
		}
		if len(calls) < 2 || !calls[0].listing() || !calls[1].has("rev-parse", "--absolute-git-dir") || !calls[1].heavy() {
			t.Fatalf("calls = %q, want one listing, then the heavy rev-parse --absolute-git-dir", calls)
		}
		checkAlternation(t, "worktree_remove", calls, inTop)
		checkHardenedEnv(t, "worktree_remove", calls)
	})

	// With worktreeRoot, the refusal listing is light, and the heavy rev-parse
	// --absolute-git-dir runs its own heavy listing: the order of f6010b97's listings
	// on a Linux VM. Windows refuses a worktreeRoot before any git.
	t.Run("worktree_remove with worktreeRoot", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a worktreeRoot is refused on Windows before any git call")
		}
		root := t.TempDir()
		ext := filepath.Join(root, "proj", "e0")
		raw, _ := run("git.worktree_create", map[string]any{
			"baseRepo": f.top, "branchName": "e0", "worktreePath": ext, "worktreeRoot": root})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("create reply = %s", raw)
		}
		raw, calls := run("git.worktree_remove", map[string]any{
			"baseRepo": f.top, "worktreePath": ext, "worktreeRoot": root})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("reply = %s", raw)
		}
		// The hardened calls, in order: show-toplevel (light, right after the
		// refusal listing), absolute-git-dir (heavy), worktree list (light),
		// absolute-git-dir (heavy), then update-ref. Each but the first has its own
		// listing, and the first listing is the refusal check.
		checkAlternation(t, "worktree_remove with worktreeRoot", calls, inTop)
		wantTail := [][]string{
			{"rev-parse", "--show-toplevel"},
			{"rev-parse", "--absolute-git-dir"},
			{"worktree", "list", "--porcelain", "-z"},
			{"rev-parse", "--absolute-git-dir"},
		}
		var hard []shapeCall
		for _, c := range calls {
			if c.hardened() {
				hard = append(hard, c)
			}
		}
		if len(hard) < len(wantTail) {
			t.Fatalf("calls = %q, want at least the hardened calls %q", calls, wantTail)
		}
		for i, w := range wantTail {
			if !argvEndsWith(hard[i].argv, w) {
				t.Errorf("hardened call %d = %q, want it to end %q", i, hard[i].argv, w)
			}
			if wantHeavy := w[1] == "--absolute-git-dir"; hard[i].heavy() != wantHeavy {
				t.Errorf("hardened call %d %q heavy = %v, want %v", i, hard[i].argv, hard[i].heavy(), wantHeavy)
			}
		}
		checkHardenedEnv(t, "worktree_remove with worktreeRoot", calls)
	})

	// When the refusal check of an in-repo remove fails, the check runs once more
	// before the lock-check answer, as on f6010b97 (Linux and macOS VMs): a second
	// listing, and a second rev-parse when that listing passes.
	checkTwice := func(t *testing.T, g wtFixture, wantHardened bool) {
		t.Helper()
		raw, calls := run("git.worktree_remove", map[string]any{"baseRepo": g.top, "worktreePath": g.leaf()})
		if !strings.Contains(raw, "could not check whether") {
			t.Fatalf("reply = %s, want the lock-check refusal", raw)
		}
		unit := 1
		if wantHardened {
			unit = 2
		}
		if len(calls) != 2*unit {
			t.Fatalf("calls = %q, want the check twice (%d calls)", calls, 2*unit)
		}
		for i, c := range calls {
			isListing := i%unit == 0
			if isListing && (!c.listing() || !sameShapeEnv(c.keysAndValues(), wantShapeEnv(true, false))) {
				t.Errorf("call %d = %q %q, want the heavy listing", i, c.argv, c.env)
			}
			if !isListing && (!c.has("rev-parse", "--absolute-git-dir") || !c.heavy()) {
				t.Errorf("call %d = %q, want the heavy rev-parse --absolute-git-dir", i, c.argv)
			}
			if canonicalPath(c.cwd) != canonicalPath(g.top) {
				t.Errorf("call %d runs in %s, want %s", i, c.cwd, g.top)
			}
		}
	}
	t.Run("worktree_remove refused twice, bad config", func(t *testing.T) {
		g := newWTFixture(t, false)
		cfg := filepath.Join(g.top, ".git", "config")
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, cfg, string(b)+"[broken\n", 0o644)
		checkTwice(t, g, false)
	})
	t.Run("worktree_remove refused twice, no usable git dir", func(t *testing.T) {
		// .git/objects is a file, so git finds no repository (row H20). On Windows git
		// accepts that layout, as f6010b97 does in row H20 on a Windows VM.
		if runtime.GOOS == "windows" {
			t.Skip("git on Windows accepts a .git/objects file (row H20)")
		}
		g := newWTFixture(t, false)
		objects := filepath.Join(g.top, ".git", "objects")
		if err := os.RemoveAll(objects); err != nil {
			t.Fatal(err)
		}
		writeFile(t, objects, "", 0o644)
		checkTwice(t, g, true)
	})

	// A GIT_* variable of the daemon's own environment keeps its place, before the
	// variables that the daemon adds, as on f6010b97 (Linux and macOS VMs). On Windows
	// Go's os/exec sorts the environment block, and claustrum does not work around that.
	t.Run("daemon env first", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Go sorts the environment block on Windows")
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(empty, "ceiling"))
		_, calls := run("git.info", map[string]any{"path": f.top})
		n := 0
		for i, c := range calls {
			if !c.listing() && !c.hardened() {
				continue
			}
			n++
			ceil := slices.IndexFunc(c.all, func(kv string) bool { return strings.HasPrefix(kv, "GIT_CEILING_DIRECTORIES=") })
			added := slices.IndexFunc(c.all, func(kv string) bool {
				return slices.Contains(daemonGitKeys, strings.SplitN(kv, "=", 2)[0])
			})
			if ceil < 0 || added < 0 || ceil > added {
				t.Errorf("call %d %q env = %q, want GIT_CEILING_DIRECTORIES before every added variable", i, c.argv, c.all)
			}
		}
		if n == 0 {
			t.Fatalf("calls = %q, want listings and hardened calls", calls)
		}
	})
}

// TestGitTempPrefixLengths pins the lengths of the temp name prefixes. The
// f6010b97 prefixes are 18 bytes (the status git dir) and 17 bytes (the checkout
// index directory), measured on Linux, macOS and Windows VMs.
func TestGitTempPrefixLengths(t *testing.T) {
	if len(statusGitDirTempPrefix) != 18 || len(checkoutIndexTempPrefix) != 17 {
		t.Errorf("len(%q) = %d, len(%q) = %d, want 18 and 17 (f6010b97)",
			statusGitDirTempPrefix, len(statusGitDirTempPrefix), checkoutIndexTempPrefix, len(checkoutIndexTempPrefix))
	}
}

// TestStatusExcludesFile pins the core.excludesFile value of the status call. The
// null device is /dev/null on every OS: on Windows git status exits 128 when the
// value is NUL (D16). A real excludes file passes through.
func TestStatusExcludesFile(t *testing.T) {
	resetUserExcludesCache(t)
	set := func(v string) {
		userExcludesMu.Lock()
		userExcludesDone, userExcludesCached = true, v
		userExcludesMu.Unlock()
	}
	set(os.DevNull)
	if got := statusExcludesFile(); got != "/dev/null" {
		t.Errorf("statusExcludesFile() = %q with the null device %q, want /dev/null", got, os.DevNull)
	}
	p := filepath.Join(t.TempDir(), "ignore")
	set(p)
	if got := statusExcludesFile(); got != p {
		t.Errorf("statusExcludesFile() = %q, want %q", got, p)
	}
}
