package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The filter rule in the state shadow, where git reads the copy of the
// settings and the rule takes the repository's own records from the read of
// the gate (shadowListings). Each row makes a file a.txt that uses the
// filter of attrs, and the filter program is a marker.

// shadowFilterFixture is a seeded shadow fixture whose commit has a.txt and a
// .gitattributes file.
type shadowFilterFixture struct {
	checkoutRuleFixture
	mark string
}

func newShadowFilterFixture(t *testing.T, attrs string) shadowFilterFixture {
	t.Helper()
	f := shadowFilterFixture{checkoutRuleFixture: newCheckoutRuleFixture(t), mark: filepath.Join(t.TempDir(), "mark")}
	mustWrite(t, filepath.Join(f.repo, ".gitattributes"), attrs)
	mustWrite(t, filepath.Join(f.repo, "a.txt"), "a\n")
	runGit(t, f.repo, "add", ".gitattributes", "a.txt")
	runGit(t, f.repo, "commit", "-q", "-m", "filter")
	return f
}

// section is a [filter "<name>"] section whose smudge command leaves the marker.
func (f shadowFilterFixture) section(t *testing.T, name, key string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "[filter \"" + name + "\"]\n\t" + key + " = CLAUSTRUM_TEST_HELPER=filter-mark '" +
		filepath.ToSlash(exe) + "' '" + filepath.ToSlash(f.mark) + "'\n"
}

