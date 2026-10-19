package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The filter rule of the git.worktree_create checkout (gitfilter.go). The cell
// names are those of the VM rounds, from a Linux VM with
// 5fd08069. A Windows VM ran a part of the cells. Every filter program is this
// test binary in the "filter-mark" helper mode. Its marker file proves whether
// it ran.

// filterFixture is a wtFixture whose commit also holds z.txt and a .gitattributes
// file. The user and the system config of the daemon's git calls are empty.
type filterFixture struct {
	wtFixture
	marks string
}

// newFilterFixture commits attrs as the .gitattributes file of the repository.
func newFilterFixture(t *testing.T, attrs string) filterFixture {
	t.Helper()
	isolateGitConfig(t)
	return buildFilterFixture(t, attrs)
}

// buildFilterFixture is newFilterFixture without isolateGitConfig. A test with
// parallel rows calls isolateGitConfig once itself, and each row calls this.
func buildFilterFixture(t *testing.T, attrs string) filterFixture {
	t.Helper()
	f := filterFixture{wtFixture: newWTFixture(t, false), marks: t.TempDir()}
	writeFile(t, filepath.Join(f.top, "z.txt"), "z\n", 0o644)
	writeFile(t, filepath.Join(f.top, ".gitattributes"), attrs, 0o644)
	runGit(t, f.top, "add", ".")
	runGit(t, f.top, "commit", "-q", "-m", "attributes")
	return f
}

// marker is a filter command that leaves the marker file name when it runs.
func (f filterFixture) marker(t *testing.T, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "CLAUSTRUM_TEST_HELPER=filter-mark '" + filepath.ToSlash(exe) + "' '" +
		filepath.ToSlash(filepath.Join(f.marks, name)) + "'"
}

// ran reports whether the marker name ran.
func (f filterFixture) ran(name string) bool {
	_, err := os.Stat(filepath.Join(f.marks, name))
	return err == nil
}

// own writes key and value into .git/config of the repository.
func (f filterFixture) own(t *testing.T, key, value string) {
	t.Helper()
	runGit(t, f.top, "config", key, value)
}

// outside makes the user config of the daemon's git calls a file with text.
func outside(t *testing.T, text string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, file, text, 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", file)
}

// filterRefusalFrame is the measured result of the refusal that names the filter
// m (cell A1), byte for byte.
const filterRefusalFrame = `"result":{"success":false,"error":"Claude doesn't run the \"m\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"m\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.","errorCode":"repository_only_filter"}}`

func (f filterFixture) wantRefused(t *testing.T, raw, name string) {
	t.Helper()
	wantError(t, raw, repositoryOnlyFilterText(name), "repository_only_filter")
	f.assertRolledBack(t, []string{}, false)
}

func (f filterFixture) wantCreatedLeaf(t *testing.T, raw string) {
	t.Helper()
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("reply = %s, want success", raw)
	}
	if b, err := os.ReadFile(filepath.Join(f.leaf(), "t.txt")); err != nil || strings.TrimRight(string(b), "\r\n") != "t" {
		t.Fatalf("t.txt of the new worktree = %q, %v, want its content", b, err)
	}
}

// Cells A1 and A2. A smudge or a process filter that only .git/config defines is
// refused with the measured frame, the filter does not run, and nothing is left.
func TestWorktreeCreateRefusesRepositoryOnlyFilter(t *testing.T) {
	for _, key := range []string{"smudge", "process"} {
		t.Run(key, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			f.own(t, "filter.m."+key, f.marker(t, "local"))
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			if !strings.HasSuffix(raw, filterRefusalFrame) {
				t.Fatalf("reply = %s\nwant the result %s", raw, filterRefusalFrame)
			}
			if f.ran("local") {
				t.Error("the filter of the repository ran")
			}
			f.assertRolledBack(t, []string{}, false)
			f.assertRetryFails(t)
		})
	}
}

// assertRetryFails runs the create again. The refusal left nothing that changes
// the answer.
func (f filterFixture) assertRetryFails(t *testing.T) {
	t.Helper()
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	f.wantRefused(t, raw, "m")
}

// Cell D6. A quote mark in the name has a backslash before it in the text.
func TestWorktreeCreateFilterRefusalQuotesTheName(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=q\"t\n")
	f.own(t, `filter.q"t.smudge`, f.marker(t, "local"))
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	want := `"error":"Claude doesn't run the \"q\\\"t\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"q\\\"t\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.","errorCode":"repository_only_filter"`
	if !strings.Contains(raw, want) {
		t.Fatalf("reply = %s\nwant %s", raw, want)
	}
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
}

