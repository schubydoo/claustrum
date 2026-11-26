package git

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The deletes of git.worktree_remove and the registration of a create under the
// Windows rule of daemonVarsKept, in the state shadow. 87375cc7 deletes and
// registers there as with no daemon variable (measured on Windows). These tests
// set copyPinsWalkGitDir, so they run on every system.

// treeState is the names, kinds and contents of the tree at dir, one line each.
func treeState(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		line := rel + " " + d.Type().String()
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// ignoredRemoveRun is one remove of S from T in the state shadow, with the daemon
// variables of vars: "" for none, "xx" for GIT_DIR and GIT_COMMON_DIR of X, "xy"
// for GIT_DIR of X and GIT_COMMON_DIR of a third repository Y. X holds an entry
// zz whose record names S. locked locks S in T, and gone removes the folder of S
// first.
type ignoredRemoveRun struct {
	frame          string
	sGone, s0Gone  bool
	branch, zzKept bool
	xSame          bool
}

func runIgnoredRemove(t *testing.T, vars string, locked, gone bool) ignoredRemoveRun {
	t.Helper()
	clearFolderEnv(t)
	f := newRegFixture(t)
	y := filepath.Join(f.root, "Y")
	runGit(t, f.root, "init", "-q", "-b", "yonly", y)
	runGit(t, y, "commit", "-q", "--allow-empty", "-m", "y0")
	zz := f.entry(f.X, "zz")
	writeFile(t, filepath.Join(zz, "gitdir"), filepath.Join(f.S, ".git")+"\n", 0o644)
	if locked {
		runGit(t, f.T, "worktree", "lock", f.S)
	}
	if gone {
		if err := os.RemoveAll(f.S); err != nil {
			t.Fatal(err)
		}
	}
	before := treeState(t, gitDirOf(f.X))
	switch vars {
	case "xx":
		daemonGitEnv(t, gitDirOf(f.X), gitDirOf(f.X))
	case "xy":
		daemonGitEnv(t, gitDirOf(f.X), gitDirOf(y))
	}
	d, _ := newSeededShadowDeps(t)
	frame := gitDispatch(t, d, rpcLine(t, "git.worktree_remove", f.removeParams()))
	frame = strings.ReplaceAll(frame, strings.Trim(jsonQuoted(t, f.root), `"`), "<root>")
	return ignoredRemoveRun{
		frame:  frame,
		sGone:  !exists(f.S),
		s0Gone: !exists(f.entry(f.T, "s0")),
		branch: hasBranch(t, f.T, "s0"),
		zzKept: exists(zz),
		xSame:  treeState(t, gitDirOf(f.X)) == before,
	}
}

// TestIgnoredDaemonVarsRemoveDeletesAsWithNone: under GIT_DIR and GIT_COMMON_DIR
// of X, and under GIT_DIR of X with GIT_COMMON_DIR of Y, a remove of S from T
// deletes the folder, the entry s0 of T and the branch s0, and the git directory
// of X does not change. Its entry zz stays. A locked S is refused with the text
// of no variable, and nothing goes. Each run equals the run with no variable.
// 87375cc7 does so on Windows, with S present and gone (measured on Windows).
// Mutation: daemonGitDirSkipped keeps the rule of before with no off switch. The
// locked rows pass with that mutation too.
func TestIgnoredDaemonVarsRemoveDeletesAsWithNone(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	for _, locked := range []bool{false, true} {
		for _, gone := range []bool{false, true} {
			var base ignoredRemoveRun
			name := fmt.Sprintf("locked %v gone %v", locked, gone)
			t.Run(name+" no variable", func(t *testing.T) { base = runIgnoredRemove(t, "", locked, gone) })
			if !locked && (!base.s0Gone || base.branch || !base.zzKept || !base.xSame) {
				t.Fatalf("%s with no variable: %+v, want s0 and its branch gone and X kept", name, base)
			}
			if locked && (base.s0Gone || !base.branch || strings.Contains(base.frame, `"success":true`)) {
				t.Fatalf("%s with no variable: %+v, want a refusal that keeps s0", name, base)
			}
			for _, vars := range []string{"xx", "xy"} {
				t.Run(name+" "+vars, func(t *testing.T) {
					got := runIgnoredRemove(t, vars, locked, gone)
					if got != base {
						t.Errorf("got %+v\nwant %+v", got, base)
					}
				})
			}
		}
	}
}

// TestIgnoredCommonDirNamesTheCopy: under a daemon GIT_COMMON_DIR the calls of
// a request in the repository name its copy, as with no variable. So the add of
// a create on Windows takes the staged steps and registers the new worktree in
// baseRepo (TestIgnoredCommonDirCreateRegistersInBaseRepo). While a request with
// "shadowDirOff":true runs, no call names the copy. Mutation: daemonCommonDirSet
// returns daemonCommonDirInEnv.
func TestIgnoredCommonDirNamesTheCopy(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	u := f.useOf(t, f.repo)
	_, other := configRepo(t)
	t.Setenv("GIT_COMMON_DIR", other)
	if _, ok := u.names(f.repo); !ok {
		t.Error("names = false, want the copy")
	}
	offRequestsLive.Add(1)
	defer offRequestsLive.Add(-1)
	if _, ok := u.names(f.repo); ok {
		t.Error("off switch: names = true, want no copy")
	}
}

// TestIgnoredCommonDirPlainFolderInfo: under a daemon GIT_COMMON_DIR, a first
// git.info on a folder in no repository gives the reply of no variable, with no
// settingsControl member. 87375cc7 does so on Windows (measured on Windows).
// Mutation:
// daemonCommonDirSet returns daemonCommonDirInEnv.
func TestIgnoredCommonDirPlainFolderInfo(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	run := func(t *testing.T, withVar bool) string {
		f := newShadowFixture(t)
		if withVar {
			t.Setenv("GIT_COMMON_DIR", f.gitDir)
		}
		plain := resolveTestRoot(t, t.TempDir())
		raw, _ := f.info(t, plain, nil)
		return raw
	}
	var plain, got string
	t.Run("no variable", func(t *testing.T) { plain = run(t, false) })
	t.Run("GIT_COMMON_DIR", func(t *testing.T) { got = run(t, true) })
	if strings.Contains(plain, "settingsControl") || !strings.Contains(plain, `"isRepo":false`) {
		t.Fatalf("no variable: %s, want isRepo false and no settingsControl", plain)
	}
	if got != plain {
		t.Errorf("git.info = %s\nwant %s", got, plain)
	}
}