// top is the repository folder with its links resolved, in the form of an
// includeIf "gitdir:" condition.
func (f shadowFilterFixture) top(t *testing.T) string {
	t.Helper()
	top, err := filepath.EvalSymlinks(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(top)
}

func scopedCalls(calls []envCall) int {
	n := 0
	for _, c := range calls {
		if slices.Contains(c.argv, "--show-scope") {
			n++
		}
	}
	return n
}

// TestShadowFilterRuleFailsClosed: a filter whose keys come from the
// repository config, or from a file that it includes, is refused. A filter
// that the user config defines runs. A filter that git cannot see in the copy
// does not run. No row makes a --show-scope listing, as on 5fd08069 in the
// state shadow (round task084-01).
func TestShadowFilterRuleFailsClosed(t *testing.T) {
	for _, row := range []struct {
		name, attrs string
		// stage writes the configs of the row.
		stage func(t *testing.T, f shadowFilterFixture)
		// refused is the name of the refusal, or "" for a create that succeeds.
		refused string
		ran     bool
	}{
		{"R-Q1a the repository config", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "m", "smudge"))
		}, "m", false},
		{"F1 a file that the repository config includes", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			setIncludeFile(t, f.gitDir, "inc.cfg", f.section(t, "m", "smudge"))
			f.addConfig(t, "[include]\n\tpath = inc.cfg\n")
		}, "m", false},
		{"another letter case of the key and the name", "a.txt filter=M\n", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "M", "Smudge"))
		}, "M", false},
		{"the same record twice in the repository config", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "m", "smudge")+f.section(t, "m", "smudge"))
		}, "m", false},
		{"the record of the user config in the repository config too", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "m", "smudge"))
			outside(t, f.section(t, "m", "smudge"))
		}, "", true},
		{"R-Q4h a conditional include of the user config", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			top, err := filepath.EvalSymlinks(f.repo)
			if err != nil {
				t.Fatal(err)
			}
			inc := filepath.Join(t.TempDir(), "inc.cfg")
			mustWrite(t, inc, f.section(t, "m", "smudge"))
			outside(t, "[includeIf \"gitdir/i:"+filepath.ToSlash(top)+"/\"]\n\tpath = "+filepath.ToSlash(inc)+"\n")
		}, "", true},
		{"F1b a conditional include of the repository config", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			top, err := filepath.EvalSymlinks(f.repo)
			if err != nil {
				t.Fatal(err)
			}
			setIncludeFile(t, f.gitDir, "f.cfg", f.section(t, "m", "smudge"))
			f.addConfig(t, "[includeIf \"gitdir:"+filepath.ToSlash(top)+"/\"]\n\tpath = f.cfg\n")
		}, "m", false},
		// Cell F1e of round task084-05 (Linux VM, 3 of 3 runs): a later
		// `smudge = cat` in the repository config after the includeIf line. Each
		// value of the filter comes from a file of the repository, and 5fd08069
		// answers repository_only_filter with the three items and the rollback.
		{"F1e a later smudge = cat after an includeIf file", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			top, err := filepath.EvalSymlinks(f.repo)
			if err != nil {
				t.Fatal(err)
			}
			setIncludeFile(t, f.gitDir, "m.cfg", f.section(t, "m", "smudge"))
			f.addConfig(t, "[includeIf \"gitdir:"+filepath.ToSlash(top)+"/\"]\n\tpath = m.cfg\n[filter \"m\"]\n\tsmudge = cat\n")
		}, "m", false},
		// Cells Q8: in one file the last value counts, so a command and then
		// `smudge = cat` in .git/config pass (lastSmudgePerFile).
		{"Q8 a command and then cat in the repository config", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "m", "smudge")+"[filter \"m\"]\n\tsmudge = cat\n")
		}, "", false},
		// Cells F1g and F1h of round task084-06 (Linux VM, 3 of 3 runs): an
		// includeIf file with `smudge = cat` and a command in .git/config, in
		// either order. 5fd08069 refuses, whatever the order.
		{"F1g cat in an includeIf file, then a command", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			setIncludeFile(t, f.gitDir, "c.cfg", "[filter \"m\"]\n\tsmudge = cat\n")
			f.addConfig(t, "[includeIf \"gitdir:"+f.top(t)+"/\"]\n\tpath = c.cfg\n"+f.section(t, "m", "smudge"))
		}, "m", false},
		{"F1h a command, then cat in an includeIf file", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			setIncludeFile(t, f.gitDir, "c.cfg", "[filter \"m\"]\n\tsmudge = cat\n")
			f.addConfig(t, f.section(t, "m", "smudge")+"[includeIf \"gitdir:"+f.top(t)+"/\"]\n\tpath = c.cfg\n")
		}, "m", false},
		{"F2 a filter that the repository config gets after a git.info", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			f.addConfig(t, f.section(t, "m", "smudge"))
		}, "m", false},
		{"a filter that the repository config gets after the gate", "a.txt filter=m\n", func(t *testing.T, f shadowFilterFixture) {
			atStep(t, "checkout", func() { f.addConfig(t, f.section(t, "m", "smudge")) })
		}, "", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShadowFilterFixture(t, row.attrs)
			row.stage(t, f)
			raw, calls := f.create(t, nil)
			if row.refused != "" {
				wantError(t, raw, repositoryOnlyFilterText(row.refused), "repository_only_filter")
				f.wantGone(t)
			} else if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if exists(f.mark) != row.ran {
				t.Errorf("the filter ran = %v, want %v", exists(f.mark), row.ran)
			}
			if n := scopedCalls(calls); n != 0 {
				t.Errorf("%d listings with --show-scope ran, want none", n)
			}
		})
	}
}

// In the state shadow (round task084-01) the stub fails or holds
// every --show-scope listing. A create with no filter succeeds (cell R-Q4b).
// A filter that only the repository defines is refused at once with a
// timeoutMs that a held listing outlasts (cell R-Q4a).
func TestShadowCreateMakesNoScopedListing(t *testing.T) {
	for _, tc := range []struct {
		name, seam string
		filter     bool
	}{{"R-Q4b the call fails", "scoped", false}, {"R-Q4a the call is held", "hold-scoped", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShadowFilterFixture(t, "a.txt filter=m\n")
			if tc.filter {
				f.addConfig(t, f.section(t, "m", "smudge"))
			}
			t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", tc.seam)
			t.Setenv("CLAUSTRUM_GITSTUB_HOLD_MS", "30000")
			start := time.Now()
			raw, calls := f.create(t, map[string]any{"timeoutMs": 20000})
			if tc.filter {
				wantError(t, raw, repositoryOnlyFilterText("m"), "repository_only_filter")
			} else if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if n := scopedCalls(calls); n != 0 {
				t.Errorf("%d listings with --show-scope ran, want none", n)
			}
			if took := time.Since(start); took >= 20*time.Second {
				t.Errorf("the create answered after %s, want it before the timeoutMs", took)
			}
		})
	}
}

