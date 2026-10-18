//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The call shape of the five git methods in the three states of a request.
// They are the state shadow, a request with "shadowDirOff":true, and the state
// fallback-git. Each row is one call. It has the option list, the argv after
// the list, the working directory and the variables that the daemon adds. The
// variables are in the order of the environment. The rows of the state shadow
// are those of 5fd08069 without the member on a Linux VM (val-s3p2b1-linux
// cells A1, A3-create-plain and A4-remove-branch). The rows with the member
// are those of 5fd08069 with it (the same cells). The state fallback-git has the variables of the
// member on the read methods (s3p2-oldgit section 5, Linux VM). In that state
// the light calls of a create and the listings of the branch step carry six
// values, and the short-lived folders are in the folder of the daemon
// (val-s3p2b2-1-macos cell S4-wt1-first-status, macOS VM, and val-s3p2b2-3-windows
// cell Q-S4c, Windows VM). Those two call logs sort the variables, so the order
// of the six values in that state is not measured.
//
// The order of the variables is measured on Linux and macOS VMs only, so this
// file has the unix tag. On Windows Go sorts the environment block (D23).
//
// The short forms of the rows:
//
//	{A}  GIT_NO_LAZY_FETCH=1 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_ASKPASS= GIT_TERMINAL_PROMPT=0
//	{A2} GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null GIT_NO_LAZY_FETCH=1 GIT_ASKPASS=
//	{C}  GIT_ALLOW_PROTOCOL=https:ssh GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null
//	{D}  GIT_COMMON_DIR=<R> GIT_OBJECT_DIRECTORY=<repo>/.git/objects
//	{W}  GIT_DIR=<repo>/.git GIT_WORK_TREE=<repo>
//	{G}  GIT_COMMON_DIR=<repo>/.git
//	{H}  GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=hook.enabled GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=hook.event GIT_CONFIG_VALUE_1=
//	{L}  LC_ALL=C LANGUAGE=C
//
// <S> is the folder of the daemon, and <R> the folder of the repository in it.
// <wt> is the linked worktree of the fixture, and <leaf> the new worktree.
// <tmp> is the temporary folder, and <cwd> the working directory of the daemon.
// <n> is a decimal number, and <id> a commit id.

const (
	shapeA  = "GIT_NO_LAZY_FETCH=1 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_ASKPASS= GIT_TERMINAL_PROMPT=0"
	shapeA2 = "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null GIT_NO_LAZY_FETCH=1 GIT_ASKPASS="
	shapeC  = "GIT_ALLOW_PROTOCOL=https:ssh GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null"
	shapeD  = "GIT_COMMON_DIR=<R> GIT_OBJECT_DIRECTORY=<repo>/.git/objects"
	shapeW  = "GIT_DIR=<repo>/.git GIT_WORK_TREE=<repo>"
	shapeG  = "GIT_COMMON_DIR=<repo>/.git"
	shapeH  = "GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=hook.enabled GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=hook.event GIT_CONFIG_VALUE_1="
	shapeL  = "LC_ALL=C LANGUAGE=C"
	// shapeRM is the order of the three calls of the branch step of a remove:
	// %s is the place of the folder variables.
	shapeRM = "GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null %s {H} GIT_NO_LAZY_FETCH=1 GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_ASKPASS="
)

// shapeDaemonNames are the variables that the daemon sets on a git call. The
// rows show these, and a variable of the host of the test stays out.
var shapeDaemonNames = append(slices.Clone(daemonGitKeys), "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE", "LC_ALL", "LANGUAGE")

// shapeRow is one call, with its variables in each state. An empty text for a
// state means that the call does not run in that state.
type shapeRow struct {
	list, argv, cwd       string
	shadow, off, fallback string
}

// same is a row whose variables are equal with the member and in the state
// fallback-git.
func same(list, argv, cwd, shadow, other string) shapeRow {
	return shapeRow{list: list, argv: argv, cwd: cwd, shadow: shadow, off: other, fallback: other}
}

// shapeState is one of the three states of a request.
type shapeState int