// The states that are not refused. Cells A3 to A5, Q8, C1, B6 and A7.
func TestWorktreeCreateFilterStatesThatPass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f filterFixture)
		ran   []string
	}{
		{"clean alone", func(t *testing.T, f filterFixture) { f.own(t, "filter.m.clean", f.marker(t, "local")) }, nil},
		{"required alone", func(t *testing.T, f filterFixture) { f.own(t, "filter.m.required", "true") }, nil},
		{"empty smudge", func(t *testing.T, f filterFixture) { f.own(t, "filter.m.smudge", "") }, nil},
		{"a marker value and then cat", func(t *testing.T, f filterFixture) {
			f.own(t, "filter.m.smudge", f.marker(t, "local"))
			runGit(t, f.top, "config", "--add", "filter.m.smudge", "cat")
		}, nil},
		{"no path uses it", func(t *testing.T, f filterFixture) { f.own(t, "filter.unused.smudge", f.marker(t, "local")) }, nil},
		{"only the environment of the daemon defines it", func(t *testing.T, f filterFixture) {
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "filter.m.smudge")
			t.Setenv("GIT_CONFIG_VALUE_0", f.marker(t, "env"))
		}, []string{"env"}},
		{"clean in the repository and smudge outside", func(t *testing.T, f filterFixture) {
			f.own(t, "filter.m.clean", f.marker(t, "local"))
			outside(t, "[filter \"m\"]\n\tsmudge = "+f.marker(t, "home")+"\n")
		}, []string{"home"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			tc.setup(t, f)
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			f.wantCreatedLeaf(t, raw)
			for _, name := range []string{"local", "home", "env"} {
				if got, want := f.ran(name), slices.Contains(tc.ran, name); got != want {
					t.Errorf("marker %s ran = %v, want %v", name, got, want)
				}
			}
		})
	}
}

// Cells A7 of part 3 and O6. With a definition in the repository and one outside
// it, the create succeeds and the outside filter runs.
func TestWorktreeCreateOutsideFilterWins(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.smudge", f.marker(t, "local"))
	f.own(t, "filter.m.required", "true")
	outside(t, "[filter \"m\"]\n\tsmudge = "+f.marker(t, "home")+"\n")
	raw, argv := f.readTreeArgv(t, nil)
	wantFirstPairs(t, argv, "filter.m.required=false", "filter.m.smudge="+f.marker(t, "home"))
	f.wantCreatedLeaf(t, raw)
	if !f.ran("home") || f.ran("local") {
		t.Errorf("markers: home ran = %v, local ran = %v, want the outside filter alone", f.ran("home"), f.ran("local"))
	}
}

// Cell Q3. A filter name with "=" that only the repository defines gets the
// measured text, and nothing is left.
func TestWorktreeCreateRefusesFilterNameWithEquals(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=a=b\n")
	f.own(t, "filter.a=b.smudge", f.marker(t, "local"))
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	want := `"result":{"success":false,"error":"git worktree add failed (checkout): this repository's git config cannot be used: the filter \"a=b\" is defined only there, and Claude cannot handle \"=\" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it","errorCode":"worktree_add_failed"}}`
	if !strings.HasSuffix(raw, want) {
		t.Fatalf("reply = %s\nwant the result %s", raw, want)
	}
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
	f.assertRolledBack(t, []string{}, false)
}

// A process key of the repository beside an outside smudge key gets claustrum's
// own refusal, and no filter runs. 5fd08069 runs the outside filter and succeeds
// there (cell Q1). That difference stays.
func TestWorktreeCreateRefusesFilterThatNoPairReplaces(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.process", f.marker(t, "local"))
	outside(t, "[filter \"m\"]\n\tsmudge = "+f.marker(t, "home")+"\n")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	wantError(t, raw, "git worktree add failed (checkout): "+unreplaceableFilterText("m"), "worktree_add_failed")
	if f.ran("local") || f.ran("home") {
		t.Errorf("markers: local ran = %v, home ran = %v, want none", f.ran("local"), f.ran("home"))
	}
	f.assertRolledBack(t, []string{}, false)
}

// Cell Q7 (Linux VM): on 5fd08069 a cat first on PATH does not run. With smudge
// = cat the create succeeds and no cat program starts. The required = true of
// this fixture is in no cell: it is from the development host. The read-tree
// call gets
// three pairs that turn the filter off. A stand-in cat is first on PATH: this
// test binary in the "cat-mark" helper mode.
func TestWorktreeCreateSmudgeCatStartsNoProgram(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.smudge", "cat")
	f.own(t, "filter.m.required", "true")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if runtime.GOOS == "windows" {
		// A copy, because a symlink needs a privilege on Windows.
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(bin, "cat.exe"), string(b), 0o755)
	} else if err := os.Symlink(exe, filepath.Join(bin, "cat")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUSTRUM_TEST_CAT_MARK", filepath.Join(f.marks, "cat"))
	t.Setenv("CLAUSTRUM_TEST_HELPER", "cat-mark")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	f.wantCreatedLeaf(t, raw)
	if f.ran("cat") {
		t.Error("a cat program ran")
	}
	// The control. With the rule off, git runs the cat of PATH, so the stand-in
	// can be reached.
	f.assertRetryWithRuleOffRunsCat(t)
	plan := planFilters(parseFilterListing([]byte("filter.m.smudge\ncat\x00filter.m.required\ntrue\x00")), nil, "", false)
	if want := []string{"-c", "filter.m.process=", "-c", "filter.m.required=false", "-c", "filter.m.smudge="}; !slices.Equal(plan.args, want) {
		t.Errorf("pairs for smudge = cat: %q, want %q", plan.args, want)
	}
}

// assertRetryWithRuleOffRunsCat removes the worktree of the first create and
// creates it again with shadowDirOff. The stand-in cat must run then.
func (f filterFixture) assertRetryWithRuleOffRunsCat(t *testing.T) {
	t.Helper()
	runGit(t, f.top, "worktree", "remove", "--force", f.leaf())
	runGit(t, f.top, "branch", "-D", "w1")
	raw, _ := f.createWith(t, newTestServer(t), "w1", 0, map[string]any{"shadowDirOff": true})
	f.wantCreatedLeaf(t, raw)
	if runtime.GOOS == "windows" {
		// Git for Windows puts its own /usr/bin before the PATH of the caller, so its
		// own cat runs and the stand-in cannot (cells W1, Windows VM).
		t.Log("the marker half of the control does not run on Windows: Git for Windows finds its own cat first")
		return
	}
	if !f.ran("cat") {
		t.Error("control: with shadowDirOff the stand-in cat did not run, so the test cannot see a cat that runs")
	}
}

