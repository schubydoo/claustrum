package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// The git calls outside any repository, claustrum's own rules. Every
// call here runs through the git stub of the test binary, which records the
// working folder and the environment of each call (CLAUSTRUM_GITSTUB_ENVLOG).

// outsideStub puts the stub first on PATH and returns the reader of its log. It
// sets one name that the filter drops (a relative GIT_SSH), GIT_CONFIG_SYSTEM
// with an absolute value (the filter keeps it, the version run drops it) and
// GIT_CONFIG_NOSYSTEM. match is the stub rule: "" runs every call on the real git.
func outsideStub(t *testing.T) func() []envCall {
	t.Helper()
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	log := filepath.Join(t.TempDir(), "env.log")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	t.Setenv("GIT_SSH", "rel/ssh")
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return func() []envCall { return readEnvCalls(t, log) }
}

// stubAnswer makes each call that has all the words of match exit with code and
// print stdout, with no real git. The words are exact arguments.
func stubAnswer(t *testing.T, match string, code int, stdout string) {
	t.Helper()
	slowGit(t, match, "fail", 0, "", stdout)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", strconv.Itoa(code))
}

// resetAttrSourceForTest clears the stored --attr-source probe so it re-runs
// under a controlled PATH. Direct assignment is safe in a test that is not parallel
// and has no parallel parent: nothing else probes at that time. A top-level test
// with t.Parallel() starts after every sequential top-level test ended. Parallel
// rows end before their own top-level test does. Each test that resets the probe
// also calls t.Setenv, and Go refuses t.Setenv in a parallel test.
func resetAttrSourceForTest() {
	attrSourceDone = false
	attrSourceOK = false
}

func sameFolder(a, b string) bool {
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return ea != "" && ea == eb
}

// checkOutside tests the folder of a call and its filtered environment. A
// relative GIT_SSH is gone, GIT_CONFIG_NOSYSTEM stays. versionRun says whether
// the call is a plain version run, which also drops GIT_CONFIG_SYSTEM.
func checkOutside(t *testing.T, what string, c envCall, versionRun bool) {
	t.Helper()
	if !sameFolder(c.cwd, gitVersionDir) {
		t.Errorf("%s runs in %q, want %q", what, c.cwd, gitVersionDir)
	}
	if v, ok := lookupExact(c.env, "GIT_SSH"); ok {
		t.Errorf("%s keeps the relative GIT_SSH=%q: the environment is not the filtered one", what, v)
	}
	if v, _ := lookupExact(c.env, "GIT_CONFIG_NOSYSTEM"); v != "1" {
		t.Errorf("%s lost GIT_CONFIG_NOSYSTEM (%q)", what, v)
	}
	_, system := lookupExact(c.env, "GIT_CONFIG_SYSTEM")
	if system == versionRun {
		t.Errorf("%s has GIT_CONFIG_SYSTEM set = %v, want %v", what, system, !versionRun)
	}
}