const (
	stateShadow shapeState = iota
	stateOff
	stateFallback
)

func (s shapeState) String() string {
	return [...]string{"shadow", "with the member", "fallback-git"}[s]
}

func (r shapeRow) vars(s shapeState) string {
	return [...]string{r.shadow, r.off, r.fallback}[s]
}

// shapeFixture is the seeded fixture of one state, with the paths that the rows
// mask.
type shapeFixture struct {
	shadowFixture
	state shapeState
	leaf  string
}

func newShapeFixture(t *testing.T, state shapeState) shapeFixture {
	t.Helper()
	f := newSeededShadowFixture(t)
	// go test hands the test binary GIT_TERMINAL_PROMPT=0, and a host can export
	// others. Take every variable that the daemon sets out of the environment of
	// the test, so that the stub sees what the daemon adds.
	for _, k := range shapeDaemonNames {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	empty := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	resetAttr := func() {
		attrSourceOnce = sync.Once{}
		attrSourceOK = false
	}
	resetAttr()
	t.Cleanup(resetAttr)
	if state == stateFallback {
		h := f.s.gitShadow
		h.mu.Lock()
		h.state = controlFallback
		h.mu.Unlock()
	}
	return shapeFixture{shadowFixture: f, state: state, leaf: filepath.Join(f.repo, ".claude", "worktrees", "w1")}
}

// request sends one request in the state of the fixture and returns the reply and
// the calls as rows.
func (f shapeFixture) request(t *testing.T, method string, params map[string]any) (string, []string) {
	t.Helper()
	if f.state == stateOff {
		params["shadowDirOff"] = true
	}
	raw, calls := f.call(t, method, params)
	var lines []string
	for _, c := range calls {
		lines = append(lines, f.line(t, c))
	}
	return raw, lines
}

var (
	shapeNumber = regexp.MustCompile(`-[0-9]{6,}`)
	shapeCommit = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
)

// line is one call as a row: "<list> | <argv> | <cwd> | <variables>".
func (f shapeFixture) line(t *testing.T, c envCall) string {
	t.Helper()
	argv := c.argv
	attr := ""
	if len(argv) > 0 && strings.HasPrefix(argv[0], "--attr-source=") && len(argv) > 1 && argv[1] == "-c" {
		attr, argv = "ATTR+", argv[1:]
	}
	var items []string
	for len(argv) > 1 && argv[0] == "-c" {
		items, argv = append(items, argv[1]), argv[2:]
	}
	fill := func(list []string) []string {
		out := slices.Clone(list)
		for i := range out {
			out[i] = strings.Replace(out[i], "%s", "/dev/null", 1)
		}
		return out
	}
	p20, p28 := fill(shapeList20), fill(shapeList28)
	list := "?" + strings.Join(items, " ")
	lfs := "lfs.storage=" + filepath.Join(f.gitDir, "lfs")
	switch {
	case len(items) == 0:
		list = "none"
	case slices.Equal(items, p28):
		list = "P28"
	case slices.Equal(items, p20):
		list = "P20"
	case slices.Equal(items, append(slices.Clone(p20), "core.commitGraph=false")):
		list = "P21"
	case slices.Equal(items, append(slices.Clone(p20), "core.splitIndex=false", "core.commitGraph=false")):
		list = "P22"
	case slices.Equal(items, append(append([]string{lfs}, p20...), "core.splitIndex=false", "core.commitGraph=false")):
		list = "lfs.storage=<repo>/.git/lfs+P22"
	}
	var vars []string
	for _, kv := range c.env {
		if slices.Contains(shapeDaemonNames, envName(kv)) {
			vars = append(vars, kv)
		}
	}
	text := attr + list + " | " + strings.Join(argv, " ") + " | " + c.cwd + " | " + strings.Join(vars, " ")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	masks := []string{}
	if names := daemonFolders(t, f.run); len(names) == 1 {
		dir := filepath.Join(f.run, shadowRootName, names[0])
		masks = append(masks, filepath.Join(dir, shadowRepoName(f.gitDir)), "<R>", dir, "<S>")
	}
	masks = append(masks, f.leaf, "<leaf>", f.wt, "<wt>", f.repo, "<repo>", cwd, "<cwd>",
		// The temporary folder has no trailing separator in the mask. On macOS
		// TMPDIR ends with a slash, and a mask with it took the separator of
		// the row away (val-s3p2b2-3-macos part C).
		filepath.Clean(resolveTestRoot(t, os.TempDir())), "<tmp>", filepath.Clean(os.TempDir()), "<tmp>")
	text = strings.NewReplacer(masks...).Replace(text)
	text = shapeNumber.ReplaceAllString(text, "-<n>")
	text = shapeCommit.ReplaceAllString(text, "<id>")
	return strings.NewReplacer(shapeA, "{A}", shapeA2, "{A2}", shapeC, "{C}", shapeD, "{D}", shapeW, "{W}",
		shapeG, "{G}", shapeH, "{H}", shapeL, "{L}").Replace(text)
}

// check compares the calls of one request with the rows of its state.
func (f shapeFixture) check(t *testing.T, what string, got []string, rows []shapeRow) {
	t.Helper()
	var want []string
	for _, r := range rows {
		if v := r.vars(f.state); v != "" {
			want = append(want, strings.TrimRight(r.list+" | "+r.argv+" | "+r.cwd+" | "+strings.TrimPrefix(v, "none"), " ")+"")
		}
	}
	for i := range got {
		got[i] = strings.TrimRight(got[i], " ")
	}
	if slices.Equal(got, want) {
		return
	}
	t.Errorf("%s, state %s: %d calls, want %d", what, f.state, len(got), len(want))
	for i := 0; i < max(len(got), len(want)); i++ {
		g, w := "(no call)", "(no call)"
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Errorf("call %d\n got  %s\n want %s", i+1, g, w)
		}
	}
}