// A filter name that is not valid UTF-8 cannot go through a Windows command line
// as it is. A filter with such a name that only the repository defines is
// refused before the checkout, on every system, with claustrum's own text.
// 5fd08069 answers another text on Linux and success on Windows (cells S3 and
// X1). The test writes the config bytes itself.
func TestWorktreeCreateRefusesFilterNameThatIsNotUTF8(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=n\xffn\n")
	cfg := filepath.Join(f.top, ".git", "config")
	old, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, string(old)+"[filter \"n\xffn\"]\n\tsmudge = "+f.marker(t, "local")+"\n", 0o644)
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	wantError(t, raw, "git worktree add failed (checkout): this repository's git config cannot be used: the filter "+
		`"n\xffn" is defined only there, and its name is not valid UTF-8; define it in ~/.gitconfig on the remote machine, or rename it`,
		"worktree_add_failed")
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
	f.assertRolledBack(t, []string{}, false)

	// With a definition outside the repository too, the pairs with the outside
	// values cannot name the filter either.
	both := planFilters(parseFilterListing([]byte("filter.n\xffn.smudge\nout\x00filter.n\xffn.smudge\nown\x00")),
		parseFilterListing([]byte("filter.n\xffn.smudge\nout\x00")), "", false)
	if both.refusal != unreplaceableFilterText("n\xffn") || len(both.args) != 0 {
		t.Errorf("a name that is not UTF-8 on both sides: refusal %q, pairs %q", both.refusal, both.args)
	}
}

// Cells Q8. The last value of a key counts. cat and then a marker value is
// refused. The reverse order is a row of TestWorktreeCreateFilterStatesThatPass.
func TestWorktreeCreateLastFilterValueCounts(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.smudge", "cat")
	runGit(t, f.top, "config", "--add", "filter.m.smudge", f.marker(t, "local"))
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	f.wantRefused(t, raw, "m")
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
}

// Cell Q14. A git that refuses --show-scope with exit code 129 gets the two
// plain listings, and the filter is still refused. The outside listing runs in
// the new worktree, not in the temporary directory.
func TestWorktreeCreateFilterRuleWithOldGit(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.smudge", f.marker(t, "local"))
	installGitSlowStub(t, f.realGit)
	ctxlog := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "noscope")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	f.wantRefused(t, raw, "m")
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
	calls := readShapeCalls(t, ctxlog)
	at := slices.IndexFunc(calls, func(c shapeCall) bool { return c.has("--show-scope") })
	if at < 0 || at+3 >= len(calls) {
		t.Fatalf("no scoped listing with three calls after it")
	}
	out, full, rt := calls[at+1], calls[at+2], calls[at+3]
	if !slices.Equal(out.argv, []string{"config", "-z", "--list"}) || !slices.Contains(out.all, "GIT_DIR="+os.DevNull) || out.cwd != full.cwd {
		t.Errorf("call after the scoped listing = %q in %s, want config -z --list with GIT_DIR at the null device in %s", out.argv, out.cwd, full.cwd)
	}
	if !full.listing() || !strings.HasPrefix(full.argv[0], "--git-dir=") || !rt.has("read-tree") {
		t.Errorf("then %q and %q, want the listing of the checkout and the read-tree call", full.argv, rt.argv)
	}
}

// With a git that refuses --show-scope, a filter that only the user config
// defines still runs: the plain outside listing shows it. Cell A7 on that path,
// which no VM ran.
func TestWorktreeCreateOutsideFilterWithOldGit(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.clean", f.marker(t, "local"))
	outside(t, "[filter \"m\"]\n\tsmudge = "+f.marker(t, "home")+"\n")
	installGitSlowStub(t, f.realGit)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "noscope")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	f.wantCreatedLeaf(t, raw)
	if !f.ran("home") {
		t.Error("the outside filter did not run")
	}
}

// Cell R1. A filter that the user config defines through includeIf "gitdir:…"
// is outside the repository: it runs, and the create succeeds. With a git that
// refuses --show-scope the same filter is refused, where 5fd08069 runs it (cell
// S1). That second row is the accepted edge of an old git.
func TestWorktreeCreateFilterOfConditionalInclude(t *testing.T) {
	for _, tc := range []struct {
		name    string
		oldGit  bool
		refused bool
	}{{"scoped listing", false, false}, {"git refuses --show-scope", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			top, err := filepath.EvalSymlinks(f.top)
			if err != nil {
				t.Fatal(err)
			}
			inc := filepath.Join(t.TempDir(), "included")
			writeFile(t, inc, "[filter \"m\"]\n\tsmudge = "+f.marker(t, "home")+"\n", 0o644)
			outside(t, "[includeIf \"gitdir/i:"+filepath.ToSlash(top)+"/\"]\n\tpath = "+filepath.ToSlash(inc)+"\n")
			if tc.oldGit {
				installGitSlowStub(t, f.realGit)
				t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "noscope")
			}
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			if tc.refused {
				f.wantRefused(t, raw, "m")
			} else {
				f.wantCreatedLeaf(t, raw)
			}
			if f.ran("home") == tc.refused {
				t.Errorf("the filter of the included file ran = %v, want %v", f.ran("home"), !tc.refused)
			}
		})
	}
}

