package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests of the limit of repository folders in the folder of the daemon.
// The cell is G13 of round task075-01 (Linux VM).

// lowerRepoLimit sets shadowRepoLimit for one test.
func lowerRepoLimit(t *testing.T, n int) {
	t.Helper()
	old := shadowRepoLimit
	shadowRepoLimit = n
	t.Cleanup(func() { shadowRepoLimit = old })
}

// Cell G13: the limit of repository folders is 128.
func TestShadowRepoLimitIs128(t *testing.T) {
	if shadowRepoLimit != 128 {
		t.Errorf("shadowRepoLimit = %d, want 128", shadowRepoLimit)
	}
}

// Cell G13, with the limit at 2: a third repository removes the folder of the
// one with the oldest use, and a later use of that one removes the next. A
// short-lived dot folder does not count. The order by use is that of 5fd08069
// (EL2 cell 45, Linux VM).
func TestShadowRepoLimitEvictsOldestUse(t *testing.T) {
	lowerRepoLimit(t, 2)
	f := newRepoFolderFixture(t)
	_, gitB := configRepo(t)
	repoB := filepath.Dir(gitB)
	_, gitC := configRepo(t)
	repoC := filepath.Dir(gitC)
	writeFile(t, filepath.Join(f.h.dir, ".gitdir-1", "x"), "", 0o600)
	use := func(dir string) string {
		t.Helper()
		return f.sync(t, dir)
	}
	a := use(f.repo)
	b := use(repoB)
	// The folder with the larger name gets the older use, so that an order by
	// name alone removes the other one.
	older, newer, newerRepo := a, b, repoB
	if filepath.Base(a) < filepath.Base(b) {
		older, newer, newerRepo = b, a, f.repo
	}
	use(newerRepo)
	c := use(repoC)
	sweptGone(t, older)
	sweptThere(t, newer)
	sweptThere(t, c)
	sweptThere(t, filepath.Join(f.h.dir, ".gitdir-1", "x"))
	// The folder that went is made again, and the folder with the oldest use
	// now goes.
	if older == a {
		use(f.repo)
	} else {
		use(repoB)
	}
	sweptGone(t, newer)
	sweptThere(t, older)
	sweptThere(t, c)
}

// A folder that a git child holds stays (E2L cell 37, Linux VM). With each
// folder held the request gets the text of `repoLimitError`, and nothing goes
// (E2L cell 36).
func TestShadowRepoLimitKeepsBusyFolders(t *testing.T) {
	lowerRepoLimit(t, 2)
	f := newRepoFolderFixture(t)
	_, gitB := configRepo(t)
	_, gitC := configRepo(t)
	a := f.sync(t, f.repo)
	b := f.sync(t, filepath.Dir(gitB))
	dropA := holdFolder(t, f.h, a)
	defer holdFolder(t, f.h, b)()
	trust := gitDirTrustFor(filepath.Dir(gitC))
	_, err := f.h.syncRepo(trust.pinCommonDir, repoReadVerdict(trust, nil))
	const want = "Claude could not prepare its own files on the remote machine: git is running in 2 repositories at once; try again"
	if got := shadowRepoFailureText(err); got != want {
		t.Errorf("the refusal = %q\nwant %q", got, want)
	}
	sweptThere(t, a)
	sweptThere(t, b)
	sweptGone(t, filepath.Join(f.h.dir, shadowRepoName(trust.pinCommonDir)))
	// b is busy, a is free: a goes.
	dropA()
	if _, err := f.h.syncRepo(trust.pinCommonDir, repoReadVerdict(trust, nil)); err != nil {
		t.Fatal(err)
	}
	sweptGone(t, a)
	sweptThere(t, b)
}

// Through the methods: no folder is held after the replies, so a third
// repository finds a free folder.
func TestShadowRepoLimitAfterReplies(t *testing.T) {
	lowerRepoLimit(t, 2)
	f := newShadowFixture(t)
	repoB, _ := configRepo(t)
	repoC, _ := configRepo(t)
	for _, repo := range []string{f.repo, repoB, repoC} {
		if raw, _ := f.info(t, repo, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("git.info on %s = %s", repo, raw)
		}
	}
	sweptGone(t, filepath.Join(daemonFolder(t, f.run), shadowRepoName(filepath.Join(f.repo, ".git"))))
	f.s.gitShadow.repoMu.Lock()
	busy := len(f.s.gitShadow.repoBusy)
	f.s.gitShadow.repoMu.Unlock()
	if busy != 0 {
		t.Errorf("%d folders in use after the replies, want 0", busy)
	}
}

// holdFolder takes the hold of the repository folder dir of h, as a git child
// does, and returns its drop.
func holdFolder(t *testing.T, h *shadowHost, dir string) func() {
	t.Helper()
	drop, err := h.holdRepo(filepath.Dir(dir), filepath.Base(dir), false)
	if err != nil {
		t.Fatal(err)
	}
	return drop
}

// A failed open of the folder of the daemon lets the request go on to its
// mkdir (not measured, claustrum's own).
func TestShadowRepoRoomIgnoresAFailedListing(t *testing.T) {
	base, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = base.Close()
	h := &shadowHost{}
	if err := h.evictOldRepoFolder(base, "own", strings.Repeat("a", 32)); err != nil {
		t.Errorf("evictOldRepoFolder on a closed root = %v, want nil", err)
	}
}