// pair is a listing and the hardened call after it, in one folder, with the
// same group and folder variables in each state.
func pair(list, argv, cwd, shadow, other string) []shapeRow {
	return []shapeRow{
		same("none", "config -z --list", cwd, shadow+" {L}", other+" {L}"),
		same(list, argv, cwd, shadow+" {H}", other+" {H}"),
	}
}

func rows(parts ...[]shapeRow) []shapeRow {
	return slices.Concat(parts...)
}

// The four pairs of git.info and the two of git.list_branches. In every state
// they have the list of 28 items and the six heavy values (s3-linux cell B0,
// Linux VM). In the state shadow the folder variables
// are {D} {W}, else {G}.
func infoRows() []shapeRow {
	p := func(argv string) []shapeRow { return pair("P28", argv, "<repo>", "{A} {D} {W}", "{A} {G}") }
	return rows(p("rev-parse --git-dir"), p("remote get-url origin"),
		p("symbolic-ref refs/remotes/origin/HEAD"), p("branch --show-current"))
}

func listBranchesRows() []shapeRow {
	p := func(argv string) []shapeRow { return pair("P28", argv, "<repo>", "{A} {D} {W}", "{A} {G}") }
	return rows(p("rev-parse --git-dir"), p("for-each-ref --format=%(refname:short) --sort=refname refs/heads/"))
}