// A listing of the rule that fails refuses the create as a failed checkout, with
// the text of git. No read-tree call runs and no filter runs. The scoped listing
// fails here for another reason than its option. Not measured.
func TestWorktreeCreateFilterListingFails(t *testing.T) {
	for _, tc := range []struct{ seam, which string }{
		{"scoped", "scoped"}, {"noscope,outside", "outside"}, {"noscope,full", "full"},
	} {
		t.Run(tc.seam, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			f.own(t, "filter.m.smudge", f.marker(t, "local"))
			installGitSlowStub(t, f.realGit)
			log := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
			t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", tc.seam)
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			wantError(t, raw, "git worktree add failed (checkout): fatal: stub refuses the "+tc.which+" listing", "worktree_add_failed")
			if calls := gitCalls(t, log, "read-tree"); len(calls) != 0 {
				t.Errorf("read-tree calls = %q, want none", calls)
			}
			if f.ran("local") {
				t.Error("the filter of the repository ran")
			}
			f.assertRolledBack(t, []string{}, false)
		})
	}
}

// A timeoutMs of the caller that expires during a listing before the read-tree
// call answers the timeout frame, as before the filter rule. The repository has
// no filter. The frame is the one of main at 81c8893 for a wait on the listing
// of the checkout, and the one of cells S2 (Linux VM).
//
// The deadline ends a listing of the filter rule. It does not end the listing of
// the checkout, which is the "full" one: that one runs to its end, as on
// 5fd08069 (cell T12 of round task010-01, Linux VM). With "noscope" the full
// listing of the rule is held first, and then the one of the checkout.
//
// The stub holds the listing that the seam names and no other call: its log has
// one "held" line with that name. The read of the config file is not held. A
// held "full" listing sleeps 1 s past the deadline and ends. Another held
// listing sleeps 30 s past it, and the kill at the deadline must end it. A killed
// stub writes no "held-end" line, so that test does not depend on the time.
func TestWorktreeCreateDeadlineDuringFilterListing(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	lateMs := int((2*calibrate(t, realGit) + 500*time.Millisecond).Milliseconds())
	for _, seam := range []string{"hold-scoped", "noscope,hold-outside", "noscope,hold-full", "hold-full"} {
		t.Run(seam, func(t *testing.T) {
			f := newWTFixture(t, false)
			log := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
			t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", seam)
			kind := seam[strings.LastIndex(seam, "hold-")+len("hold-"):]
			full := kind == "full"
			holdMs := lateMs + 30000
			if full {
				holdMs = lateMs + 1000
			}
			t.Setenv("CLAUSTRUM_GITSTUB_HOLD_MS", strconv.Itoa(holdMs))
			raw, took := f.create(t, newTestServer(t), "w1", "", lateMs)
			wantError(t, raw, "git worktree add timed out after "+strconv.Itoa(lateMs)+
				"ms (deadline expired during the checkout): context deadline exceeded", "timeout")
			f.assertRolledBack(t, []string{}, false)
			want := 1
			if seam == "noscope,hold-full" {
				want = 2
			}
			if held := gitCalls(t, log, "held"); len(held) != want || held[0][1] != kind {
				t.Errorf("held calls = %q, want %d of the %s listing", held, want, kind)
			}
			hold := time.Duration(holdMs) * time.Millisecond
			if full && took < hold {
				t.Errorf("the create answered after %s, want it after the held listing (%d ms)", took, holdMs)
			}
			if !full && took >= hold {
				t.Errorf("the create answered after %s, want it at its deadline of %d ms", took, lateMs)
			}
			if ended := gitCalls(t, log, "held-end"); !full && len(ended) != 0 {
				t.Errorf("held listings that ran to their end = %q, want the %s listing killed at the deadline", ended, kind)
			}
		})
	}
}

// The listing with no repository in view has GIT_DIR at the null device, and it
// has no GIT_COMMON_DIR and no GIT_WORK_TREE of the daemon. The config variables
// of the daemon stay.
func TestOutsideListingEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/daemon/git")
	t.Setenv("GIT_COMMON_DIR", "/daemon/common")
	t.Setenv("GIT_WORK_TREE", "/daemon/tree")
	t.Setenv("GIT_CONFIG_GLOBAL", "/daemon/gitconfig")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	var got []string
	for _, kv := range outsideListingEnv() {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_COMMON_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") || strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") {
			got = append(got, kv)
		}
	}
	slices.Sort(got)
	want := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_GLOBAL=/daemon/gitconfig", "GIT_DIR=" + os.DevNull}
	if !slices.Equal(got, want) {
		t.Fatalf("outside listing env = %q, want %q", got, want)
	}
}

// Cells A8, O4 and O5. A clean key, a required key or an empty smudge from
// outside is no definition, so the refusal stays.
func TestWorktreeCreateOutsideKeysThatDefineNothing(t *testing.T) {
	for _, line := range []string{"clean = cat", "required = true", "smudge ="} {
		t.Run(line, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			f.own(t, "filter.m.smudge", f.marker(t, "local"))
			outside(t, "[filter \"m\"]\n\t"+line+"\n")
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			f.wantRefused(t, raw, "m")
			if f.ran("local") {
				t.Error("the filter of the repository ran")
			}
		})
	}
}