// A create in the state shadow whose read of the repository config gives no
// verdict has no copy and reads the live config. The rule then takes the
// scoped listing (filterRule.scopedPlan), and a filter of the repository is
// refused. That state is claustrum's own, not measured.
func TestShadowFilterRuleWithNoRead(t *testing.T) {
	old := repoListingMaxBytes
	t.Cleanup(func() { repoListingMaxBytes = old })
	repoListingMaxBytes = 1
	f := newShadowFilterFixture(t, "a.txt filter=m\n")
	f.addConfig(t, f.section(t, "m", "smudge"))
	raw, calls := f.create(t, nil)
	wantError(t, raw, repositoryOnlyFilterText("m"), "repository_only_filter")
	if exists(f.mark) {
		t.Error("the filter of the repository ran")
	}
	if n := scopedCalls(calls); n != 1 {
		t.Errorf("%d listings with --show-scope ran, want one", n)
	}
}

// Cell L6 of round task084-02 (Linux VM) and cell Q4bp (macOS VM). In the state
// shadow the listing before the read-tree call fails. The create runs one `git
// version` in the root folder, answers the hooks refusal of that listing and
// rolls back. No second listing and no read-tree call follow.
func TestShadowFailedListingBeforeTheCheckout(t *testing.T) {
	f := newShadowFilterFixture(t, "a.txt filter=m\n")
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "full")
	raw, calls := f.create(t, nil)
	want := failedFrame(t, "git worktree add failed (checkout): config-defined hooks could not be pinned off; git not run: "+
		"listing the configuration in force: exit status 128: fatal: stub refuses the full listing")
	if raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	f.wantGone(t)
	at := slices.IndexFunc(calls, func(c envCall) bool {
		return slices.Contains(c.argv, "--list") && c.cwd == f.leaf
	})
	// An empty gitVersionDir (Windows) runs the call in the working folder of
	// the process.
	versionDir := gitVersionDir
	if versionDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		versionDir = wd
	}
	if at < 0 || at+1 >= len(calls) || !slices.Equal(calls[at+1].argv, []string{"version"}) || !sameDir(calls[at+1].cwd, versionDir) {
		t.Fatalf("calls = %q, want the failed listing in the leaf and then version in %s", calls, versionDir)
	}
	for _, c := range calls[at+1:] {
		if slices.Contains(c.argv, "read-tree") || (slices.Contains(c.argv, "--list") && c.cwd == f.leaf) {
			t.Errorf("the call %q in %s ran after the failed listing, want none", c.argv, c.cwd)
		}
	}
}

// sameDir reports whether a and b name one folder.
func sameDir(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// Cell F1i of round task084-06 (Linux VM, 3 of 3 runs): the repository config
// has only `smudge = cat` for the filter. With a settings copy the read-tree
// call gets no item for it, as on 5fd08069, and the create succeeds.
func TestShadowAllCatFilterGetsNoItem(t *testing.T) {
	f := newShadowFilterFixture(t, "a.txt filter=m\n")
	f.addConfig(t, "[filter \"m\"]\n\tsmudge = cat\n")
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	args := readTreeArgs(calls)
	if args == nil || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "filter.m.") }) {
		t.Errorf("the read-tree call %q, want one with no filter.m item", args)
	}
}
