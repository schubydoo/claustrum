package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The configuration listing of 89cb6289 (gitlisting.go): the argv, the C locale, the
// parse, the hook pins and their limits, and the classes of a failed listing. Each
// test names the mutation that turns it red.

// envCall is one git call as the CLAUSTRUM_GITSTUB_ENVLOG of the git-slow stub records
// it: the working directory, the argv and the whole environment.
type envCall struct {
	cwd  string
	argv []string
	env  []string
}

func readEnvCalls(t *testing.T, log string) []envCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var calls []envCall
	for _, rec := range strings.Split(string(b), "\x1d\n") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1e", 3)
		if len(parts) != 3 {
			t.Fatalf("ENVLOG record %q has %d fields, want 3", rec, len(parts))
		}
		calls = append(calls, envCall{cwd: parts[0], argv: strings.Split(parts[1], "\x1f"), env: strings.Split(parts[2], "\x1f")})
	}
	return calls
}

func (c envCall) listing() bool { return argvEndsWith(c.argv, []string{"config", "-z", "--list"}) }

func (c envCall) excludesRead() bool { return slices.Contains(c.argv, "--includes") }

func (c envCall) version() bool { return slices.Equal(c.argv, []string{"version"}) }

func (c envCall) hardened() bool { return slices.Contains(c.argv, "core.hooksPath=/dev/null") }

// pins returns the KEY=VALUE pairs of the call's GIT_CONFIG_COUNT set, in index order.
func (c envCall) pins(t *testing.T) []string {
	t.Helper()
	v, _ := lookupExact(c.env, configCountName)
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("call %q: GIT_CONFIG_COUNT=%q", c.argv, v)
	}
	var out []string
	for i := range n {
		k, _ := lookupExact(c.env, configKeyPrefix+strconv.Itoa(i))
		val, _ := lookupExact(c.env, configValPrefix+strconv.Itoa(i))
		out = append(out, k+"="+val)
	}
	return out
}

// listingFixture is a repository with the git-slow stub on PATH and an ENVLOG. The
// host's global and system config are out of the way, and so is every GIT_* and
// locale variable the daemon reads.
type listingFixture struct {
	wtFixture
	log string
	s   *server
}

func newListingFixture(t *testing.T) listingFixture {
	t.Helper()
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearDaemonConfigEnv(t)
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "LC_ALL", "LANGUAGE"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	log := filepath.Join(t.TempDir(), "env.log")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	return listingFixture{wtFixture: f, log: log, s: newTestServer(t)}
}

// call dispatches method and returns the reply and the git calls it made.
func (f listingFixture) call(t *testing.T, method string, params map[string]any) (string, []envCall) {
	t.Helper()
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	raw := dispatchRaw(t, f.s, rpcLine(t, method, params))
	return raw, readEnvCalls(t, f.log)
}

// result is the result or error member of a raw reply, as JSON.
func replyPart(t *testing.T, raw string) string {
	t.Helper()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("reply %s: %v", raw, err)
	}
	if r.Error != nil {
		return string(r.Error)
	}
	return string(r.Result)
}

func errPart(t *testing.T, msg string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"code": -32603, "message": msg})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// appendConfig adds text to the repository's .git/config.
func (f listingFixture) appendConfig(t *testing.T, text string) {
	t.Helper()
	p := filepath.Join(f.top, ".git", "config")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(b)+text, 0o644)
}

func (f listingFixture) wt() string { return filepath.Join(f.top, ".claude", "worktrees", "w1") }

// fiveMethods runs the five git methods on the fixture and returns each part.
func (f listingFixture) fiveMethods(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range []struct {
		name   string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.top}},
		{"git.list_branches", map[string]any{"path": f.top}},
		{"git.status", map[string]any{"path": f.top, "baseRepo": f.top}},
		{"git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
	} {
		raw, _ := f.call(t, m.name, m.params)
		out[m.name] = replyPart(t, raw)
	}
	return out
}