// Cells V2 to V6, V8 and V9. Each value is refused. No marker is in them, so the
// reply is the proof.
func TestWorktreeCreateRefusedFilterValues(t *testing.T) {
	// The rows share the git config isolation of the test, so they run in parallel.
	isolateGitConfig(t)
	for _, tc := range []struct{ filter, value string }{
		{"m", "cat "}, {"m", "/bin/cat"}, {"m", "cat -"}, {"m", "cat %f"}, {"m", "true"},
		{"m", "git-lfs smudge -- %f"}, {"lfs", "git lfs smudge -- %f"},
	} {
		t.Run(tc.filter+" "+tc.value, func(t *testing.T) {
			t.Parallel()
			f := buildFilterFixture(t, "t.txt filter="+tc.filter+"\n")
			f.own(t, "filter."+tc.filter+".smudge", tc.value)
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			wantError(t, raw, repositoryOnlyFilterText(tc.filter), "repository_only_filter")
			f.assertRolledBack(t, []string{}, false)
		})
	}
}

// readTreeArgv runs one create through the "git-slow" stub and returns the reply
// and the argv of its read-tree call.
func (f filterFixture) readTreeArgv(t *testing.T, extra map[string]any) (string, []string) {
	t.Helper()
	return f.readTreeArgvWith(t, extra, "")
}

// readTreeArgvWith is readTreeArgv with a CLAUSTRUM_GITSTUB_FAIL_LISTING value.
func (f filterFixture) readTreeArgvWith(t *testing.T, extra map[string]any, seam string) (string, []string) {
	t.Helper()
	installGitSlowStub(t, f.realGit)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", seam)
	log := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	raw, _ := f.createWith(t, newTestServer(t), "w1", 0, extra)
	calls := gitCalls(t, log, "read-tree")
	if len(calls) != 1 {
		t.Fatalf("read-tree calls = %q, want one", calls)
	}
	return raw, calls[0]
}

// wantFirstPairs fails the test unless argv starts with the -c pairs and the
// profile follows them.
func wantFirstPairs(t *testing.T, argv []string, pairs ...string) {
	t.Helper()
	var want []string
	for _, p := range pairs {
		want = append(want, "-c", p)
	}
	want = append(want, "-c", "core.hooksPath=/dev/null")
	if len(argv) < len(want) || !slices.Equal(argv[:len(want)], want) {
		t.Fatalf("read-tree argv = %q\nwant it to start with %q", argv, want)
	}
	n := 0
	for _, a := range argv {
		if strings.HasPrefix(a, "filter.") {
			n++
		}
	}
	if n != len(pairs) {
		t.Fatalf("read-tree argv = %q, want %d filter pairs", argv, len(pairs))
	}
}

// Cells V7 and V13. Under the name lfs the two git-lfs values pass, and the
// read-tree call gets each as a pair. The create does not need a git-lfs program:
// a smudge filter that does not start is no failure for git.
func TestWorktreeCreatePassesLfsFilterOn(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=lfs\n")
	f.own(t, "filter.lfs.smudge", "git-lfs smudge -- %f")
	raw, argv := f.readTreeArgv(t, nil)
	wantFirstPairs(t, argv, "filter.lfs.smudge=git-lfs smudge -- %f")
	f.wantCreatedLeaf(t, raw)

	plan := planFilters(parseFilterListing([]byte("filter.lfs.process\ngit-lfs filter-process\x00")), nil, "", false)
	if want := []string{"-c", "filter.lfs.process=git-lfs filter-process"}; !slices.Equal(plan.args, want) || len(plan.refused) != 0 {
		t.Fatalf("plan of the lfs process value = %q refused %q, want %q and none", plan.args, plan.refused, want)
	}
}

// Cells X4 and L1 (Windows VM). The repository's own config has a git-lfs key
// that names a program. The lfs values then do not pass: the read-tree call gets
// the three pairs, and the answer is the refusal that names lfs (cell L1 with
// build D). The same keys in the user config change nothing. The rule
// is claustrum's own. No git-lfs program is needed.
func TestWorktreeCreateLfsWithTransferProgramOfRepository(t *testing.T) {
	off := []string{"filter.lfs.process=", "filter.lfs.required=true", "filter.lfs.smudge=false"}
	for _, tc := range []struct {
		name    string
		own     [][2]string
		outside string
		oldGit  bool
		refused bool
	}{
		{"custom transfer path", [][2]string{{"lfs.customtransfer.x.path", "/no/such/agent"}}, "", false, true},
		{"standalone transfer agent", [][2]string{{"lfs.standalonetransferagent", "x"}}, "", false, true},
		{"extension command", [][2]string{{"lfs.extension.x.clean", "/no/such/command"}}, "", false, true},
		{"both keys, git refuses --show-scope", [][2]string{{"lfs.standalonetransferagent", "x"}, {"lfs.customtransfer.x.path", "/no/such/agent"}}, "", true, true},
		{"another lfs key", [][2]string{{"lfs.url", "https://example.invalid/lfs"}}, "", false, false},
		{"the keys in the user config", nil, "[lfs]\n\tstandalonetransferagent = x\n[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n", false, false},
		{"the keys in the user config, git refuses --show-scope", nil, "[lfs]\n\tstandalonetransferagent = x\n[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=lfs\n")
			f.own(t, "filter.lfs.smudge", "git-lfs smudge -- %f")
			for _, kv := range tc.own {
				f.own(t, kv[0], kv[1])
			}
			if tc.outside != "" {
				outside(t, tc.outside)
			}
			seam := ""
			if tc.oldGit {
				seam = "noscope"
			}
			raw, argv := f.readTreeArgvWith(t, nil, seam)
			if tc.refused {
				wantFirstPairs(t, argv, off...)
				wantError(t, raw, repositoryOnlyFilterText("lfs"), "repository_only_filter")
				f.assertRolledBack(t, []string{}, false)
				return
			}
			wantFirstPairs(t, argv, "filter.lfs.smudge=git-lfs smudge -- %f")
			f.wantCreatedLeaf(t, raw)
		})
	}
}

