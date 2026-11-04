package git

import (
	"slices"
	"strings"
	"testing"
)

// The filter lfs in the state shadow with a settings copy, from the cells
// (Linux, git 2.43.0 and git-lfs 3.4.1). The stub fails the read-tree call with
// a text of its own, so no row needs git-lfs. The rows test the items of that
// call and the reply.

// lfsKnownLines is the [filter "lfs"] section that `git lfs install` writes.
const lfsKnownLines = "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n" +
	"\tprocess = git-lfs filter-process\n\trequired = true\n"

// lfsKnownItems are the four items of the known lines (measured).
var lfsKnownItems = []string{
	"filter.lfs.clean=git-lfs clean -- %f", "filter.lfs.process=git-lfs filter-process",
	"filter.lfs.required=true", "filter.lfs.smudge=git-lfs smudge -- %f",
}

// failLfsCheckout makes the read-tree call exit 128 with stderr, and git does
// not run.
func failLfsCheckout(t *testing.T, stderr string) {
	t.Helper()
	slowGit(t, "read-tree", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	stubStderr(t, stderr, false)
}

// TestShadowLfsFilterItems: the items of the read-tree call and
// the reply for each state of the filter lfs (measured on Linux).
func TestShadowLfsFilterItems(t *testing.T) {
	const forced = "fatal: forced failure\n"
	neutral := []string{"filter.lfs.process=", "filter.lfs.required=true", "filter.lfs.smudge=false"}
	for _, row := range []struct {
		name string
		// stage writes the configs of the row.
		stage   func(t *testing.T, f shadowFilterFixture)
		stderr  string
		items   []string
		refused bool
	}{
		{"the known lines in the repository config", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, lfsKnownLines)
		}, forced, lfsKnownItems, false},
		{"the known lines in the user config", func(t *testing.T, _ shadowFilterFixture) {
			outside(t, lfsKnownLines)
		}, forced, nil, false},
		{"a repository smudge with --skip alone", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, "[filter \"lfs\"]\n\tsmudge = git-lfs smudge --skip %f\n")
		}, forced, []string{"filter.lfs.smudge=git-lfs smudge --skip %f"}, false},
		{"a repository smudge program under the name lfs", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, f.section(t, "lfs", "smudge"))
		}, "fatal: a.txt: smudge filter lfs failed\n", neutral, true},
		{"a custom transfer agent of the repository", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, lfsKnownLines+"[lfs]\n\tstandalonetransferagent = x\n[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n")
		}, forced, lfsKnownItems, false},
		{"an extension of the repository", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, lfsKnownLines+"[lfs \"extension.x\"]\n\tsmudge = /no/such/command\n\tclean = /no/such/command\n")
		}, forced, lfsKnownItems, false},
		{"a custom transfer agent of the repository, the known lines outside", func(t *testing.T, f shadowFilterFixture) {
			outside(t, lfsKnownLines)
			f.addConfig(t, "[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n")
		}, forced, nil, false},
		{"cat for smudge and clean in the repository config", func(t *testing.T, f shadowFilterFixture) {
			f.addConfig(t, "[filter \"lfs\"]\n\tsmudge = cat\n\tclean = cat\n")
		}, forced, nil, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShadowFilterFixture(t, "a.txt filter=lfs\n")
			row.stage(t, f)
			failLfsCheckout(t, row.stderr)
			raw, calls := f.create(t, nil)
			if items := filterItems(calls); !slices.Equal(items, row.items) {
				t.Errorf("filter items of read-tree = %q, want %q", items, row.items)
			}
			if row.refused {
				wantError(t, raw, repositoryOnlyFilterText("lfs"), "repository_only_filter")
			} else if want := failedFrame(t, "git worktree add failed (checkout): "+strings.TrimSpace(row.stderr)); raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			f.wantGone(t)
		})
	}
}

// TestShadowLfsDownloadNote: with a settings copy a failed
// checkout text that holds "smudge filter lfs failed" after the cut gets the
// note after it (measured). A text that the cut of 512 bytes ends at "smudge
// filter lf" gets none (measured). With a program key of the repository the
// answer is the same text, not the git-lfs program refusal (measured). With
// "shadowDirOff":true there is no rule and no note (claustrum's own, not
// measured).
func TestShadowLfsDownloadNote(t *testing.T) {
	const failed = "fatal: a.txt: smudge filter lfs failed"
	cut := strings.Repeat("x", stderrHeadCap-len("smudge filter lf")) + "smudge filter lfs failed\n"
	for _, row := range []struct {
		name, config, user, stderr, want string
		extra                            map[string]any
	}{
		{"the words end the text", lfsKnownLines, "", failed + "\n", failed + lfsDownloadNote, nil},
		{"a text before the words", lfsKnownLines, "", "Downloading a.txt (36 B)\nerror: external filter 'git-lfs filter-process' failed\n" + failed + "\n",
			"Downloading a.txt (36 B) error: external filter 'git-lfs filter-process' failed " + failed + lfsDownloadNote, nil},
		{"the cut ends before the words", lfsKnownLines, "", cut, strings.TrimSuffix(cut, "s failed\n"), nil},
		{"a program key of the repository", "[lfs \"customtransfer.x\"]\n\tpath = /no/such/agent\n", lfsKnownLines, failed + "\n", failed + lfsDownloadNote, nil},
		{"shadowDirOff", lfsKnownLines, "", failed + "\n", failed, map[string]any{"shadowDirOff": true}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShadowFilterFixture(t, "a.txt filter=lfs\n")
			if row.user != "" {
				outside(t, row.user)
			}
			f.addConfig(t, row.config)
			failLfsCheckout(t, row.stderr)
			raw, _ := f.create(t, row.extra)
			if want := failedFrame(t, "git worktree add failed (checkout): "+row.want); raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			f.wantGone(t)
		})
	}
}