// the names that the `git version` run removes, with exact case. A given
// list is not filtered. A nil list is the filtered environment.
func TestGitVersionEnvNames(t *testing.T) {
	given := []string{
		"GIT_DIR=/a", "GIT_CONFIG=/a", "GIT_NAMESPACE=a", "GIT_WORK_TREE=/a", "GIT_COMMON_DIR=/a",
		"GIT_INDEX_FILE=/a", "GIT_OBJECT_DIRECTORY=/a", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/a",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_GLOBAL=/a", "GIT_CONFIG_SYSTEM=/a", "GIT_CONFIG_PARAMETERS='a'",
		"GIT_CEILING_DIRECTORIES=/a", "GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_CONFIG_KEY_0=a", "GIT_CONFIG_VALUE_0=b", "GIT_CONFIG_KEY_x=a", "GIT_CONFIG_VALUE_=b",
		// These stay: another spelling, other names, and a relative program name that
		// the filter would drop but a given list does not pass through.
		"git_config_count=1", "Git_Dir=/a", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_KEY=a",
		"GIT_SSH=rel/ssh", "LC_ALL=C", "KEEP=1",
	}
	want := []string{"git_config_count=1", "Git_Dir=/a", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_KEY=a",
		"GIT_SSH=rel/ssh", "LC_ALL=C", "KEEP=1"}
	if got := gitVersionEnv(given); !slices.Equal(got, want) {
		t.Errorf("gitVersionEnv(given) = %q, want %q", got, want)
	}
	if got := gitVersionEnv([]string{}); got == nil {
		t.Error("gitVersionEnv of an empty list is nil, which would hand git the daemon's environment")
	}

	t.Setenv("GIT_SSH", "rel/ssh")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "git"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	env := gitVersionEnv(nil)
	for name, wantSet := range map[string]bool{"GIT_SSH": false, "GIT_DIR": false, "GIT_CONFIG_NOSYSTEM": true} {
		if _, set := lookupExact(env, name); set != wantSet {
			t.Errorf("gitVersionEnv(nil) has %s set = %v, want %v", name, set, wantSet)
		}
	}
}

// the plain `git version` runs of the shadow code and of the failed
// listing run in the folder of gitVersionDir with the filtered environment, and
// drop the names of the version run.
func TestGitVersionRunsFolderAndEnv(t *testing.T) {
	calls := outsideStub(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "a.b")
	t.Setenv("GIT_CONFIG_VALUE_0", "c")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(t.TempDir(), "up"))
	for _, c := range []struct {
		name string
		run  func()
	}{
		{"gitVersionRun", func() { gitVersionRun(gitUse{}, true) }},
		{"versionAfterFailure", func() { versionAfterFailure() }},
		{"shadowVersionNote", func() { shadowVersionNote(t.TempDir(), shadowTestEnv()) }},
	} {
		before := len(calls())
		c.run()
		got := calls()[before:]
		if len(got) == 0 {
			t.Fatalf("%s made no git call", c.name)
		}
		first := got[0]
		if !first.version() {
			t.Fatalf("%s: first call = %q, want version", c.name, first.argv)
		}
		checkOutside(t, c.name, first, true)
		for _, n := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CEILING_DIRECTORIES"} {
			if v, ok := lookupExact(first.env, n); ok {
				t.Errorf("%s keeps %s=%q", c.name, n, v)
			}
		}
	}
}

// SystemRoot if it is absolute, else the folder of the executable,
// else the working folder of the daemon. The rule runs on every system.
func TestWindowsCallDir(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "claustrum")
	for _, c := range []struct {
		name, root string
		exe        string
		exeErr     error
		want       string
	}{
		{"absolute SystemRoot", `C:\Windows`, exe, nil, `C:\Windows`},
		{"UNC SystemRoot", `\\srv\share\win`, exe, nil, `\\srv\share\win`},
		{"relative SystemRoot", `Windows`, exe, nil, filepath.Dir(exe)},
		{"no drive SystemRoot", `\Windows`, exe, nil, filepath.Dir(exe)},
		{"empty SystemRoot", ``, exe, nil, filepath.Dir(exe)},
		{"no executable", ``, ``, errors.New("no executable"), ``},
		{"relative root, no executable", `Windows`, exe, errors.New("no executable"), ``},
	} {
		if got := windowsCallDir(c.root, c.exe, c.exeErr); got != c.want {
			t.Errorf("%s: windowsCallDir = %q, want %q", c.name, got, c.want)
		}
	}
}

// the probe runs in the folder of gitVersionDir with the filtered environment
// and no removal. Exit 0 is "supported" and is cached.
func TestAttrSourceProbeSupported(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "version", 0, "")
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)
	for range 2 {
		if got := attrSourceArgs(); !slices.Equal(got, []string{"--attr-source=" + gitEmptyTree}) {
			t.Fatalf("attrSourceArgs() = %q, want the option", got)
		}
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("%d git calls for two uses, want 1: %v", len(got), got)
	}
	checkOutside(t, "the probe", got[0], false)
}

