package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A broken GIT_CONFIG_KEY_<n> in the daemon's environment, in a folder that holds no
// repository. 89cb6289 was measured on Linux, macOS and Windows VMs. The phases P3,
// P5, P6 and P7 are those of the rows. A row names its Windows cell in win. A row with
// no Windows cell does not run on Windows. These tests set the daemon's environment, so
// none of them runs in parallel.

// notMeasuredHere reports whether a row stays out on this system: the system is
// Windows, and the row has no Windows cell.
func notMeasuredHere(win string) bool {
	return runtime.GOOS == "windows" && win == ""
}

// brokenPairPhases are the phases whose listing git fails.
var brokenPairPhases = []struct {
	name string
	env  []string
}{
	{"P3 empty key", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=", "GIT_CONFIG_VALUE_0=x"}},
	{"P5 key with no section", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=nodot", "GIT_CONFIG_VALUE_0=x"}},
	{"P6 invalid key", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a.b c", "GIT_CONFIG_VALUE_0=x"}},
	{"P7 good pair then broken pair", []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=user.s11", "GIT_CONFIG_VALUE_0=v",
		"GIT_CONFIG_KEY_1=", "GIT_CONFIG_VALUE_1=x"}},
}

// noRepoFixture is the listing fixture with the folders of the rows beside the
// repository T: N is a plain folder, NG holds an empty .git folder, R is a
// worktreeRoot with the level cp, R0 is an empty worktreeRoot, and M does not exist.
type noRepoFixture struct {
	listingFixture
	n, ng, r, r0, m string
}

func newNoRepoFixture(t *testing.T) noRepoFixture {
	t.Helper()
	f := newListingFixture(t)
	root := filepath.Dir(f.top)
	nf := noRepoFixture{listingFixture: f, n: filepath.Join(root, "N"), ng: filepath.Join(root, "NG"),
		r: filepath.Join(root, "R"), r0: filepath.Join(root, "R0"), m: filepath.Join(root, "M")}
	for _, d := range []string{nf.n, filepath.Join(nf.ng, ".git"), filepath.Join(nf.r, "cp"), nf.r0} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return nf
}

func (f noRepoFixture) leaf() string     { return filepath.Join(f.n, ".claude", "worktrees", "w1") }
func (f noRepoFixture) external() string { return filepath.Join(f.r, "cp", "w1") }

// rootCreate is the request of the cells Wf (Windows VM): a git.worktree_create with
// the plain folder as baseRepo and the empty worktreeRoot R0. Row A-N6 on the Linux VM
// names the same paths.
func (f noRepoFixture) rootCreate() map[string]any {
	return map[string]any{"baseRepo": f.n, "branchName": "w1",
		"worktreePath": filepath.Join(f.r0, "cp", "w1"), "worktreeRoot": f.r0}
}

// frame dispatches method and returns the whole reply frame.
func (f noRepoFixture) frame(t *testing.T, method string, params map[string]any) string {
	t.Helper()
	raw, _ := f.call(t, method, params)
	return raw
}

// listingText is the detail of the failed listing in dir: the exec error, then ": " and
// git's own stderr through the text rule. The test runs the real git for it, so the
// text after "exit status 128: " is that of the git on this host.
func (f noRepoFixture) listingText(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command(f.realGit, "config", "-z", "--list")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_DIR="+os.DevNull, "LC_ALL=C", "LANGUAGE=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil || err.Error() != "exit status 128" {
		t.Fatalf("real listing in %s: err = %v, want exit status 128", dir, err)
	}
	text := worktreeGitText(stderr.String(), nil)
	if !strings.HasSuffix(text, "fatal: unable to parse command-line config") {
		t.Fatalf("real listing stderr = %q, want git's command-line config failure", stderr.String())
	}
	return "exit status 128: " + text
}

func errFrame(t *testing.T, msg string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"error":` + errPart(t, msg) + `}`
}

func resultFrame(t *testing.T, result any) string {
	t.Helper()
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return `{"jsonrpc":"2.0","id":1,"result":` + string(b) + `}`
}

// dirNames lists dir, or nil when it does not exist.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// noRepoRows runs the rows of a failed listing and compares each whole frame. text is
// the refusal text that each frame carries. rootCell is the Windows cell of row A-N6
// in the phase of the caller, or "".
func (f noRepoFixture) noRepoRows(t *testing.T, text, rootCell string) {
	t.Helper()
	keep := filepath.Join(f.external(), "keep.txt")
	writeFile(t, keep, "k\n", 0o644)
	create := resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	for _, row := range []struct {
		name, win, method string
		params            map[string]any
		want              string
	}{
		{"A-N1", "P3-infoN", "git.info", map[string]any{"path": f.n}, errFrame(t, text)},
		{"A-N2", "P3-lbN", "git.list_branches", map[string]any{"path": f.n}, errFrame(t, text)},
		{"A-S1", "P3-statN", "git.status", map[string]any{"baseRepo": f.n, "path": f.n}, errFrame(t, text)},
		{"A-X2", "P3-infoX", "git.info", map[string]any{"path": f.ng}, errFrame(t, text)},
		{"A-N3", "P3-crN", "git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()}, create},
		{"A-N6", rootCell, "git.worktree_create", f.rootCreate(), create},
		{"A-N4", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()},
			resultFrame(t, worktreeRemoveResult{Success: false,
				Error: "failed to remove worktree: could not check whether " + f.leaf() + " is locked (" + text + "); retry"})},
		{"A-N4 no branchName", "P3-rmN", "git.worktree_remove", map[string]any{"baseRepo": f.n, "worktreePath": f.leaf()},
			resultFrame(t, worktreeRemoveResult{Success: false,
				Error: "failed to remove worktree: could not check whether " + f.leaf() + " is locked (" + text + "); retry"})},
		{"A-N5b", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "worktreePath": f.external(), "worktreeRoot": f.r},
			resultFrame(t, worktreeRemoveResult{Success: false,
				Error: "failed to remove worktree: cannot determine the repository's work tree: " + text})},
		{"A-N5c", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "branchName": "w1",
			"worktreePath": f.external(), "worktreeRoot": f.r},
			resultFrame(t, worktreeRemoveResult{Success: false,
				Error: "failed to remove worktree: cannot determine the repository's work tree: " + text})},
	} {
		if notMeasuredHere(row.win) {
			continue
		}
		if got := f.frame(t, row.method, row.params); got != row.want {
			t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, row.want)
		}
	}
	// Nothing changes on disk in any row.
	if got := dirNames(t, f.n); len(got) != 0 {
		t.Errorf("the plain folder holds %q, want nothing", got)
	}
	if got := dirNames(t, filepath.Join(f.r, "cp")); !slices.Equal(got, []string{"w1"}) || !exists(keep) {
		t.Errorf("the worktreeRoot level holds %q (keep.txt there: %v), want w1 with keep.txt", got, exists(keep))
	}
	if got := dirNames(t, f.r0); len(got) != 0 {
		t.Errorf("the empty worktreeRoot holds %q, want nothing", got)
	}
}

// With a broken pair, the five git methods refuse in a plain folder with the hooks
// refusal of the failed listing. The rows are A-N1 to A-N4, A-N6, A-S1 and A-X2 on
// Linux and macOS VMs. Row A-N4 ran with a branchName on the Linux VM and without one
// on the macOS VM. Rows A-N5b and A-N5c are from Linux and macOS VMs too, and they
// have no Windows cell. On a Windows VM the cells are P3-infoN, P3-infoX, P3-lbN,
// P3-statN, P3-crN and P3-rmN, and the same cells of P5 to P7. Cell P3-rmN ran with
// no branchName. Row A-N6 has the Windows cell Wf-P3-crNroot in P3 only.
// The text after "exit status 128: " is the stderr of the listing of the git on this
// host. Mutations: answer "no repository" before the listing in each method.
func TestBrokenConfigPairRefusesInPlainFolder(t *testing.T) {
	for i, ph := range brokenPairPhases {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			plantEnv(t, ph.env...)
			rootCell := ""
			if i == 0 {
				rootCell = "Wf-P3-crNroot"
			}
			f.noRepoRows(t, hooksRefusalPrefix+f.listingText(t, f.n), rootCell)
		})
	}
}

// The frames of the phase P3 rows with the text of the git of the VMs. The stub prints
// that stderr for every listing, so the frames are the measured bytes on every git.
func TestBrokenConfigPairMeasuredFrames(t *testing.T) {
	f := newNoRepoFixture(t)
	stubListing(t, 128, `error: empty config key\nfatal: unable to parse command-line config\n`)
	const text = "config-defined hooks could not be pinned off; git not run: listing the configuration in force: " +
		"exit status 128: error: empty config key fatal: unable to parse command-line config"
	f.noRepoRows(t, text, "Wf-P3-crNroot")
	if got, want := f.frame(t, "git.info", map[string]any{"path": f.n}),
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"`+text+`"}}`; got != want {
		t.Errorf("A-N1 = %s\nwant %s", got, want)
	}
}

// A path that does not exist keeps its frames with a broken pair (rows A-M1 and A-M3
// to A-M5, and cell P3-infoM on a Windows VM).
func TestBrokenConfigPairMissingPathKeepsFrames(t *testing.T) {
	f := newNoRepoFixture(t)
	plantEnv(t, brokenPairPhases[0].env...)
	leaf := filepath.Join(f.m, ".claude", "worktrees", "w1")
	for _, row := range []struct {
		name, win, method string
		params            map[string]any
		want              string
	}{
		{"A-M1", "P3-infoM", "git.info", map[string]any{"path": f.m}, notRepoInfo},
		{"A-M3", "", "git.list_branches", map[string]any{"path": f.m}, notRepoList},
		{"A-M4", "", "git.worktree_create", map[string]any{"baseRepo": f.m, "branchName": "w1", "worktreePath": leaf}, notRepoCreate},
		{"A-M5", "", "git.worktree_remove", map[string]any{"baseRepo": f.m, "branchName": "w1", "worktreePath": leaf},
			`{"success":true,"branchKept":true}`},
		{"A-M5 no branchName", "", "git.worktree_remove", map[string]any{"baseRepo": f.m, "worktreePath": leaf}, `{"success":true}`},
	} {
		if notMeasuredHere(row.win) {
			continue
		}
		want := `{"jsonrpc":"2.0","id":1,"result":` + row.want + `}`
		if got := f.frame(t, row.method, row.params); got != want {
			t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, want)
		}
	}
	if exists(f.m) {
		t.Errorf("a request made %s", f.m)
	}
}