// The 21 calls of a first git.status on a linked worktree (val-s3p2b1-linux
// cell A1). The first two have no --git-dir= and carry {D} {W} in the state
// shadow. The 19 others have one and carry {D} only. The temporary git folder
// is <S>/.gitdir-<n> in the state shadow, and <tmp>/claude-ssh-gitdir-<n> with
// the member (row D5 of that round).
func statusRows() []shapeRow {
	out := rows(
		pair("P28", "rev-parse --absolute-git-dir", "<repo>", "{A} {D} {W}", "{A} {G}"),
		pair("P28", "--git-dir=<repo>/.git --work-tree=<repo> rev-parse --show-toplevel", "<repo>/.git", "{A} {D}", "{A} {G}"),
		[]shapeRow{same("none", "--attr-source=<id> version", "<cwd>", "none", "none")},
	)
	// The listing before the rev-parse pair has --git-dir, so it is written out.
	out[2].argv = "--git-dir=<repo>/.git config -z --list"
	for _, verb := range []struct {
		argv  string
		locks bool
	}{
		{"status --porcelain --untracked-files=all --ignore-submodules=all", true},
		{"ls-files -s -z", true},
		{"rev-parse --verify -q HEAD^{commit}", false},
		{"diff-index --cached --raw -z --ignore-submodules=none HEAD --", true},
	} {
		locks := ""
		if verb.locks {
			locks = " GIT_OPTIONAL_LOCKS=0"
		}
		out = append(out,
			same("none", "--git-dir=<repo>/.git config -z --list", "<cwd>", "{A} {D} {L}", "{A} {G} {L}"),
			same("P28", "--git-dir=<repo>/.git hash-object -t tree /dev/null", "<cwd>", "{A} {D} {H}", "{A} {G} {H}"),
			shapeRow{list: "none", cwd: "<wt>",
				argv: "--git-dir=%s config -z --list", shadow: "{A} {D} {L}", off: "{A} {G} {L}", fallback: "{A} {G} {L}"},
			shapeRow{list: "ATTR+P28", cwd: "<wt>",
				argv:   "--git-dir=%s --work-tree=<wt> " + verb.argv,
				shadow: "{A} {D} {H}" + locks, off: "{A} {G} {H}" + locks, fallback: "{A} {G} {H}" + locks},
		)
	}
	return out
}

// statusRowsFor fills the temporary git folder of the state into statusRows.
func statusRowsFor(state shapeState) []shapeRow {
	dir := "<S>/.gitdir-<n>"
	if state == stateOff {
		dir = "<tmp>/claude-ssh-gitdir-<n>"
	}
	out := statusRows()
	for i := range out {
		if strings.Contains(out[i].argv, "%s") {
			out[i].argv = fmt.Sprintf(out[i].argv, dir)
		}
	}
	return out
}

// The calls of a plain git.worktree_create in a repository that has a linked
// worktree. The rows of 5fd08069 are cell 50a of s3p2b-edges-linux and
// val-s3p2b1-linux cell A3-create-plain.
//
// Two points differ from the 13 calls of 5fd08069 in the state shadow:
//
//   - 5fd08069 runs the --show-toplevel pair after `rev-parse
//     --absolute-git-dir`, with and without the member. claustrum does not.
//   - claustrum runs its own --show-scope listing before the two listings of
//     the read-tree call. It reads the repository's own config, so it carries
//     {G}. With the member the filter rule is off, and the listing does not run.
func createRows() []shapeRow {
	const leaf = "<leaf>"
	index := map[bool]string{true: " GIT_INDEX_FILE=<S>/.index-<n>/index", false: " GIT_INDEX_FILE=<tmp>/claude-ssh-index-<n>/index"}
	// light is a light pair outside the new worktree: six values in the states
	// shadow and fallback-git, four with the member.
	light := func(argv string) []shapeRow {
		out := pair("P20", argv, "<repo>", "{A2} {D} {W}", "{C} {G}")
		out[0].fallback, out[1].fallback = "{A2} {G} {L}", "{A2} {G} {H}"
		return out
	}
	return rows(
		light("rev-parse --is-inside-work-tree"),
		light("rev-parse --abbrev-ref HEAD"),
		pair("P28", "rev-parse --absolute-git-dir", "<repo>", "{A} {D} {W}", "{A} {G}"),
		light("worktree add --no-track --no-checkout -b w1 <leaf>"),
		[]shapeRow{
			{list: "none", argv: "--git-dir=<repo>/.git config -z --list --show-scope", cwd: leaf,
				shadow: "{C} {G} {L}", fallback: "{C} {G} {L}"},
			same("none", "--git-dir=<repo>/.git config -z --list", leaf, "{C} {D} {L}", "{C} {G} {L}"),
			{list: "none", argv: "--git-dir=<repo>/.git config -z --list", cwd: leaf,
				shadow: "{C} {D} {H}" + index[true] + " LANGUAGE=C"},
			{list: "lfs.storage=<repo>/.git/lfs+P22", cwd: leaf,
				argv:   "--git-dir=<repo>/.git --work-tree=<leaf> read-tree -u --reset --no-recurse-submodules refs/heads/w1",
				shadow: "{C} {D} {H}" + index[true] + " LANGUAGE=C"},
			{list: "P22", cwd: leaf,
				argv: "--git-dir=<repo>/.git --work-tree=<leaf> read-tree -u --reset --no-recurse-submodules refs/heads/w1",
				off:  "{C} {G} {H}" + index[false], fallback: "{C} {G} {H}" + index[true]},
		},
	)
}