// Every listing of git.info ends with `config -z --list` and has no --name-only. On
// Windows the listing of the root pair starts with --git-dir=<pin>. Mutation: put
// --name-only back.
func TestListingArgv(t *testing.T) {
	f := newListingFixture(t)
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("reply = %s", raw)
	}
	n := 0
	for _, c := range calls {
		if slices.Contains(c.argv, "config") && !c.excludesRead() {
			n++
			if !c.listing() || slices.Contains(c.argv, "--name-only") {
				t.Errorf("listing argv = %q, want it to end with config -z --list", c.argv)
			}
		}
	}
	if n == 0 {
		t.Fatalf("calls = %v, want listings", calls)
	}
}

// The listing drops the daemon's LC_ALL and LANGUAGE, keeps LANG, and
// ends with LC_ALL=C and LANGUAGE=C. The call after it keeps the daemon's three.
// Mutation: append the two C entries without removing the inherited ones.
func TestListingLocaleEnv(t *testing.T) {
	f := newListingFixture(t)
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	t.Setenv("LANG", "de_DE.UTF-8")
	_, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	listings, hardened := 0, 0
	for _, c := range calls {
		switch {
		case c.listing():
			listings++
			if runtime.GOOS != "windows" {
				// On Windows os/exec sorts the block, so the order is not checked there.
				if tail := c.env[len(c.env)-2:]; !slices.Equal(tail, []string{"LC_ALL=C", "LANGUAGE=C"}) {
					t.Errorf("listing env ends %q, want LC_ALL=C LANGUAGE=C", tail)
				}
			}
			for _, bad := range []string{"LC_ALL=de_DE.UTF-8", "LANGUAGE=de"} {
				if slices.Contains(c.env, bad) {
					t.Errorf("listing env keeps the daemon's %s", bad)
				}
			}
			if !slices.Contains(c.env, "LANG=de_DE.UTF-8") || !slices.Contains(c.env, "LC_ALL=C") || !slices.Contains(c.env, "LANGUAGE=C") {
				t.Errorf("listing env = %q, want LANG=de_DE.UTF-8, LC_ALL=C and LANGUAGE=C", c.env)
			}
		case c.hardened():
			hardened++
			for _, want := range []string{"LC_ALL=de_DE.UTF-8", "LANGUAGE=de", "LANG=de_DE.UTF-8"} {
				if !slices.Contains(c.env, want) {
					t.Errorf("hardened call %q lacks %s", c.argv, want)
				}
			}
			if slices.Contains(c.env, "LC_ALL=C") {
				t.Errorf("hardened call %q carries LC_ALL=C", c.argv)
			}
		}
	}
	if listings == 0 || hardened == 0 {
		t.Fatalf("calls = %d listings and %d hardened calls, want both", listings, hardened)
	}
	// os/exec keeps only the last of two entries with one name, so the trace cannot
	// show an inherited LC_ALL left in front of LC_ALL=C. The built env can.
	for _, kv := range precursorEnv(false, nil) {
		if kv == "LC_ALL=de_DE.UTF-8" || kv == "LANGUAGE=de" {
			t.Errorf("precursorEnv keeps the daemon's %s", kv)
		}
	}
}