// the probe fails with a code and `git version` works. The answer is "not
// supported" and is cached. The `git version` run is a plain version run.
func TestAttrSourceProbeNotSupported(t *testing.T) {
	calls := outsideStub(t)
	t.Setenv("CLAUSTRUM_GITSTUB_ARGEND", gitEmptyTree)
	stubAnswer(t, "version", 129, "")
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)
	for range 2 {
		if got := attrSourceArgs(); got != nil {
			t.Fatalf("attrSourceArgs() = %q, want nil", got)
		}
	}
	got := calls()
	if len(got) != 2 || !got[1].version() {
		t.Fatalf("calls = %v, want the probe and one plain version run", got)
	}
	checkOutside(t, "the probe", got[0], false)
	checkOutside(t, "the version run of the probe", got[1], true)
}

// both runs fail with a code. The answer is "unknown" and is not cached, so
// the next use runs the probe again. Mutation: cache the failure as before.
func TestAttrSourceProbeUnknownIsNotCached(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "version", 129, "")
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)
	attrSourceArgs()
	first := len(calls())
	attrSourceArgs()
	if second := len(calls()); second <= first {
		t.Errorf("the second use made no git call (%d then %d): the unknown answer was cached", first, second)
	}
	if attrSourceDone {
		t.Error("the unknown answer is stored")
	}
	// The probe works at last: its answer is stored then.
	stubAnswer(t, "version", 0, "")
	if got := attrSourceArgs(); got == nil || !attrSourceDone {
		t.Errorf("attrSourceArgs() = %q, stored %v, want the option and a stored answer", got, attrSourceDone)
	}
}

// a git that does not start gives "unknown" too, and no `git version` run
// follows.
func TestAttrSourceProbeNoGitIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)
	if got := attrSourceArgs(); got != nil {
		t.Errorf("attrSourceArgs() = %q, want nil", got)
	}
	if attrSourceDone {
		t.Error("a probe that did not start stored an answer")
	}
}

// the version gate runs in the folder of gitVersionDir with the filtered
// environment and no removal. A version caches, and so does "too old".
func TestIncludeScanGateCall(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "version", 0, "git version 2.31.0")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "git"))
	h := &shadowHost{}
	for range 2 {
		if h.includeScan() {
			t.Fatal("includeScan = true for git 2.31.0")
		}
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("%d git calls for two uses, want 1 (the too old answer is kept)", len(got))
	}
	checkOutside(t, "the version gate", got[0], false)
	if _, ok := lookupExact(got[0].env, "GIT_DIR"); !ok {
		t.Error("the version gate lost GIT_DIR: only the version run drops it")
	}
}

// output with no version is "too old" for this call and stores nothing.
// Mutation: keep the answer of a run whose output does not parse.
func TestIncludeScanGateNoVersionIsNotKept(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "version", 0, "garbage")
	h := &shadowHost{}
	for range 2 {
		if h.includeScan() {
			t.Fatal("includeScan = true for garbage")
		}
	}
	if n := len(calls()); n != 2 {
		t.Errorf("%d git calls for two uses, want 2: nothing is kept", n)
	}
	stubAnswer(t, "version", 0, "git version 2.40.0")
	for range 2 {
		if !h.includeScan() {
			t.Error("includeScan = false for git 2.40.0")
		}
	}
	if n := len(calls()); n != 3 {
		t.Errorf("%d git calls after a good version, want 3: it is kept", n)
	}
}

// excludesHome makes a home with the file .config/git/ignore and sets HOME and
// USERPROFILE to it. The tests make the config read answer nothing, so that the
// folder rules of the lookup decide.
func excludesHome(t *testing.T) (home, ignore string) {
	t.Helper()
	home = t.TempDir()
	ignore = filepath.Join(home, ".config", "git", "ignore")
	writeFile(t, ignore, "*.x\n", 0o644)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home, ignore
}