// The filter lfs of the user config, and a program key of git-lfs in the
// repository's own config. The filter then does not run: the read-tree call
// gets the three pairs. A path under the filter gets claustrum's own text, and
// with no such path the create succeeds, and another filter of the user config
// still runs. With no such key, or with the keys in
// the user config only, the outside filter runs and the call has no pair. The
// outside filter is a marker, so no git-lfs program is needed. A Windows VM ran
// the rule with a working git-lfs (cells L2 to L5 and L7). The lfs.extension row
// is from the help text of git-lfs 3.7.1, and no VM ran it.
func TestWorktreeCreateOutsideLfsWithTransferProgramOfRepository(t *testing.T) {
	const urlKey = "lfs.https://example.invalid/r.standalonetransferagent"
	userKeys := "[lfs]\n\tstandalonetransferagent = x\n[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n"
	off := []string{"filter.lfs.process=", "filter.lfs.required=true", "filter.lfs.smudge=false"}
	for _, tc := range []struct {
		name    string
		attrs   string
		own     [][2]string
		user    string
		oldGit  bool
		wantKey string // the key in the refusal, or "" for a create that succeeds
		pairs   bool
	}{
		{"custom transfer path", "t.txt filter=lfs\n", [][2]string{{"lfs.customtransfer.x.path", "/no/such/agent"}}, "", false, "lfs.customtransfer.x.path", true},
		{"standalone transfer agent", "t.txt filter=lfs\n", [][2]string{{"lfs.standalonetransferagent", "x"}}, "", false, "lfs.standalonetransferagent", true},
		{"standalone transfer agent of a URL", "t.txt filter=lfs\n", [][2]string{{urlKey, "x"}}, "", false, urlKey, true},
		{"extension command", "t.txt filter=lfs\n", [][2]string{{"lfs.extension.x.smudge", "/no/such/command"}}, "", false, "lfs.extension.x.smudge", true},
		{"two keys, the first in sorted order is named", "t.txt filter=lfs\n", [][2]string{{"lfs.standalonetransferagent", "x"}, {"lfs.customtransfer.x.path", "/no/such/agent"}}, "", false, "lfs.customtransfer.x.path", true},
		{"git refuses --show-scope", "t.txt filter=lfs\n", [][2]string{{"lfs.standalonetransferagent", "x"}}, "", true, "lfs.standalonetransferagent", true},
		{"a path under another filter of the user config", "t.txt filter=m\n", [][2]string{{"lfs.standalonetransferagent", "x"}}, "", false, "", true},
		{"no key", "t.txt filter=lfs\n", [][2]string{{"lfs.url", "https://example.invalid/lfs"}}, "", false, "", false},
		{"the keys in the user config", "t.txt filter=lfs\n", nil, userKeys, false, "", false},
		{"the keys in the user config, git refuses --show-scope", "t.txt filter=lfs\n", nil, userKeys, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilterFixture(t, tc.attrs)
			for _, kv := range tc.own {
				f.own(t, kv[0], kv[1])
			}
			outside(t, "[filter \"lfs\"]\n\tsmudge = "+f.marker(t, "home")+"\n[filter \"m\"]\n\tsmudge = "+f.marker(t, "m")+"\n"+tc.user)
			seam := ""
			if tc.oldGit {
				seam = "noscope"
			}
			raw, argv := f.readTreeArgvWith(t, nil, seam)
			if tc.pairs {
				wantFirstPairs(t, argv, off...)
			} else {
				wantFirstPairs(t, argv)
			}
			uses := strings.Contains(tc.attrs, "filter=lfs")
			if tc.wantKey != "" {
				wantError(t, raw, "git worktree add failed (checkout): this repository's git config cannot be used: it names a program for git-lfs (\""+
					tc.wantKey+"\"); remove that setting, or put it into ~/.gitconfig on the remote machine", "worktree_add_failed")
				f.assertRolledBack(t, []string{}, false)
			} else {
				f.wantCreatedLeaf(t, raw)
			}
			if want := uses && !tc.pairs; f.ran("home") != want {
				t.Errorf("the lfs filter of the user config ran = %v, want %v", f.ran("home"), want)
			}
			if f.ran("m") == uses {
				t.Errorf("the filter m of the user config ran = %v, want %v", f.ran("m"), !uses)
			}
		})
	}
}