// Git prints its listing error under the C locale. The stub writes its
// own LC_ALL and LANGUAGE into the error. Mutation: skip the append.
func TestListingLocaleReachesGit(t *testing.T) {
	f := newListingFixture(t)
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	slowGit(t, "config,-z,--list", "fail", 0, "fatal: bad config LC_ALL=$LC_ALL LANGUAGE=$LANGUAGE", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	t.Setenv("CLAUSTRUM_GITSTUB_EXPAND", "1")
	raw, _ := f.call(t, "git.info", map[string]any{"path": f.top})
	want := errPart(t, hooksRefusalPrefix+"exit status 128: fatal: bad config LC_ALL=C LANGUAGE=C")
	if got := replyPart(t, raw); got != want {
		t.Errorf("info = %s\nwant %s", got, want)
	}
}

// pinsOfHardened checks that every hardened call of calls carries want after the
// two base pins, and returns how many it checked.
func pinsOfHardened(t *testing.T, calls []envCall, want ...string) int {
	t.Helper()
	full := append([]string{"hook.enabled=false", "hook.event="}, want...)
	n := 0
	for _, c := range calls {
		if !c.hardened() {
			continue
		}
		n++
		if got := c.pins(t); !slices.Equal(got, full) {
			t.Errorf("call %q pins %q\nwant %q", c.argv, got, full)
		}
	}
	return n
}

// A value with a LF and a key-like text, a value that reads like
// a hook key, a key with no value and an empty value pin nothing. Only the real hook
// is pinned, on every hardened call of all five methods (row L04). Mutations: parse
// the output line by line, or treat a record without a LF as an error.
func TestListingParsePinsOnlyRealHooks(t *testing.T) {
	f := newListingFixture(t)
	f.appendConfig(t, "[x]\n\ty = \"a=b\\nhook.evil.command=1\"\n\tz = hook.foo.command\n\tflag\n\te =\n"+
		"[hook \"real\"]\n\tcommand = true\n\tevent = pre-commit\n")
	n := 0
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
		raw, calls := f.call(t, m.name, m.params)
		if strings.Contains(raw, `"error"`) {
			t.Fatalf("%s = %s", m.name, raw)
		}
		n += pinsOfHardened(t, calls, "hook.real.enabled=false", "hook.real.event=")
	}
	if n == 0 {
		t.Fatal("no hardened call seen")
	}
}

// The hook names of row L05 are pinned in the order the listing
// first shows them: the empty name, a name with a dot, a name in upper case, and a
// name under two keys once. `hook.x` names none. Mutations: sort the names, lowercase
// them, drop the empty name, or pin a name once per key.
func TestListingHookPinOrder(t *testing.T) {
	f := newListingFixture(t)
	f.appendConfig(t, "[hook \"\"]\n\tcommand = x\n[hook \"a.b\"]\n\tcommand = y\n[HOOK \"Up\"]\n\tcommand = z\n"+
		"[hook]\n\tx = 1\n[hook \"dup\"]\n\tcommand = c\n\tevent = e\n")
	_, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	if pinsOfHardened(t, calls, "hook..enabled=false", "hook..event=", "hook.a.b.enabled=false", "hook.a.b.event=",
		"hook.Up.enabled=false", "hook.Up.event=", "hook.dup.enabled=false", "hook.dup.event=") == 0 {
		t.Fatal("no hardened call seen")
	}
}

// The daemon's own GIT_CONFIG_COUNT pair stays in front of the hook
// pins, and the count covers all of them. Mutation: put the hook pins before the
// inherited pair.
func TestConfigPinEnvInheritedPairFirst(t *testing.T) {
	daemon := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y"}
	got := configPinEnv(daemon, 1, []string{"n"})
	want := []string{"GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y",
		"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=hook.n.enabled", "GIT_CONFIG_VALUE_3=false", "GIT_CONFIG_KEY_4=hook.n.event", "GIT_CONFIG_VALUE_4="}
	if !slices.Equal(got, want) {
		t.Errorf("configPinEnv = %q\nwant %q", got, want)
	}
}

// listingOf builds the -z output of keys, each with the value "v".
func listingOf(keys ...string) []byte {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\nv\x00")
	}
	return []byte(b.String())
}

func hookKeys(n, size int) []string {
	var keys []string
	for i := range n {
		name := strconv.Itoa(i)
		name += strings.Repeat("h", size-len(name))
		keys = append(keys, "hook."+name+".command")
	}
	return keys
}