// With no GIT_CONFIG_* entry (P0), and in the phases P8 to P10, every frame of a
// plain folder stays as it was. The listing of git.info runs with the plain folder as
// its working directory and GIT_DIR set to the null device, as in the call log of
// 89cb6289. On a Windows VM the cells are those of P0 and P8 to P10. Cell P0-rmN ran
// with no branchName. Row A-N6 has the Windows cell Wf-P0-crNroot in P0 only.
func TestPlainFolderFramesWithoutBrokenPair(t *testing.T) {
	for _, ph := range []struct {
		name, rootCell string
		env            []string
	}{
		{"P0 nothing set", "Wf-P0-crNroot", nil},
		{"P8 count 0", "", []string{"GIT_CONFIG_COUNT=0"}},
		{"P9 empty value", "", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.s11", "GIT_CONFIG_VALUE_0="}},
		{"P10 open quote in GIT_CONFIG_PARAMETERS", "", []string{"GIT_CONFIG_PARAMETERS='x"}},
	} {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			plantEnv(t, ph.env...)
			for _, row := range []struct {
				name, win, method string
				params            map[string]any
				want              string
			}{
				{"A-N1", "P0-infoN", "git.info", map[string]any{"path": f.n}, notRepoInfo},
				{"A-N2", "P0-lbN", "git.list_branches", map[string]any{"path": f.n}, notRepoList},
				{"A-S1", "P0-statN", "git.status", map[string]any{"baseRepo": f.n, "path": f.n}, notRepoStatus},
				{"A-X2", "P0-infoX", "git.info", map[string]any{"path": f.ng}, notRepoInfo},
				{"A-N3", "P0-crN", "git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()}, notRepoCreate},
				{"A-N6", ph.rootCell, "git.worktree_create", f.rootCreate(), notRepoCreate},
				{"A-N4", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()},
					`{"success":true,"branchKept":true}`},
				{"A-N4 no branchName", "P0-rmN", "git.worktree_remove", map[string]any{"baseRepo": f.n, "worktreePath": f.leaf()},
					`{"success":true}`},
				{"A-N5b", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "worktreePath": f.external(), "worktreeRoot": f.r},
					`{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: exit status 128"}`},
			} {
				if notMeasuredHere(row.win) {
					continue
				}
				want := `{"jsonrpc":"2.0","id":1,"result":` + row.want + `}`
				if got := f.frame(t, row.method, row.params); got != want {
					t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, want)
				}
			}
			if got := dirNames(t, f.r0); len(got) != 0 {
				t.Errorf("the empty worktreeRoot holds %q, want nothing", got)
			}
			_, calls := f.call(t, "git.info", map[string]any{"path": f.n})
			i := slices.IndexFunc(calls, envCall.listing)
			if i < 0 {
				t.Fatalf("calls = %v, want a listing", calls)
			}
			if canonicalPath(calls[i].cwd) != canonicalPath(f.n) || !slices.Contains(calls[i].env, "GIT_DIR="+os.DevNull) {
				t.Errorf("listing runs in %s with GIT_DIR=%v, want %s and the null device",
					calls[i].cwd, slices.Contains(calls[i].env, "GIT_DIR="+os.DevNull), f.n)
			}
		})
	}
}