// the lookup runs in gitVersionDir with the filtered environment and
// GIT_DIR at the null device.
func TestExcludesLookupCall(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "--includes", 0, "")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "git"))
	t.Setenv("XDG_CONFIG_HOME", "")
	excludesHome(t)
	resolveUserExcludesFile()
	got := calls()
	if len(got) != 1 || !got[0].excludesRead() {
		t.Fatalf("calls = %v, want the excludes read", got)
	}
	checkOutside(t, "the excludes read", got[0], false)
	if v, _ := lookupExact(got[0].env, "GIT_DIR"); v != os.DevNull {
		t.Errorf("GIT_DIR = %q, want %q", v, os.DevNull)
	}
}

// a relative XDG_CONFIG_HOME counts as unset, so the base is the home.
func TestExcludesRelativeXDGCountsAsUnset(t *testing.T) {
	outsideStub(t)
	stubAnswer(t, "--includes", 0, "")
	_, ignore := excludesHome(t)
	t.Setenv("XDG_CONFIG_HOME", "rel")
	if got := resolveUserExcludesFile(); got != ignore {
		t.Errorf("lookup = %q, want %q", got, ignore)
	}
}

// an XDG_CONFIG_HOME that is set and absolute is the only base. With no file
// there, the answer is the null device, though the home has the file.
func TestExcludesAbsoluteXDGWithoutFileIsNullDevice(t *testing.T) {
	outsideStub(t)
	stubAnswer(t, "--includes", 0, "")
	excludesHome(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := resolveUserExcludesFile(); got != os.DevNull {
		t.Errorf("lookup = %q, want %q", got, os.DevNull)
	}
	ignore := filepath.Join(xdg, "git", "ignore")
	writeFile(t, ignore, "*.y\n", 0o644)
	if got := resolveUserExcludesFile(); got != ignore {
		t.Errorf("lookup with the file = %q, want %q", got, ignore)
	}
}

// a relative home gives a relative base, and the answer is the null device,
// also when the file is there relative to the working folder of the daemon.
func TestExcludesRelativeHomeIsNullDevice(t *testing.T) {
	outsideStub(t)
	stubAnswer(t, "--includes", 0, "")
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "rel", ".config", "git", "ignore"), "*.x\n", 0o644)
	t.Chdir(cwd)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "rel")
	t.Setenv("USERPROFILE", "rel")
	if got := resolveUserExcludesFile(); got != os.DevNull {
		t.Errorf("lookup = %q, want %q", got, os.DevNull)
	}
}

// the read of config text runs in gitVersionDir. Its environment is the
// filtered one, the daemon's GIT_CONFIG_PARAMETERS stays, and GIT_DIR, LC_ALL and
// LANGUAGE come last with their fixed values.
func TestConfigTextReadCall(t *testing.T) {
	calls := outsideStub(t)
	stubAnswer(t, "--file", 0, "")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'a.b=c'")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "git"))
	if _, err := listConfigText(t.Context(), []byte("[a]\n\tb = c\n"), gitVersionDir, repoConfigListEnv()); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 || !got[0].configRead() {
		t.Fatalf("calls = %v, want one config read", got)
	}
	c := got[0]
	if !sameFolder(c.cwd, gitVersionDir) {
		t.Errorf("the read runs in %q, want %q", c.cwd, gitVersionDir)
	}
	if _, ok := lookupExact(c.env, "GIT_SSH"); ok {
		t.Error("the read keeps the relative GIT_SSH")
	}
	for name, want := range map[string]string{
		"GIT_DIR": os.DevNull, "LC_ALL": "C", "LANGUAGE": "C",
		"GIT_CONFIG_PARAMETERS": "'a.b=c'", "GIT_CONFIG_NOSYSTEM": "1",
	} {
		if v, ok := lookupExact(c.env, name); !ok || v != want {
			t.Errorf("the read has %s=%q (set %v), want %q", name, v, ok, want)
		}
	}
}
