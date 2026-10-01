//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The Windows git.info root and the letter case of the `already exists` refusal. Each
// test names the mutation that turns it red.

// stubToplevel makes `rev-parse --show-toplevel` print answer and exit 0.
func stubToplevel(t *testing.T, answer string) {
	t.Helper()
	slowGit(t, "--show-toplevel", "fail", 0, "", answer+`\n`)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "0")
}

// Git's answer is the root only when it names the walk root's directory.
// Another folder and a relative answer give the walk root with forward slashes. The
// walk root in another letter case gives git's spelling (rows W14 to W16). Mutations:
// always take git's answer, or never take it.
func TestInfoRootWindows(t *testing.T) {
	f := newListingFixture(t)
	other := t.TempDir()
	root, _ := gitWalkRoot(f.top)
	walk := filepath.ToSlash(root)
	swapped := strings.ToUpper(walk)
	for _, tc := range []struct{ name, answer, want string }{
		{"another folder", filepath.ToSlash(other), walk},
		{"same folder, other case", swapped, swapped},
		{"relative", "../T", walk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubToplevel(t, tc.answer)
			raw, _ := f.call(t, "git.info", map[string]any{"path": f.top})
			var r struct {
				Result struct {
					Repo string `json:"repo"`
					Root string `json:"root"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				t.Fatal(err)
			}
			if r.Result.Root != tc.want || r.Result.Repo != filepath.Base(tc.want) {
				t.Errorf("info = %s, want root %s", raw, tc.want)
			}
		})
	}
}

// The pair runs in the pinned git directory: `--git-dir=<pin> config -z
// --list`, then `--git-dir=<pin> --work-tree=<walk root> rev-parse --show-toplevel`
// (row W01). Mutation: keep the request directory as the working directory.
func TestInfoRootPairWindows(t *testing.T) {
	f := newListingFixture(t)
	pins := commonDirPinEnv(f.top)
	if len(pins) != 1 {
		t.Fatalf("pin = %q, want one", pins)
	}
	pin := strings.TrimPrefix(pins[0], "GIT_COMMON_DIR=")
	walk, _ := gitWalkRoot(f.top)
	_, calls := f.call(t, "git.info", map[string]any{"path": f.top})
	i := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--show-toplevel") })
	if i < 1 {
		t.Fatalf("calls = %v, want the show-toplevel pair", calls)
	}
	pre, top := calls[i-1], calls[i]
	if !slices.Equal(pre.argv, []string{"--git-dir=" + pin, "config", "-z", "--list"}) {
		t.Errorf("listing = %q, want --git-dir=%s config -z --list", pre.argv, pin)
	}
	if !slices.Contains(top.argv, "--git-dir="+pin) || !slices.Contains(top.argv, "--work-tree="+walk) {
		t.Errorf("pair = %q, want --git-dir=%s --work-tree=%s", top.argv, pin, walk)
	}
	for _, c := range []envCall{pre, top} {
		if !strings.EqualFold(filepath.Clean(c.cwd), pin) {
			t.Errorf("call %q runs in %s, want %s", c.argv, c.cwd, pin)
		}
	}
}

// For <T>\lnk\.., a directory symlink lnk -> X\sub gives the root X, and a
// junction gives the root T, because the walk resolves no junction (rows D03-symlink
// and D03-junction). Mutations: resolve junctions in the walk, or give up the walk
// when filepath.EvalSymlinks fails on the junction.
func TestInfoRootLinkThenDotDotWindows(t *testing.T) {
	r := newTrustRepo(t)
	x := filepath.Join(r.base, "X")
	initTrustMain(t, x)
	if err := os.MkdirAll(filepath.Join(x, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("junction", func(t *testing.T) {
		lnk := filepath.Join(r.T, "jl")
		makeJunction(t, lnk, filepath.Join(x, "sub"))
		m := infoFields(t, info(t, lnk+`\..`))
		if m["isRepo"] != true || m["root"] != infoRootOf(r.T) || m["repo"] != "T" {
			t.Errorf("info = %v, want root %s and repo T", m, infoRootOf(r.T))
		}
	})
	t.Run("symlink", func(t *testing.T) {
		lnk := filepath.Join(r.T, "sl")
		if err := os.Symlink(filepath.Join(x, "sub"), lnk); err != nil {
			t.Skipf("symlink: %v", err)
		}
		m := infoFields(t, info(t, lnk+`\..`))
		if m["root"] != infoRootOf(x) {
			t.Errorf("info = %v, want root %s", m, infoRootOf(x))
		}
	})
}

// A repository below a junction answers git's own spelling of the root, which names
// the target of the junction (row W09). Mutation: give up the walk when
// filepath.EvalSymlinks fails on the junction.
func TestInfoRootBelowJunctionWindows(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	repo := filepath.Join(base, "real", "R")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	initTrustMain(t, repo)
	j := filepath.Join(base, "J")
	makeJunction(t, j, filepath.Join(base, "real"))
	m := infoFields(t, info(t, filepath.Join(j, "R")))
	if m["isRepo"] != true || m["root"] != infoRootOf(repo) || m["repo"] != "R" {
		t.Errorf("info = %v, want root %s and repo R", m, infoRootOf(repo))
	}
}

// The `already exists` refusal of an in-repo create names the path with
// the on-disk letter case of each existing component (row W15). Mutation: name the
// path as sent.
func TestCreateExistsNamesOnDiskCaseWindows(t *testing.T) {
	r := newTrustRepo(t)
	leaf := filepath.Join(r.T, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := existingPathSpelling(strings.ToUpper(leaf)); got != filepath.VolumeName(strings.ToUpper(leaf))+leaf[len(filepath.VolumeName(leaf)):] {
		t.Errorf("existingPathSpelling = %s, want %s", got, leaf)
	}
	base := strings.ToUpper(r.T)
	rep := trustCall(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "worktreePath": strings.ToUpper(leaf), "branchName": "w1"})
	want, _ := json.Marshal(worktreeResult{Success: false, ErrorCode: "unsafe_path",
		Error: "refusing to create worktree: " + filepath.VolumeName(base) + leaf[len(filepath.VolumeName(leaf)):] +
			" already exists, and a new worktree is only ever created in a fresh directory"})
	wantResult(t, "create", rep, string(want))
}