// A daemon GIT_COMMON_DIR, and a baseRepo whose `.git` is an empty file. No row of
// 89cb6289 measures that state. The trust check finds no repository, and the listing
// gets no GIT_DIR entry there, so git reads the `.git` file and fails with `invalid
// gitfile format`. No GIT_CONFIG_* entry is set. The three requests keep the frames
// that they had before the daemon read that listing. The worktreeRoot row does not
// run on Windows, as in the rows above.
// Mutations: read the answer of the listing without noRepoPinned in
// noRepoListingRefusal (create), in removeGoneWorktree (remove) and in
// externalWorkTreeRefusal (remove with worktreeRoot).
func TestDaemonCommonDirKeepsNoRepoFrames(t *testing.T) {
	f := newNoRepoFixture(t)
	root := filepath.Dir(f.n)
	e := filepath.Join(root, "E")
	writeFile(t, filepath.Join(e, ".git"), "", 0o644)
	keep := filepath.Join(f.external(), "keep.txt")
	writeFile(t, keep, "k\n", 0o644)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(root, "no-common-dir"))
	// The control: the real listing in E fails, and not with "not a git repository".
	cmd := exec.Command(f.realGit, "config", "-z", "--list")
	cmd.Dir = e
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || !strings.Contains(stderr.String(), "invalid gitfile format") {
		t.Fatalf("real listing in %s: err = %v, stderr = %q, want git's gitfile failure", e, err, stderr.String())
	}
	leaf := filepath.Join(e, ".claude", "worktrees", "w1")
	for _, row := range []struct {
		name, method string
		params       map[string]any
		want         string
		skipWindows  bool
	}{
		{"create", "git.worktree_create", map[string]any{"baseRepo": e, "branchName": "w1", "worktreePath": leaf}, notRepoCreate, false},
		{"remove", "git.worktree_remove", map[string]any{"baseRepo": e, "branchName": "w1", "worktreePath": leaf},
			`{"success":true,"branchKept":true}`, false},
		{"remove with worktreeRoot", "git.worktree_remove", map[string]any{"baseRepo": e, "worktreePath": f.external(), "worktreeRoot": f.r},
			`{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: exit status 128"}`, true},
	} {
		if row.skipWindows && runtime.GOOS == "windows" {
			continue
		}
		if got, want := f.frame(t, row.method, row.params), resultOf(row.want); got != want {
			t.Errorf("%s = %s\nwant %s", row.name, got, want)
		}
	}
	if got := dirNames(t, e); !slices.Equal(got, []string{".git"}) {
		t.Errorf("the folder holds %q, want .git only", got)
	}
	if !exists(keep) {
		t.Errorf("%s is gone", keep)
	}
}