// The 8 calls of a git.worktree_remove with branchName (cell 45 of
// s3p2b-edges2-linux and val-s3p2b1-linux cell A4-remove-branch). The three
// listings of the branch step carry the six
// values in the state shadow and the four with the member (row N2).
func removeRows() []shapeRow {
	step := func(list, argv string) []shapeRow {
		listing := same("none", "config -z --list", "<repo>", "{A2} {D} {W} {L}", "{C} {G} {L}")
		listing.fallback = "{A2} {G} {L}"
		return []shapeRow{
			listing,
			same(list, argv, "<repo>", fmt.Sprintf(shapeRM, "{D} {W}"), fmt.Sprintf(shapeRM, "{G}")),
		}
	}
	return rows(
		pair("P28", "rev-parse --absolute-git-dir", "<repo>", "{A} {D} {W}", "{A} {G}"),
		step("P20", "for-each-ref --count=10001 --format=%(objectname)%00%(refname)%00%(symref) refs/heads/"),
		step("P21", "rev-list -n 1 <id> --not --remotes --not --stdin --"),
		step("P20", "update-ref --no-deref -d refs/heads/w1 <id>"),
	)
}

// TestGitCallShapePerState pins every call of the five methods in each of the
// three states, as the rows above give them.
func TestGitCallShapePerState(t *testing.T) {
	requireGit(t)
	for _, state := range []shapeState{stateShadow, stateOff, stateFallback} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			// The first request reads the user's excludes file and, without the
			// member, the config of the repository. Both are checked here and stay
			// out of the rows.
			raw, got := f.request(t, "git.info", map[string]any{"path": f.repo})
			if !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			first := []string{"none | config --includes --path core.excludesFile | <tmp> | GIT_DIR=/dev/null"}
			if state == stateShadow {
				// The call about the repository, then the read of the config. The
				// ceiling of the call is in TestProbeCallShape.
				first = append(first,
					"?core.hooksPath=/dev/null core.fsmonitor=false | rev-parse --git-dir | <repo> | GIT_COMMON_DIR=<S>/.probe GIT_OBJECT_DIRECTORY=<S>/.probe/objects GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 {L}",
					"none | config --no-includes --file - --list -z | / | GIT_DIR=/dev/null {L}")
			}
			if len(got) < len(first) || !slices.Equal(got[:len(first)], first) {
				t.Fatalf("the first calls of the first request = %q\nwant %q", got[:min(len(got), len(first))], first)
			}
			f.check(t, "git.info", got[len(first):], infoRows())

			_, got = f.request(t, "git.list_branches", map[string]any{"path": f.repo})
			f.check(t, "git.list_branches", got, listBranchesRows())

			raw, got = f.request(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
			if !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.status = %s", raw)
			}
			f.check(t, "git.status", got, statusRowsFor(state))

			raw, got = f.request(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf})
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			f.check(t, "git.worktree_create", got, createRows())

			raw, got = f.request(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf, "branchName": "w1"})
			if raw != removeOK {
				t.Fatalf("git.worktree_remove = %s", raw)
			}
			f.check(t, "git.worktree_remove", got, removeRows())
		})
	}
}