// The limits at the parser: the key cap, no value cap, the name count and the byte
// sum. Mutations: cap the key at 1048575 or 1048577, cap the value, test the count
// with >=, count key bytes or add 1 per name.
func TestParseConfigListingLimits(t *testing.T) {
	key := func(n int) string { return "x." + strings.Repeat("s", n-4) + ".y" }
	for _, tc := range []struct {
		name string
		out  []byte
		want string
	}{
		{"key of 1048576 bytes", listingOf(key(1 << 20)), ""},
		{"key of 1048577 bytes", listingOf(key(1<<20 + 1)), "a configuration key is longer than the listing reads"},
		{"value of 2 MiB", []byte("x.big\n" + strings.Repeat("v", 2<<20) + "\x00"), ""},
		{"1024 names", listingOf(hookKeys(1024, 5)...), ""},
		{"1025 names", listingOf(hookKeys(1025, 5)...), "too many configured hooks to pin"},
		{"64 names of 1024 bytes", listingOf(hookKeys(64, 1024)...), ""},
		{"63 names of 1024 bytes and one of 1025", listingOf(append(hookKeys(63, 1024), "hook."+strings.Repeat("z", 1025)+".command")...),
			"configured hook names exceed the aggregate pin byte bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := parseConfigListing(tc.out); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// Through git.info, a key of 1048576 bytes and a value of 2 MiB
// answer normally. A key of 1048577 bytes refuses with the listing text, and no
// hardened call runs (row L08c).
func TestListingKeyCapThroughInfo(t *testing.T) {
	sub := func(n int) string { return strings.Repeat("s", n-4) }
	for _, tc := range []struct {
		name, config string
		refused      bool
	}{
		{"key 1048576", "[x \"" + sub(1<<20) + "\"]\n\ty = 1\n", false},
		{"key 1048577", "[x \"" + sub(1<<20+1) + "\"]\n\ty = 1\n", true},
		{"value 2 MiB", "[x]\n\tbig = " + strings.Repeat("v", 2<<20) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListingFixture(t)
			f.appendConfig(t, tc.config)
			raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
			if !tc.refused {
				if !strings.Contains(raw, `"isRepo":true`) {
					t.Errorf("info = %s, want the normal answer", raw)
				}
				return
			}
			want := errPart(t, hooksRefusalPrefix+"a configuration key is longer than the listing reads")
			if got := replyPart(t, raw); got != want {
				t.Errorf("info = %s\nwant %s", got, want)
			}
			for _, c := range calls {
				if c.hardened() {
					t.Errorf("hardened call %q ran after the refusal", c.argv)
				}
			}
		})
	}
}

// 1025 hook names refuse with the hooks prefix only, and no hardened call
// runs (row L06b).
func TestListingTooManyHooksThroughInfo(t *testing.T) {
	f := newListingFixture(t)
	var b strings.Builder
	for i := range 1025 {
		b.WriteString("[hook \"h" + strconv.Itoa(10000 + i)[1:] + "\"]\n\tcommand = x\n")
	}
	f.appendConfig(t, b.String())
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	if got, want := replyPart(t, raw), errPart(t, hooksPinPrefix+"too many configured hooks to pin"); got != want {
		t.Errorf("info = %s\nwant %s", got, want)
	}
	for _, c := range calls {
		if c.hardened() {
			t.Errorf("hardened call %q ran after the refusal", c.argv)
		}
	}
}

// stubListing makes every listing fail with exit code and the stderr text.
func stubListing(t *testing.T, code int, stderr string) {
	t.Helper()
	slowGit(t, "config,-z,--list", "fail", 0, stderr, "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", strconv.Itoa(code))
}

// A listing that exits 128 and says "fatal: not a git
// repository", in any letter case and after leading white space, gives the "no
// repository" shape of each method (rows L14a, L14b and L14d). Mutations: drop the
// class check, compare case-sensitively, keep the leading white space.
func TestListingNotARepository(t *testing.T) {
	for _, tc := range []struct{ name, stderr string }{
		{"L14a", "fatal: not a git repository (stub)"},
		{"L14b", "FATAL: NOT A GIT REPOSITORY"},
		{"L14d", strings.Repeat(`\n `, 40) + "fatal: not a git repository (stub)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListingFixture(t)
			stubListing(t, 128, tc.stderr)
			got := f.fiveMethods(t)
			for m, want := range map[string]string{
				"git.info":            notRepoInfo,
				"git.list_branches":   notRepoList,
				"git.status":          notRepoStatus,
				"git.worktree_create": notRepoCreate,
				"git.worktree_remove": `{"success":true,"branchKept":true}`,
			} {
				if got[m] != want {
					t.Errorf("%s = %s\nwant %s", m, got[m], want)
				}
			}
			if exists(f.wt()) {
				t.Errorf("create left %s", f.wt())
			}
		})
	}
}

// Exit 1 with the same text is not that class. `git version` works, so
// the answer is the listing refusal (row L14c). Mutation: ignore the exit code.
func TestListingNotARepositoryNeedsExit128(t *testing.T) {
	f := newListingFixture(t)
	stubListing(t, 1, "fatal: not a git repository (stub)")
	raw, _ := f.call(t, "git.info", map[string]any{"path": f.top})
	want := errPart(t, hooksRefusalPrefix+"exit status 1: fatal: not a git repository (stub)")
	if got := replyPart(t, raw); got != want {
		t.Errorf("info = %s\nwant %s", got, want)
	}
}

// git.worktree_remove on that listing runs two listings in baseRepo, the
// first with the heavy profile and the second with the light one of the branch step,
// and no other git call. Then it answers branchKept. Mutations: one listing, or run
// for-each-ref after the second.
func TestListingNotARepositoryRemoveTrace(t *testing.T) {
	f := newListingFixture(t)
	stubListing(t, 128, "fatal: not a git repository (stub)")
	raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"})
	if got := replyPart(t, raw); got != `{"success":true,"branchKept":true}` {
		t.Errorf("remove = %s", got)
	}
	var rest []envCall
	for _, c := range calls {
		if !c.excludesRead() {
			rest = append(rest, c)
		}
	}
	if len(rest) != 2 || !rest[0].listing() || !rest[1].listing() {
		var argv [][]string
		for _, c := range rest {
			argv = append(argv, c.argv)
		}
		t.Fatalf("calls = %q, want two listings and nothing else", argv)
	}
	if !slices.Contains(rest[0].env, "GIT_NO_LAZY_FETCH=1") || slices.Contains(rest[1].env, "GIT_NO_LAZY_FETCH=1") ||
		!slices.Contains(rest[1].env, "GIT_NO_REPLACE_OBJECTS=1") {
		t.Errorf("listing profiles wrong: first heavy=%v, second light=%v",
			slices.Contains(rest[0].env, "GIT_NO_LAZY_FETCH=1"), slices.Contains(rest[1].env, "GIT_NO_REPLACE_OBJECTS=1"))
	}
}

// versionDir is where `git version` runs after a failed listing.
func versionDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "/"
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// When every command exits 1 with "boom", `git version` fails too, so all
// five methods answer that git cannot run (row L11a). The listing and the version
// call give the same detail here. The version call runs in the root directory
// (Windows: the daemon's working directory). Mutation: skip the probe.
func TestListingGitCannotRun(t *testing.T) {
	f := newListingFixture(t)
	stubListing(t, 1, "boom")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "version")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "boom")
	msg := gitCannotRunPrefix + "exit status 1: boom"
	got := f.fiveMethods(t)
	create, _ := json.Marshal(worktreeResult{Success: false, Error: msg, ErrorCode: "worktree_add_failed"})
	remove, _ := json.Marshal(worktreeRemoveResult{Success: false,
		Error: "failed to remove worktree: could not check whether " + f.wt() + " is locked (" + msg + "); retry"})
	for m, want := range map[string]string{
		"git.info":            errPart(t, msg),
		"git.list_branches":   errPart(t, msg),
		"git.status":          errPart(t, msg),
		"git.worktree_create": string(create),
		"git.worktree_remove": string(remove),
	} {
		if got[m] != want {
			t.Errorf("%s = %s\nwant %s", m, got[m], want)
		}
	}
	_, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	i := slices.IndexFunc(calls, envCall.version)
	if i < 1 || !calls[i-1].listing() {
		t.Fatalf("calls = %v, want git version right after the listing", calls)
	}
	if canonicalPath(calls[i].cwd) != canonicalPath(versionDir(t)) {
		t.Errorf("git version runs in %s, want %s", calls[i].cwd, versionDir(t))
	}
}