// versionFails makes every `git version` exit 1 with the stderr of the Linux rows. On
// a Windows VM the stderr of the stub was "boom", so the cells differ from these
// frames in that text alone.
func versionFails(t *testing.T) {
	t.Helper()
	slowGit(t, "version", "fail", 0, `git-stub: version fails\n`, "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
}

const versionFailsText = "git cannot run on this host; git not run: exit status 1: git-stub: version fails"

// Row A-T1 in phase P11. In a repository, with a broken pair and a `git version` that
// fails, the frame carries the exec error and the stderr of the version call, not
// those of the listing. The version child gets no GIT_CONFIG_COUNT set. On a Windows
// VM the cell is P11-infoT.
// Mutations: quote the listing's detail, pass the daemon's set on.
func TestGitVersionChildGetsNoConfigCountSet(t *testing.T) {
	f := newNoRepoFixture(t)
	versionFails(t)
	plantEnv(t, brokenPairPhases[0].env...)
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + versionFailsText + `"}}`
	if raw != want {
		t.Errorf("A-T1 in P11 = %s\nwant %s", raw, want)
	}
	i := slices.IndexFunc(calls, envCall.version)
	if i < 1 || !calls[i-1].listing() {
		t.Fatalf("calls = %v, want git version right after the listing", calls)
	}
	if !slices.Contains(calls[i-1].env, "GIT_CONFIG_KEY_0=") {
		t.Errorf("listing env = %q, want the daemon's pair in it", calls[i-1].env)
	}
	for _, kv := range calls[i].env {
		if n := envName(kv); n == configCountName || isConfigPairName(n) {
			t.Errorf("git version got %q, want no entry of the GIT_CONFIG_COUNT set", kv)
		}
	}
}

// The plain-folder rows of phase P11 answer the "cannot run" text of the version call
// too (rows A-N1 to A-N4 and A-S1 on Linux and macOS VMs). On a Windows VM the cells
// are P11-infoN, Wa-lbN, Wa-statN and Wa-crN. git.worktree_remove has no Windows cell
// there.
func TestGitVersionFailsInPlainFolder(t *testing.T) {
	f := newNoRepoFixture(t)
	versionFails(t)
	plantEnv(t, brokenPairPhases[0].env...)
	for _, row := range []struct {
		name, win, method string
		params            map[string]any
		want              string
	}{
		{"A-N1", "P11-infoN", "git.info", map[string]any{"path": f.n}, errFrame(t, versionFailsText)},
		{"A-N2", "Wa-lbN", "git.list_branches", map[string]any{"path": f.n}, errFrame(t, versionFailsText)},
		{"A-S1", "Wa-statN", "git.status", map[string]any{"baseRepo": f.n, "path": f.n}, errFrame(t, versionFailsText)},
		{"A-N3", "Wa-crN", "git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()},
			resultFrame(t, worktreeResult{Success: false, Error: versionFailsText, ErrorCode: "worktree_add_failed"})},
		{"A-N4", "", "git.worktree_remove", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()},
			resultFrame(t, worktreeRemoveResult{Success: false, Error: "failed to remove worktree: could not check whether " +
				f.leaf() + " is locked (" + versionFailsText + "); retry"})},
	} {
		if notMeasuredHere(row.win) {
			continue
		}
		if got := f.frame(t, row.method, row.params); got != row.want {
			t.Errorf("%s %s = %s\nwant %s", row.name, row.method, got, row.want)
		}
	}
	if got := dirNames(t, f.n); len(got) != 0 {
		t.Errorf("the plain folder holds %q, want nothing", got)
	}
}

