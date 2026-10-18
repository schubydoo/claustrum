package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// enableWorktreeConfig gives the repository of f the worktreeConfig switch and
// the `config.worktree` file text.
func enableWorktreeConfig(t *testing.T, f shadowFixture, text string) {
	t.Helper()
	config := filepath.Join(f.gitDir, "config")
	mustWrite(t, config, mustRead(t, config)+"[extensions]\n\tworktreeConfig = true\n")
	mustWrite(t, filepath.Join(f.gitDir, worktreeConfigName), text)
}

// dropFixtureWorktree removes the linked worktree of the fixture, and the
// folder `worktrees` with it.
func dropFixtureWorktree(t *testing.T, f shadowFixture) {
	t.Helper()
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	_ = os.Remove(filepath.Join(f.gitDir, worktreesSubdir))
}

// configStepFixture is a seeded shadow fixture with the worktreeConfig switch,
// no linked worktree, and a first git.info that brought the copy up.
type configStepFixture struct {
	shadowFixture
	leaf, reg, main string
}

func newConfigStepFixture(t *testing.T, text string) configStepFixture {
	t.Helper()
	f := newSeededShadowFixture(t)
	dropFixtureWorktree(t, f)
	enableWorktreeConfig(t, f, text)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	return configStepFixture{
		shadowFixture: f,
		leaf:          filepath.Join(f.repo, ".claude", "worktrees", "w1"),
		reg:           filepath.Join(f.gitDir, worktreesSubdir, "w1"),
		main:          filepath.Join(f.gitDir, worktreeConfigName),
	}
}

func (f configStepFixture) params() map[string]any {
	return map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf}
}

func (f configStepFixture) create(t *testing.T) (string, []envCall) {
	t.Helper()
	return f.call(t, "git.worktree_create", f.params())
}

// failedFrame is the reply of a create that failed with errText.
func failedFrame(t *testing.T, errText string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, errText) + `,"errorCode":"worktree_add_failed"}}`
}

func ranReadTree(calls []envCall) bool {
	return slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") })
}

// TestConfigStepUnsetAllFailureFailsTheCreate: an `--unset-all` call that
// fails fails the create with the prefix and its cause. No read-tree runs, and
// the leaf, the registration and the branch go (cell C1 of round task007-01,
// Linux, macOS and Windows VMs). The stub stands in for the wrapper.
func TestConfigStepUnsetAllFailureFailsTheCreate(t *testing.T) {
	f := newConfigStepFixture(t, "[core]\n\tbare = false\n")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--unset-all,core.bare")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "5")
	raw, calls := f.create(t)
	want := failedFrame(t, completingAddPrefix+"removing core.bare from the new worktree's config.worktree: exit status 5")
	if raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran, want none")
	}
	for _, p := range []string{f.leaf, f.reg} {
		if exists(p) {
			t.Errorf("%s stays, want it gone", p)
		}
	}
	if branchExists(t, f.repo, "w1") {
		t.Error("the branch w1 stays, want it gone")
	}
}

// TestConfigStepReplacesAnEntryAtTheDestination: a file and an empty folder at
// `<registration>/config.worktree` before the copy get the copy, and the create
// succeeds (cells C4 and C5 of round task007-01, Linux and macOS VMs, and C4
// on a Windows VM). The seam puts the entry there at the step before the copy.
// On Windows an empty folder fails the create with `openat config.worktree: is
// a directory` (cell S9 of round task087-01, Windows VM).
func TestConfigStepReplacesAnEntryAtTheDestination(t *testing.T) {
	const filemode = "[core]\n\tfilemode = false\n"
	for _, row := range []struct {
		cell        string
		stage       func(t *testing.T, at string)
		failsOnThis bool
	}{
		{"C4 a file", func(t *testing.T, at string) { mustWrite(t, at, "x\n") }, false},
		{"C5 an empty folder", func(t *testing.T, at string) {
			if err := os.Mkdir(at, 0o755); err != nil {
				t.Error(err)
			}
		}, runtime.GOOS == "windows"},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newConfigStepFixture(t, filemode)
			at := filepath.Join(f.reg, worktreeConfigName)
			atStep(t, "registration", func() { row.stage(t, at) })
			raw, _ := f.create(t)
			if row.failsOnThis {
				if want := failedFrame(t, completingAddPrefix+"openat config.worktree: is a directory"); raw != want {
					t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
				}
				return
			}
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			if fi, err := os.Lstat(at); err != nil || !fi.Mode().IsRegular() || mustRead(t, at) != filemode {
				t.Errorf("the entry: %v, %v, want a regular file with the copy", fi, err)
			}
		})
	}
}