// TestShadowVersionCallAfterAFailedListing pins the `version` call that follows
// a first listing which fails. In a create it carries the six values in the
// order of the light calls of the state shadow. With the member it carries the
// four values (val-s3p2b1-linux table A7 and row N10, Linux VM). In git.info it
// carries the six heavy values in both. The call has no folder variable, no
// hook pin and no LC_ALL.
func TestShadowVersionCallAfterAFailedListing(t *testing.T) {
	for _, state := range []shapeState{stateShadow, stateOff} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			if raw, _ := f.request(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			slowGit(t, "config,-z,--list", "fail", 0, "fatal: the stub of this cell answers exit 128\n", "")
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			const text = `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: fatal: the stub of this cell answers exit 128`
			raw, got := f.request(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf})
			if !strings.Contains(raw, `"error":"`+text+`","errorCode":"worktree_add_failed"`) {
				t.Errorf("git.worktree_create = %s", raw)
			}
			f.check(t, "git.worktree_create", got, []shapeRow{
				same("none", "config -z --list", "<repo>", "{A2} {D} {W} {L}", "{C} {G} {L}"),
				same("none", "version", "/", "{A2}", "{C}"),
			})
			raw, got = f.request(t, "git.info", map[string]any{"path": f.repo})
			if !strings.Contains(raw, `"message":"`+text+`"`) {
				t.Errorf("git.info = %s", raw)
			}
			f.check(t, "git.info", got, []shapeRow{
				same("none", "config -z --list", "<repo>", "{A} {D} {W} {L}", "{A} {G} {L}"),
				same("none", "version", "/", "{A}", "{A}"),
			})
		})
	}
}

// TestShadowFilterRuleWithAnOldGit pins the listings of the checkout in the
// state shadow for a git that refuses --show-scope. The filter rule then runs
// two plain listings, and the second reads the repository's own config ({G}).
// It is not the listing of the checkout in that state: the two listings
// against the copy follow it, as on 5fd08069 (val-s3p2b1-linux rows N3 and N4).
// The three listings of the rule are claustrum's own calls.
func TestShadowFilterRuleWithAnOldGit(t *testing.T) {
	f := newShapeFixture(t, stateShadow)
	if raw, _ := f.request(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "noscope")
	raw, got := f.request(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	at := slices.IndexFunc(got, func(l string) bool { return strings.Contains(l, "--show-scope") })
	if at < 0 {
		t.Fatalf("calls = %q, want the scoped listing", got)
	}
	const index = " GIT_INDEX_FILE=<S>/.index-<n>/index LANGUAGE=C"
	f.check(t, "the checkout of git.worktree_create", got[at:], []shapeRow{
		{list: "none", argv: "--git-dir=<repo>/.git config -z --list --show-scope", cwd: "<leaf>", shadow: "{C} {G} {L}"},
		{list: "none", argv: "config -z --list", cwd: "<leaf>", shadow: "{C} {L} GIT_DIR=/dev/null"},
		{list: "none", argv: "--git-dir=<repo>/.git config -z --list", cwd: "<leaf>", shadow: "{C} {G} {L}"},
		{list: "none", argv: "--git-dir=<repo>/.git config -z --list", cwd: "<leaf>", shadow: "{C} {D} {L}"},
		{list: "none", argv: "--git-dir=<repo>/.git config -z --list", cwd: "<leaf>", shadow: "{C} {D} {H}" + index},
		{list: "lfs.storage=<repo>/.git/lfs+P22", cwd: "<leaf>",
			argv:   "--git-dir=<repo>/.git --work-tree=<leaf> read-tree -u --reset --no-recurse-submodules refs/heads/w1",
			shadow: "{C} {D} {H}" + index},
	})
}

// TestShadowTopLevelPairOfARemove pins the variables of the --show-toplevel
// pair of a git.worktree_remove, per state. In the state shadow both calls
// carry {D} with no GIT_DIR and no GIT_WORK_TREE, and with the member {G}
// (val-s3p2b1-linux table A6, Linux VM). The fixture is a folder under
// .claude/worktrees that no registration names: the pair runs there before the
// look at the registrations.
func TestShadowTopLevelPairOfARemove(t *testing.T) {
	for _, state := range []shapeState{stateShadow, stateOff} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			if raw, _ := f.request(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			stray := filepath.Join(f.repo, ".claude", "worktrees", "stray")
			if err := os.Mkdir(stray, 0o755); err != nil {
				t.Fatal(err)
			}
			raw, got := f.request(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": stray})
			if raw != removeOK {
				t.Fatalf("git.worktree_remove = %s", raw)
			}
			vars := map[shapeState]string{stateShadow: "{A} {D}", stateOff: "{A} {G}"}[state]
			want := []string{
				"none | --git-dir=<repo>/.git config -z --list | <repo>/.git | " + vars + " {L}",
				"P28 | --git-dir=<repo>/.git --work-tree=<repo> rev-parse --show-toplevel | <repo>/.git | " + vars + " {H}",
			}
			at := slices.Index(got, want[0])
			if at < 0 || at+1 >= len(got) || got[at+1] != want[1] {
				t.Errorf("calls = %q\nwant the pair %q", got, want)
			}
		})
	}
}