// Row A-M1 in phase P11 (Linux and macOS VMs) and cell Wa-infoM (Windows VM). With a
// broken pair and a `git version` that fails, git.info on a path that does not exist
// keeps its frame. Mutation: answer the "cannot run" text without the stat of the
// path (failedListingCheck).
func TestGitVersionFailsMissingPathKeepsFrame(t *testing.T) {
	f := newNoRepoFixture(t)
	versionFails(t)
	plantEnv(t, brokenPairPhases[0].env...)
	if got, want := f.frame(t, "git.info", map[string]any{"path": f.m}), resultOf(notRepoInfo); got != want {
		t.Errorf("git.info = %s\nwant %s", got, want)
	}
	if exists(f.m) {
		t.Errorf("the request made %s", f.m)
	}
}

// Cell C-c (Linux and macOS VMs) and cells Wb (Windows VM). GIT_CONFIG_GLOBAL names a
// file that git cannot parse, and no GIT_CONFIG_COUNT is set. The listing fails, and
// the `git version` child gets no GIT_CONFIG_GLOBAL, so it passes. The answer is the
// hooks refusal with the detail of the listing, not the "cannot run" text. The text
// after "exit status 128: " is that of the git on this host. Mutation: pass
// GIT_CONFIG_GLOBAL on to `git version`.
func TestGitVersionChildGetsNoGlobalConfig(t *testing.T) {
	f := newNoRepoFixture(t)
	broken := filepath.Join(filepath.Dir(f.n), "broken.cfg")
	writeFile(t, broken, "[broken\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", broken)
	// The listing of the real git in N gives the expected detail.
	cmd := exec.Command(f.realGit, "config", "-z", "--list")
	cmd.Dir = f.n
	cmd.Env = append(os.Environ(), "GIT_DIR="+os.DevNull, "LC_ALL=C", "LANGUAGE=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || err.Error() != "exit status 128" {
		t.Fatalf("real listing with the broken file: err = %v, want exit status 128", err)
	}
	text := hooksRefusalPrefix + "exit status 128: " + worktreeGitText(stderr.String(), nil)
	raw, calls := f.call(t, "git.info", map[string]any{"path": f.n})
	if want := errFrame(t, text); raw != want {
		t.Errorf("C-c info N = %s\nwant %s", raw, want)
	}
	i := slices.IndexFunc(calls, envCall.version)
	if i < 1 || !calls[i-1].listing() {
		t.Fatalf("calls = %v, want git version right after the listing", calls)
	}
	if !slices.Contains(calls[i-1].env, "GIT_CONFIG_GLOBAL="+broken) {
		t.Errorf("listing env has no GIT_CONFIG_GLOBAL=%s", broken)
	}
	for _, kv := range calls[i].env {
		if envName(kv) == "GIT_CONFIG_GLOBAL" {
			t.Errorf("git version got %q, want no GIT_CONFIG_GLOBAL", kv)
		}
	}
	if got, want := f.frame(t, "git.info", map[string]any{"path": f.top}), errFrame(t, text); got != want {
		t.Errorf("C-c info T = %s\nwant %s", got, want)
	}
	create := resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	if got := f.frame(t, "git.worktree_create", map[string]any{"baseRepo": f.n, "branchName": "w1", "worktreePath": f.leaf()}); got != create {
		t.Errorf("C-c create N = %s\nwant %s", got, create)
	}
}