// When only the listing fails, `git version` works and the
// answer stays the listing refusal (row L10). The version call carries the env of the
// failed listing without the GIT_COMMON_DIR pin, the two C entries and the
// GIT_CONFIG_GLOBAL of the fixture (cell C-c, Linux and macOS VMs). Mutations: answer "cannot
// run" whenever the listing fails, keep the pin.
func TestListingFailsVersionWorks(t *testing.T) {
	f := newListingFixture(t)
	stubListing(t, 1, "boom")
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	if got, want := replyPart(t, raw), errPart(t, hooksRefusalPrefix+"exit status 1: boom"); got != want {
		t.Errorf("info = %s\nwant %s", got, want)
	}
	i := slices.IndexFunc(calls, envCall.version)
	if i < 1 || !calls[i-1].listing() {
		t.Fatalf("calls = %v, want git version right after the listing", calls)
	}
	var want []string
	for _, kv := range calls[i-1].env {
		if !strings.HasPrefix(kv, "GIT_COMMON_DIR=") && kv != "LC_ALL=C" && kv != "LANGUAGE=C" &&
			envName(kv) != "GIT_CONFIG_GLOBAL" {
			want = append(want, kv)
		}
	}
	if len(want) != len(calls[i-1].env)-4 {
		t.Fatalf("listing env = %q, want the pin, the two C entries and GIT_CONFIG_GLOBAL in it", calls[i-1].env)
	}
	if !sameShapeEnv(calls[i].env, want) {
		t.Errorf("version env = %q\nwant %q", calls[i].env, want)
	}
}