// TestShadowBothAddsNameTheCopy pins the variables of the two `worktree
// add` calls of a create with existingBranch whose attach add fails. In the
// state shadow both carry GIT_COMMON_DIR=<R> with the three other folder
// variables, as on 5fd08069 on a Linux VM (val-s3p2b1-linux cell
// B2-create-existing-checked-out), and the new worktree names its registration
// in the repository.
func TestShadowBothAddsNameTheCopy(t *testing.T) {
	f := newShapeFixture(t, stateShadow)
	if raw, _ := f.request(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	// The branch wr is checked out in the linked worktree of the fixture, so git
	// refuses the attach add.
	raw, got := f.request(t, "git.worktree_create", map[string]any{
		"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf, "existingBranch": "wr"})
	if !strings.Contains(raw, `"success":true`) || !strings.Contains(raw, `"branch":"w1"`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	const vars = "{A2} {D} {W} {H}"
	want := []string{
		"P20 | worktree add --no-checkout <leaf> wr | <repo> | " + vars,
		"P20 | worktree add --no-track --no-checkout -b w1 <leaf> | <repo> | " + vars,
	}
	var adds []string
	for _, line := range got {
		if strings.Contains(line, "| worktree add ") {
			adds = append(adds, line)
		}
	}
	if !slices.Equal(adds, want) {
		t.Errorf("the add calls = %q\nwant %q", adds, want)
	}
	reg := filepath.Join(f.gitDir, "worktrees", "w1")
	if b, err := os.ReadFile(filepath.Join(f.leaf, ".git")); err != nil || strings.TrimSpace(string(b)) != "gitdir: "+reg {
		t.Errorf("the .git file of the new worktree = %q, %v\nwant gitdir: %s", b, err, reg)
	}
}

// TestShadowRemoveWithWorktreeRootNamesTheCopy pins the 8 calls of a
// git.worktree_remove with worktreeRoot in the state shadow. They carry the
// four folder variables, and the light calls carry the six values, as on
// 5fd08069 (cell N3 of val-s3p2b2-3-linux2, Linux VM: the table of the remove).
func TestShadowRemoveWithWorktreeRootNamesTheCopy(t *testing.T) {
	f := newShapeFixture(t, stateShadow)
	if raw, _ := f.request(t, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	root := resolveTestRoot(t, t.TempDir())
	requireTempOutsideCheckout(t, root)
	ext := filepath.Join(root, "proj", "wx")
	if raw, _ := f.request(t, "git.worktree_create", map[string]any{
		"baseRepo": f.repo, "branchName": "wx", "worktreePath": ext, "worktreeRoot": root}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	raw, got := f.request(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": ext, "worktreeRoot": root})
	if raw != removeOK {
		t.Fatalf("git.worktree_remove = %s", raw)
	}
	light := func(argv string) []shapeRow { return pair("P20", argv, "<repo>", "{A2} {D} {W}", "") }
	heavy := pair("P28", "rev-parse --absolute-git-dir", "<repo>", "{A} {D} {W}", "")
	f.check(t, "git.worktree_remove with worktreeRoot", got,
		rows(light("rev-parse --show-toplevel"), heavy, light("worktree list --porcelain -z"), heavy))
}
