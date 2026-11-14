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
// checkout text that holds "smudge filter lfs failed" gets the note after it
// (measured). A text that the cut of 512 bytes ends at "smudge filter lf" gets
// none (measured). A cut text with "Error downloading object" gets the note,
// also when the cut drops the words of git (4c111e86, measured on Linux, macOS
// and Windows). With a program key of the repository the
// answer is the same text, not the git-lfs program refusal (measured). With
// "shadowDirOff":true there is no rule and no note (claustrum's own, not
// measured).
func TestShadowLfsDownloadNote(t *testing.T) {
	const failed = "fatal: a.txt: smudge filter lfs failed"
	cut := strings.Repeat("x", stderrHeadCap-len("smudge filter lf")) + "smudge filter lfs failed\n"
	download := "Error downloading object: a.txt (abc): Smudge error: x\n" + strings.Repeat("y", stderrHeadCap) + "\n" + failed + "\n"
	for _, row := range []struct {
		name, config, user, stderr, want string
		extra                            map[string]any
	}{
		{"the words end the text", lfsKnownLines, "", failed + "\n", failed + lfsDownloadNote, nil},
		{"a text before the words", lfsKnownLines, "", "Downloading a.txt (36 B)\nerror: external filter 'git-lfs filter-process' failed\n" + failed + "\n",
			"Downloading a.txt (36 B) error: external filter 'git-lfs filter-process' failed " + failed + lfsDownloadNote, nil},
		{"the cut ends before the words", lfsKnownLines, "", cut, strings.TrimSuffix(cut, "s failed\n"), nil},
		{"the download words in the cut", lfsKnownLines, "", download, strings.ReplaceAll(download[:stderrHeadCap], "\n", " ") + lfsDownloadNote, nil},
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

// TestCheckoutNote: the note of a failed checkout by its stderr. With the
// private LFS store the words of a full disk, of a quota and of a missing
// object give their own notes (4c111e86, measured on Linux and macOS, the
// quota on Linux). With no store, or with no settings copy, they do not. The
// ssh note reads the text after the cut. The note of a full disk reads the
// whole stderr (measured on macOS). With the store, `Object does not exist`
// with no `on the server` gives no missing note (measured on Linux and macOS).
func TestCheckoutNote(t *testing.T) {
	const run = "/h/.claude/remote/run/c1"
	store := &filterRule{shadow: true, store: &storeUse{runDir: run}}
	full := ". A disk on the remote machine filled up during the checkout: the one that holds the new worktree, or the one that holds " +
		run + ", where Claude keeps this checkout's Git LFS downloads while it runs. Free some space there (or raise the quota)," +
		" or turn off Worktree for this session, then try again."
	quota := ". Your disk quota on the remote machine ran out during the checkout: on the disk that holds the new worktree, or on the one that holds " +
		run + ", where Claude keeps this checkout's Git LFS downloads while it runs. Free some space there (or raise the quota)," +
		" or turn off Worktree for this session, then try again."
	missing := ". The repository's Git LFS server does not have that file; it may never have been pushed from the computer where it was committed." +
		" Turn off Worktree for this session, then try again."
	const failed = "\nfatal: b.bin: smudge filter lfs failed\n"
	for _, row := range []struct {
		name, stderr string
		rule         *filterRule
		want         string
	}{
		{"no space", "write x: no space left on device" + failed, store, full},
		{"no space, upper case", "write x: No space left on device" + failed, store, full},
		{"quota", "write x: disk quota exceeded" + failed, store, quota},
		{"quota, upper case", "write x: Disk quota exceeded" + failed, store, quota},
		{"missing object", "[x] Object does not exist on the server: [404] Object does not exist" + failed, store, missing},
		{"another failure", "batch response: Fatal error: server broke" + failed, store, lfsDownloadNote},
		{"a 404 text not on the server", "Smudge error: f.bin: [404] Object does not exist" + failed, store, lfsDownloadNote},
		{"a seed short of space", "[x] Object does not exist on the server: [404] Object does not exist" + failed,
			&filterRule{shadow: true, store: &storeUse{runDir: run, short: true}},
			". There isn't enough space on the remote machine (in " + run + ") for this repository's Git LFS files, and downloading them failed." +
				" Free up space there (Claude keeps 1 GiB spare), or turn off Worktree for this session, then try again."},
		{"a seed short of space and a full disk", "write x: no space left on device" + failed,
			&filterRule{shadow: true, store: &storeUse{runDir: run, short: true}}, full},
		{"missing object with no store", "[x] Object does not exist: [404] Object does not exist" + failed, &filterRule{shadow: true}, lfsDownloadNote},
		{"no space with no store", "no space left on device" + failed, &filterRule{shadow: true}, lfsDownloadNote},
		{"no words", "no space left on device\nfatal: b.bin: smudge filter f failed\n", store, ""},
		{"no settings copy", "no space left on device" + failed, &filterRule{store: &storeUse{runDir: run}}, ""},
		{"no rule", "no space left on device" + failed, nil, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := checkoutNote(row.stderr, row.stderr, row.rule); got != row.want {
				t.Errorf("checkoutNote = %q, want %q", got, row.want)
			}
		})
	}
}

// TestCheckoutNotePastTheCut: the ssh note reads the text after the cut, on
// every system. The words of git past the cut give no note. The download words
// of git-lfs in the cut give it, and a cut inside them gives none (4c111e86,
// measured on Linux and macOS). The note of a full disk reads the whole stderr
// (measured on macOS).
func TestCheckoutNotePastTheCut(t *testing.T) {
	const tail = "\nfatal: f.bin: smudge filter lfs failed\n"
	junk := strings.Repeat("j", 600)
	store := &filterRule{shadow: true, store: &storeUse{runDir: "/r"}}
	for _, row := range []struct {
		name, text string
		rule       *filterRule
		want       string
	}{
		{"only junk in the cut", junk, &filterRule{shadow: true}, ""},
		{"the cut ends after the download words", "jjj Error downloading object: f.bin", &filterRule{shadow: true}, lfsDownloadNote},
		{"the cut ends inside the download words", "jjj Error downloading obje", &filterRule{shadow: true}, ""},
		{"a 404 text first", "Smudge error: f.bin: [404] Object does not exist " + junk, &filterRule{shadow: true}, lfsDownloadNote},
		{"a text with no download words first", "lfs smudge failed: no more details " + junk, &filterRule{shadow: true}, ""},
		{"a full disk", "write x: no space left on device", store, ". A disk on the remote machine filled up during the checkout: the one that holds the new worktree, or the one that holds /r" + lfsStoreHoldsText},
	} {
		t.Run(row.name, func(t *testing.T) {
			stderr := row.text + "\n" + junk + tail
			if got := checkoutNote(stderr, row.text, row.rule); got != row.want {
				t.Errorf("checkoutNote = %q, want %q", got, row.want)
			}
		})
	}
}

// TestStoreFullStart: a download line with a write below the private LFS store
// that ends in a full disk or a quota gives its own start and code, in any
// letter case. A path outside the store, a quoted path and an empty path give
// none (4c111e86, measured on Linux and macOS). The bounds of the store itself,
// of a missing store and of the line are claustrum's own.
func TestStoreFullStart(t *testing.T) {
	const run = "/h/.claude/remote/run/c1"
	st := run + "/git-shadow/s/.checkout-1/lfs"
	store := &filterRule{shadow: true, store: &storeUse{runDir: run, path: st}}
	noSpace := "The disk that holds " + run + " on the remote machine filled up during the checkout: Claude keeps this checkout's Git LFS downloads there while it runs." +
		" Free some space on that disk, or turn off Worktree for this session, then try again. "
	quota := "Your disk quota ran out during the checkout on the disk that holds " + run + " on the remote machine: Claude keeps this checkout's Git LFS downloads there while it runs." +
		" Free some space on that disk or raise the quota, or turn off Worktree for this session, then try again. "
	const head, failed = "Error downloading object: n.bin (x): Smudge error: Error downloading n.bin (x): ", "\nfatal: n.bin: smudge filter lfs failed\n"
	for _, row := range []struct {
		name, stderr string
		rule         *filterRule
		start, code  string
	}{
		{"no space", head + "write " + st + "/tmp/123456: no space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"no space, upper case", head + "write " + st + "/tmp/123456: No space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"quota", head + "write " + st + "/tmp/123456: disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"quota, upper case", head + "write " + st + "/tmp/123456: Disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"an object temp", "Error downloading object: f.bin (x): Smudge error: write " + st + "/objects/e1/5f/x.tmp: no space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"a later line counts", head + "write " + st + "/a: permission denied\n" + head + "write " + st + "/b: disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"no download words", "Smudge error: write " + st + "/tmp/1: no space left on device" + failed, store, "", ""},
		{"words before the end", head + "write " + st + "/tmp/1: no space left on device, and more" + failed, store, "", ""},
		{"another path", head + "write /var/tmp/other/123456: no space left on device" + failed, store, "", ""},
		{"a quoted path", head + "write \"" + st + "/tmp/123456\": no space left on device" + failed, store, "", ""},
		{"an empty path", head + "write : no space left on device" + failed, store, "", ""},
		{"the store itself", head + "write " + st + ": no space left on device" + failed, store, "", ""},
		{"a folder beside the store", head + "write " + st + "x/tmp/1: no space left on device" + failed, store, "", ""},
		{"other words", head + "write " + st + "/tmp/1: permission denied" + failed, store, "", ""},
		{"no store", head + "write " + st + "/tmp/1: no space left on device" + failed, &filterRule{shadow: true}, "", ""},
		{"no settings copy", head + "write " + st + "/tmp/1: no space left on device" + failed, &filterRule{store: store.store}, "", ""},
		{"no rule", head + "write " + st + "/tmp/1: no space left on device" + failed, nil, "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if start, code := storeFullStart(row.stderr, row.rule); start != row.start || code != row.code {
				t.Errorf("storeFullStart = %q, %q, want %q, %q", start, code, row.start, row.code)
			}
		})
	}
}