// Cells N1, N2 and N4. Every repository-only filter gets its three pairs, in
// sorted order of the names and before the profile. The refusal names the filter
// of the first path that git reports.
func TestWorktreeCreateTwoRepositoryOnlyFilters(t *testing.T) {
	pairs := []string{
		"filter.b.process=", "filter.b.required=true", "filter.b.smudge=false",
		"filter.m.process=", "filter.m.required=true", "filter.m.smudge=false",
	}
	for _, tc := range []struct{ attrs, want string }{
		{"t.txt filter=b\nz.txt filter=m\n", "b"},
		{"t.txt filter=m\nz.txt filter=b\n", "m"},
		{"t.txt filter=b\n", "b"},
	} {
		t.Run(tc.attrs, func(t *testing.T) {
			f := newFilterFixture(t, tc.attrs)
			f.own(t, "filter.m.smudge", f.marker(t, "m"))
			f.own(t, "filter.b.smudge", f.marker(t, "b"))
			raw, argv := f.readTreeArgv(t, nil)
			wantFirstPairs(t, argv, pairs...)
			f.wantRefused(t, raw, tc.want)
			if f.ran("m") || f.ran("b") {
				t.Errorf("markers: m ran = %v, b ran = %v, want none", f.ran("m"), f.ran("b"))
			}
		})
	}
}

// Cells D5 and Q4. A name over 200 bytes gets its own text with the first 40
// bytes, and nothing is left. 5fd08069 makes no read-tree call there (Windows
// VM). A name of 200 bytes gets the three pairs. Cell Q5: a name of 300 bytes
// with smudge = cat is not refused.
func TestWorktreeCreateFilterNameBound(t *testing.T) {
	long := strings.Repeat("n", 300)
	f := newFilterFixture(t, "t.txt filter="+long+"\n")
	f.own(t, "filter."+long+".smudge", f.marker(t, "local"))
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	want := `"result":{"success":false,"error":"git worktree add failed (checkout): this repository's git config cannot be used: a filter defined only there has a name longer than 200 bytes (it starts \"nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn\"); define it in ~/.gitconfig on the remote machine, or rename it","errorCode":"worktree_add_failed"}}`
	if !strings.HasSuffix(raw, want) {
		t.Fatalf("reply = %s\nwant the result %s", raw, want)
	}
	if f.ran("local") {
		t.Error("the filter of the repository ran")
	}
	f.assertRolledBack(t, []string{}, false)

	for n, refusal := range map[int]bool{200: false, 201: true} {
		name := strings.Repeat("n", n)
		plan := planFilters(parseFilterListing([]byte("filter."+name+".smudge\nx\x00")), nil, "", false)
		if (plan.refusal != "") != refusal || slices.Contains(plan.refused, name) == refusal {
			t.Errorf("name of %d bytes: refusal %q, three pairs %v", n, plan.refusal, len(plan.refused) == 1)
		}
	}
	plan := planFilters(parseFilterListing([]byte("filter."+long+".smudge\ncat\x00")), nil, "", false)
	if plan.refusal != "" || len(plan.refused) != 0 {
		t.Errorf("name of 300 bytes with smudge = cat: refusal %q, three pairs %v, want neither", plan.refusal, plan.refused)
	}
}

// Cell E1. With shadowDirOff the read-tree call gets no filter pair, the filter
// of the repository runs, and the create succeeds.
func TestWorktreeCreateShadowDirOffRunsTheFilter(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	f.own(t, "filter.m.smudge", f.marker(t, "local"))
	raw, argv := f.readTreeArgv(t, map[string]any{"shadowDirOff": true})
	wantFirstPairs(t, argv)
	f.wantCreatedLeaf(t, raw)
	if !f.ran("local") {
		t.Error("the filter of the repository did not run")
	}
}

// A shadowDirOff value that is not the
// JSON value true is no error on a git method, and the frame is that of the
// request with no member. On 5fd08069 the frame of git.info is also that of no
// member with false and with {} (cells Q12, Linux VM).
func TestShadowDirOffOtherValuesChangeNoFrame(t *testing.T) {
	f := newFilterFixture(t, "t.txt filter=m\n")
	s := newTestServer(t)
	for _, method := range []string{"git.info", "git.status"} {
		want := dispatchRaw(t, s, rpcLine(t, method, map[string]any{"path": f.top, "baseRepo": f.top}))
		if !strings.Contains(want, `"result"`) {
			t.Fatalf("%s with no member = %s, want a result", method, want)
		}
		for _, v := range []any{"true", 1, nil, false, map[string]any{}} {
			got := dispatchRaw(t, s, rpcLine(t, method, map[string]any{"path": f.top, "baseRepo": f.top, "shadowDirOff": v}))
			if got != want {
				t.Errorf("%s with shadowDirOff %#v = %s\nwant %s", method, v, got, want)
			}
		}
	}
}

// Only the JSON value true turns the filter rule off. The string "true" and the
// number 1 leave it on, as on 5fd08069 (cells Q12, Linux and Windows VMs).
func TestWorktreeCreateShadowDirOffNeedsTrue(t *testing.T) {
	for _, v := range []any{"true", 1} {
		f := newFilterFixture(t, "t.txt filter=m\n")
		f.own(t, "filter.m.smudge", f.marker(t, "local"))
		raw, _ := f.createWith(t, newTestServer(t), "w1", 0, map[string]any{"shadowDirOff": v})
		f.wantRefused(t, raw, "m")
		if f.ran("local") {
			t.Errorf("shadowDirOff %#v: the filter of the repository ran", v)
		}
	}
}

