package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The answer of git.status: the entries of `changes`. Each row is from the
// side-by-side probe of 89cb6289 on a Linux VM, unless the comment names another.

const (
	subPresent = ` S "gl"` + submodulePresentText
	subStaged  = ` S "gl"` + submoduleStagedText
	subUnread  = ` S "gl"` + submoduleUnreadText
)

// statusResult decodes a git.status frame that must hold a result.
func statusResult(t *testing.T, raw string) gitStatusResult {
	t.Helper()
	var frame struct {
		Result *gitStatusResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &frame); err != nil || frame.Result == nil {
		t.Fatalf("frame = %s, want a result (%v)", raw, err)
	}
	return *frame.Result
}

// submoduleEntries are the " S " entries of a frame. The porcelain lines before
// them come from git and are left out, so a newer git cannot move the assertion.
func submoduleEntries(t *testing.T, raw string) []string {
	t.Helper()
	r := statusResult(t, raw)
	var subs []string
	for i, c := range r.Changes {
		if strings.HasPrefix(c, " S ") {
			subs = append(subs, c)
		} else if len(subs) > 0 {
			t.Errorf("changes[%d] = %q comes after a submodule entry", i, c)
		}
	}
	if len(subs) > 0 && (r.Clean || !r.IsRepo) {
		t.Errorf("frame = %s, want isRepo:true and clean:false with a submodule entry", raw)
	}
	return subs
}

// stageGitlink adds an index entry of mode 160000 named name to the worktree.
func (f statusFixture) stageGitlink(t *testing.T, name, id string) {
	t.Helper()
	runGit(t, f.W, "update-index", "--add", "--cacheinfo", gitlinkMode+","+id+","+name)
}

// commitGitlink stages the gitlink gl at the commit of T and commits it.
func (f statusFixture) commitGitlink(t *testing.T) {
	t.Helper()
	f.stageGitlink(t, "gl", statusGitOut(t, f.T, "", "rev-parse", "HEAD"))
	runGit(t, f.W, "commit", "-q", "-m", "gl")
}

// Rows o1 and o2: the cut of a long entry and the stop at 10000 entries.
func TestPorcelainEntries(t *testing.T) {
	long := "?? " + strings.Repeat("d", 600)
	got := porcelainEntries([]byte(" M a1\n" + long + "\n"))
	if len(got) != 2 || got[0] != " M a1" {
		t.Fatalf("entries = %q, want 2 with the leading space kept", got)
	}
	// Row o1: 603 bytes become 512 bytes and a 3-byte "…".
	if want := long[:512] + "…"; got[1] != want || len(got[1]) != 515 {
		t.Errorf("long entry = %d bytes %q\nwant 515 bytes %q", len(got[1]), got[1], want)
	}
	// Row o2: 10001 entries become 10000, and the last one is a plain entry.
	var b strings.Builder
	for i := range 10001 {
		fmt.Fprintf(&b, "?? f%05d\n", i)
	}
	got = porcelainEntries([]byte(b.String()))
	if len(got) != 10000 || got[9999] != "?? f09999" {
		t.Errorf("%d entries, last %q\nwant 10000 entries, last %q", len(got), got[len(got)-1], "?? f09999")
	}
	if got := porcelainEntries(nil); got != nil {
		t.Errorf("no output gives %q, want no entry", got)
	}
}

// Row o11: the quoting and the cut of a submodule name.
func TestSubmoduleEntryQuoting(t *testing.T) {
	for name, want := range map[string]string{
		strings.Repeat("L", 250): ` S "` + strings.Repeat("L", 200) + `…"` + submodulePresentText,
		"a\"b\né":                ` S "a\"b\né"` + submodulePresentText,
		"bad\xff\xfe":            ` S "bad\xff\xfe"` + submodulePresentText,
		"sp ace":                 ` S "sp ace"` + submodulePresentText,
		"ta\tb":                  ` S "ta\tb"` + submodulePresentText,
	} {
		if got := submoduleEntry(name, submodulePresentText); got != want {
			t.Errorf("submoduleEntry(%q) = %s\nwant %s", name, got, want)
		}
	}
}