// With no git on PATH, each method answers its "no repository" shape. Row L13
// measured git.info on three VMs. A Linux VM measured the other methods. A remove of
// a gone worktree keeps the branch (row L13xb). Mutation: let the exec error reach the
// listing refusal. git.worktree_remove with worktreeRoot is not covered here
// (TestWorktreeRemoveExternalNoGitOnPath).
func TestListingNoGitOnPath(t *testing.T) {
	f := newWTFixture(t, false)
	resetUserExcludesCache(t)
	t.Setenv("PATH", t.TempDir())
	wantResult(t, "info", info(t, f.top), notRepoInfo)
	wantResult(t, "list_branches", listBranches(t, f.top), notRepoList)
	wantResult(t, "status", status(t, f.top, f.top), notRepoStatus)
	c, wt := create(t, f.top)
	wantResult(t, "create", c, notRepoCreate)
	wantResult(t, "remove, worktree gone", remove(t, f.top, wt, "w1"), `{"success":true,"branchKept":true}`)
	// Row L13xa: a worktree folder that exists gets the lock-check refusal and stays.
	// Mutation: skip the configuration check of the remove when PATH holds no git.
	keep := filepath.Join(wt, "keep.txt")
	writeFile(t, keep, "k\n", 0o644)
	present, err := json.Marshal(worktreeRemoveResult{Success: false, Error: lockCheckRefusal(wt)})
	if err != nil {
		t.Fatal(err)
	}
	wantResult(t, "remove, worktree present", remove(t, f.top, wt, "w1"), string(present))
	if !exists(keep) {
		t.Errorf("the refused remove deleted %s", keep)
	}
}