// The text of git decides the refusal. Another wording around "filter m failed"
// still gives it. A failed checkout with no such text is the failed checkout of
// before, with git's text. The stub writes the stderr and the exit code of the
// read-tree call.
func TestWorktreeCreateFilterRefusalReadsGitText(t *testing.T) {
	for _, tc := range []struct{ stderr, text, code string }{
		{"error: t.txt: the required filter m failed here\n", repositoryOnlyFilterText("m"), "repository_only_filter"},
		{"fatal: unable to read tree\n", "git worktree add failed (checkout): fatal: unable to read tree", "worktree_add_failed"},
		{"fatal: t.txt: smudge filter other failed\n", "git worktree add failed (checkout): fatal: t.txt: smudge filter other failed", "worktree_add_failed"},
	} {
		t.Run(tc.stderr, func(t *testing.T) {
			f := newFilterFixture(t, "t.txt filter=m\n")
			f.own(t, "filter.m.smudge", f.marker(t, "local"))
			installGitSlowStub(t, f.realGit)
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			slowGit(t, "read-tree", "fail", 0, strings.ReplaceAll(tc.stderr, "\n", `\n`), "")
			raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
			wantError(t, raw, tc.text, tc.code)
			f.assertRolledBack(t, []string{}, false)
		})
	}
}

// The two listings as git prints them: the one that the checkout reads, and the
// one with no repository in view. Cells B1 to B6, D3, V14, Q6 and Q9.
func TestPlanFilters(t *testing.T) {
	listing := func(recs ...string) map[string]filterValues {
		return parseFilterListing([]byte(strings.Join(recs, "\x00") + "\x00"))
	}
	outside := listing(
		"filter.s.smudge\nsys",
		"filter.g.process\nglob",
		"filter.e.smudge\nenv",
		"filter.both.smudge\nout",
		"filter.empty.smudge\n",
	)
	full := listing(
		"filter.s.smudge\nsys",
		"filter.g.process\nglob",
		"filter.both.smudge\nout",
		"filter.empty.smudge\n",
		"filter.m.smudge\nown",
		"filter.M.smudge\ncat",
		"filter.w.process\nwt",
		"filter.both.smudge\nown",
		"filter.both.required\ntrue",
		"filter.empty.smudge\nown",
		"filter.p.process\n",
		"filter.p.smudge\nown",
		"filter.a.b.required",
		"filter.m.clean\nc",
		"core.bare\nfalse",
		"filter.short\nx",
		"filter.e.smudge\nenv",
	)
	if v := full["a.b"]; !v.has[filterRequired] || v.last[filterRequired] != "true" || v.defined() {
		t.Errorf("filter a.b = %+v, want a required value true and no definition", v)
	}
	off := func(name string) []string {
		return []string{"-c", "filter." + name + ".process=", "-c", "filter." + name + ".required=true", "-c", "filter." + name + ".smudge=false"}
	}
	plan := planFilters(full, outside, "", false)
	want := slices.Concat(
		[]string{"-c", "filter.M.process=", "-c", "filter.M.required=false", "-c", "filter.M.smudge="},
		[]string{"-c", "filter.both.required=false", "-c", "filter.both.smudge=out"},
		off("empty"), off("m"), off("p"), off("w"),
	)
	if !slices.Equal(plan.args, want) || !slices.Equal(plan.refused, []string{"empty", "m", "p", "w"}) || plan.refusal != "" {
		t.Errorf("plan = %q\nrefused %q, refusal %q\nwant %q\nrefused [empty m p w]", plan.args, plan.refused, plan.refusal, want)
	}
	if name, ok := plan.refusedFilter("fatal: a: smudge filter w failed\nfatal: b: smudge filter m failed\n"); !ok || name != "w" {
		t.Errorf("refused filter = %q, %v, want w: the first one in the text", name, ok)
	}
	// A path that holds the words names no filter: the end of the fatal line counts.
	if name, ok := plan.refusedFilter("fatal: x filter empty failed.txt: smudge filter m failed\n"); !ok || name != "m" {
		t.Errorf("refused filter = %q, %v, want m: the name at the end of the line", name, ok)
	}
	if name, ok := plan.refusedFilter("fatal: a: smudge filter M failed\n"); ok {
		t.Errorf("refused filter = %q, want none: M has no three pairs", name)
	}
}

// The scoped listing as git prints it. The scopes local and worktree are the
// repository's own config. Cells B1 to B6 and R1.
func TestSplitScopedListing(t *testing.T) {
	scoped := strings.Join([]string{
		"system", "filter.s.smudge\nsys",
		"global", "filter.g.process\nglob",
		"local", "filter.m.smudge\nown",
		"worktree", "filter.w.process\nwt",
		"command", "filter.e.smudge\nenv",
		"local", "filter.g.required",
	}, "\x00") + "\x00"
	full, outside := splitScopedListing([]byte(scoped))
	wantFull := "filter.s.smudge\nsys\x00filter.g.process\nglob\x00filter.m.smudge\nown\x00filter.w.process\nwt\x00filter.e.smudge\nenv\x00filter.g.required\x00"
	wantOutside := "filter.s.smudge\nsys\x00filter.g.process\nglob\x00filter.e.smudge\nenv\x00"
	if string(full) != wantFull || string(outside) != wantOutside {
		t.Errorf("full = %q\noutside = %q\nwant %q\nand %q", full, outside, wantFull, wantOutside)
	}
	plan := planFromListings(full, outside, false)
	if !slices.Equal(plan.refused, []string{"m", "w"}) {
		t.Errorf("refused = %q, want [m w]", plan.refused)
	}
}