// The submodule entries, with real index entries.
func TestGitStatusSubmoduleEntries(t *testing.T) {
	other := strings.Repeat("a", 40)
	cases := []struct {
		name string
		edit func(t *testing.T, f statusFixture)
		want []string
	}{
		// Rows o6d and n20e: no .git below the gitlink path gives no entry.
		{"o6d folder with no .git", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			if err := os.Mkdir(filepath.Join(f.W, "gl"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"n20e no folder", func(t *testing.T, f statusFixture) { f.commitGitlink(t) }, nil},
		// Rows o6a and o6b: a .git file or folder is "present".
		{"o6a .git file", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			writeFile(t, filepath.Join(f.W, "gl", ".git"), "x\n", 0o644)
		}, []string{subPresent}},
		{"o6b .git folder", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			if err := os.MkdirAll(filepath.Join(f.W, "gl", ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, []string{subPresent}},
		// Row o4: the gitlink path is a regular file. On a Linux VM that gives the
		// "could not be inspected" entry. On a Windows VM it gives no entry.
		{"o4 gitlink path is a file", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			writeFile(t, filepath.Join(f.W, "gl"), "file\n", 0o644)
		}, map[bool][]string{false: {subUnread}, true: nil}[runtime.GOOS == "windows"]},
		// Rows o9b and o9c: a staged gitlink gives "change staged".
		{"o9b staged gitlink", func(t *testing.T, f statusFixture) {
			f.stageGitlink(t, "gl", other)
			if err := os.Mkdir(filepath.Join(f.W, "gl"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, []string{subStaged}},
		// Row o7: one gitlink gives both entries, the ls-files one first.
		{"o7 present and staged", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			writeFile(t, filepath.Join(f.W, "gl", ".git"), "x\n", 0o644)
			f.stageGitlink(t, "gl", other)
		}, []string{subPresent, subStaged}},
		// Row o8: a gitlink removed from the index gives "change staged".
		{"o8 gitlink removed from the index", func(t *testing.T, f statusFixture) {
			f.commitGitlink(t)
			runGit(t, f.W, "rm", "--cached", "-q", "gl")
		}, []string{subStaged}},
		// Row o12: one entry for each index stage.
		{"o12 conflicted gitlink", func(t *testing.T, f statusFixture) {
			writeFile(t, filepath.Join(f.W, "gl", ".git"), "x\n", 0o644)
			id := statusGitOut(t, f.T, "", "rev-parse", "HEAD")
			statusGitOut(t, f.W, fmt.Sprintf("160000 %s 1\tgl\n160000 %s 2\tgl\n160000 %s 3\tgl\n", other, id, id),
				"update-index", "--index-info")
		}, []string{subPresent, subPresent, subPresent}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStatusFixture(t)
			c.edit(t, f)
			if got := submoduleEntries(t, f.status(t)); !slices.Equal(got, c.want) {
				t.Errorf("submodule entries = %q\nwant %q", got, c.want)
			}
		})
	}
}

// Rows n20d and o10: at most 100 submodule entries, then one entry with the count
// of the rest.
func TestGitStatusSubmoduleOverflow(t *testing.T) {
	f := newStatusFixture(t)
	var info strings.Builder
	for i := range 101 {
		fmt.Fprintf(&info, "160000 %s 0\ts%03d\n", strings.Repeat("a", 40), i)
	}
	statusGitOut(t, f.W, info.String(), "update-index", "--index-info")
	got := submoduleEntries(t, f.status(t))
	if len(got) != 101 {
		t.Fatalf("%d submodule entries, want 101", len(got))
	}
	if want := ` S "s099"` + submoduleStagedText; got[99] != want {
		t.Errorf("entry 100 = %s\nwant %s", got[99], want)
	}
	if want := " S … (+1 more submodule entries)"; got[100] != want {
		t.Errorf("last entry = %q\nwant %q", got[100], want)
	}
}

// Rows o16b1 and o16b2: a worktree with a split index.
func TestGitStatusSplitIndex(t *testing.T) {
	f := newStatusFixture(t)
	runGit(t, f.W, "update-index", "--split-index")
	if shared, _ := filepath.Glob(filepath.Join(f.entry, "sharedindex.*")); len(shared) == 0 {
		t.Skip("this git wrote no sharedindex file")
	}
	if got := f.status(t); got != statusClean {
		t.Errorf("o16b1 git.status = %s\nwant %s", got, statusClean)
	}
	writeFile(t, filepath.Join(f.W, "t.txt"), "chg\n", 0o644)
	runGit(t, f.W, "add", "t.txt")
	if got := statusResult(t, f.status(t)).Changes; !slices.Equal(got, []string{"M  t.txt"}) {
		t.Errorf("o16b2 changes = %q\nwant %q", got, []string{"M  t.txt"})
	}
}

// Row o15d: an entry with no index passes, and git reads every file as deleted
// from the index and untracked.
func TestGitStatusMissingIndex(t *testing.T) {
	f := newStatusFixture(t)
	if err := os.Remove(filepath.Join(f.entry, "index")); err != nil {
		t.Fatal(err)
	}
	want := []string{"D  .gitignore", "D  t.txt", "?? .gitignore", "?? t.txt"}
	if got := statusResult(t, f.status(t)).Changes; !slices.Equal(got, want) {
		t.Errorf("o15d changes = %q\nwant %q", got, want)
	}
}

// Rows K1, n12, o17 and o16b1: the files of the temporary git folder.
func TestStatusGitDirContents(t *testing.T) {
	entry, common := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"HEAD": "ref: refs/heads/w\n", "index": "idx", "commondir": "../..\n", "gitdir": "x\n",
		"ORIG_HEAD": "o\n", "logs/HEAD": "l\n", "config.worktree": "[core]\n\tsparseCheckout = true\n",
		"info/sparse-checkout": "/*\n", "info/exclude": "e\n", "sharedindex.abc": "shared",
	} {
		writeFile(t, filepath.Join(entry, filepath.FromSlash(name)), body, 0o644)
	}
	list := func(e statusEntry) []string {
		tmp, err := buildStatusGitDir(e, common)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if !strings.HasPrefix(filepath.Base(tmp), statusGitDirTempPrefix) {
			t.Errorf("temporary folder %s, want the prefix %s", tmp, statusGitDirTempPrefix)
		}
		if b, _ := os.ReadFile(filepath.Join(tmp, "commondir")); string(b) != common+"\n" {
			t.Errorf("commondir = %q, want %q", b, common+"\n")
		}
		var names []string
		_ = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, _ error) error {
			if !d.IsDir() {
				rel, _ := filepath.Rel(tmp, p)
				names = append(names, filepath.ToSlash(rel))
			}
			return nil
		})
		return names
	}
	// Row o17, with the sharedindex file of row o16b1.
	want := []string{"HEAD", "commondir", "config.worktree", "index", "info/sparse-checkout", "sharedindex.abc"}
	if got := list(statusEntry{dir: entry, shared: []string{"sharedindex.abc"}, hasConfig: true}); !slices.Equal(got, want) {
		t.Errorf("temporary folder holds %q\nwant %q", got, want)
	}
	// Row K1: an entry with none of the three holds HEAD, commondir and index.
	for _, name := range []string{"config.worktree", "info/sparse-checkout", "sharedindex.abc"} {
		if err := os.Remove(filepath.Join(entry, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	want = []string{"HEAD", "commondir", "index"}
	if got := list(statusEntry{dir: entry}); !slices.Equal(got, want) {
		t.Errorf("temporary folder holds %q\nwant %q", got, want)
	}
}

// The records of `diff-index --raw -z`, with no git.
func TestStagedSubmodules(t *testing.T) {
	z := "\x00"
	id := strings.Repeat("a", 40)
	out := ":100644 100644 " + id + " " + id + " M" + z + "plain.txt" + z +
		":160000 160000 " + id + " " + id + " M" + z + "sub" + z +
		":000000 160000 " + id + " " + id + " A" + z + "new" + z +
		":160000 100644 " + id + " " + id + " T" + z + "t.txt" + z +
		":100644 100644 " + id + " " + id + " R100" + z + "from" + z + "to" + z +
		":160000 000000 " + id + " " + id + " D" + z + "gone" + z +
		":160000 short" + z + "ignored" + z
	want := []string{
		` S "sub"` + submoduleStagedText, ` S "new"` + submoduleStagedText,
		` S "t.txt"` + submoduleStagedText, ` S "gone"` + submoduleStagedText,
	}
	if got := stagedSubmodules([]byte(out)); !slices.Equal(got, want) {
		t.Errorf("stagedSubmodules = %q\nwant %q", got, want)
	}
}

// A listing that fails in the temporary git folder. No row measured it. claustrum
// answers the hooks refusal, as for the listing in the common directory (row o22).
func TestGitStatusTempListingFailureIsARefusal(t *testing.T) {
	f := newStatusFixture(t)
	// git reads the configuration of the common directory for this listing.
	common := filepath.Join(f.T, ".git")
	writeFile(t, filepath.Join(common, "config"), "[broken\n", 0o644)
	tmp := t.TempDir()
	ctx, cancel := gitCtx()
	defer cancel()
	r := &statusRun{ctx: ctx, path: f.W, common: common, tmp: tmp, pin: []string{"GIT_COMMON_DIR=" + common}}
	_, err := r.git(true, statusExcludesFile(), "status", "--porcelain")
	var refusal statusRefusal
	if !errors.As(err, &refusal) || !strings.HasPrefix(err.Error(), hooksRefusalPrefix) {
		t.Fatalf("status with a failing listing = %v\nwant a refusal that starts %q", err, hooksRefusalPrefix)
	}
}
