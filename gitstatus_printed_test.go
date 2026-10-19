package main

import (
	"os"
	"path/filepath"
	"testing"
)

// linkedRepo makes a symbolic link to the repository of the fixture and
// returns its path. It skips the test where the system gives no link.
func linkedRepo(t *testing.T, repo string) string {
	t.Helper()
	link := filepath.Join(resolveTestRoot(t, t.TempDir()), "jrepo")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("no symbolic link here: %v", err)
	}
	return link
}

// TestUsePrintedGitDir pins the step of a git.status whose git directory git
// printed in another spelling. The request has the copy of the path through a
// link, and git printed the real path. After the step the request names a
// second folder, whose name is the sha256 of the printed path, and its git
// directory is the printed path. That is cell D6j-junction of
// val-s3p2b2-3-windows, where the link is a junction: a Windows run of
// TestStatusThroughAJunction must match it. Only Windows calls the step
// (statusFollowsPrintedGitDir).
//
// The same spelling, a folder that is another repository and a request with no
// copy change nothing. Without the identity test the second row changes the
// request. Without the new folder the first assertion fails.
func TestUsePrintedGitDir(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	link := linkedRepo(t, f.repo)
	trust := gitDirTrustFor(f.repo)
	read := repoReadVerdict(trust, nil)
	viaLink, err := f.h.syncRepo(filepath.Join(link, ".git"), read)
	if err != nil {
		t.Fatal(err)
	}

	req := &request{shadow: f.h, use: viaLink}
	if msg := req.usePrintedGitDir(trust, f.gitDir); msg != "" {
		t.Fatalf("the step refused: %s", msg)
	}
	want := filepath.Join(f.h.dir, shadowRepoName(f.gitDir))
	if req.use.gitDir != f.gitDir || req.use.repoDir != want || req.use.repoDir == viaLink.repoDir {
		t.Errorf("the request names %s for %s\nwant %s for %s", req.use.repoDir, req.use.gitDir, want, f.gitDir)
	}
	for _, dir := range []string{viaLink.repoDir, want} {
		if _, err := os.Lstat(filepath.Join(dir, "config")); err != nil {
			t.Errorf("the folder %s has no config: %v", dir, err)
		}
	}
	if req.repoRead == nil {
		t.Error("the request has no read of the printed git directory")
	}

	same := &request{shadow: f.h, use: viaLink}
	if msg := same.usePrintedGitDir(trust, viaLink.gitDir); msg != "" || same.use.repoDir != viaLink.repoDir || same.repoRead != nil {
		t.Errorf("the same spelling: %q, %s, read %v, want no change and no read", msg, same.use.repoDir, same.repoRead != nil)
	}
	_, otherGit := configRepo(t)
	other := &request{shadow: f.h, use: viaLink}
	if msg := other.usePrintedGitDir(trust, otherGit); msg != "" || other.use.repoDir != viaLink.repoDir {
		t.Errorf("another repository: %q, %s, want no change", msg, other.use.repoDir)
	}
	none := &request{shadow: f.h}
	if msg := none.usePrintedGitDir(trust, f.gitDir); msg != "" || none.use.shadow() {
		t.Errorf("a request with no copy: %q, %+v, want no change", msg, none.use)
	}
}

// TestStatusPrintedRootSpelling pins the one more spelling of a `path` inside
// baseRepo on Windows: the root that git printed, joined with the part of
// `path` below baseRepo. In cell D6j-junction baseRepo is `C:\o\w\jrepo`, git
// printed `C:/o/w/repo`, and the `gitdir` file names
// `C:/o/w/repo/.claude/worktrees/ws/.git`. Here a symbolic link stands for the
// junction.
//
// A root that is another folder gives no spelling: without the identity test
// that row gives one. So do an empty answer, a relative answer and a `path`
// outside baseRepo.
func TestStatusPrintedRootSpelling(t *testing.T) {
	f := newRefusalFixture(t)
	link := linkedRepo(t, f.repo)
	rel := filepath.Join(".claude", "worktrees", "wr")
	path := filepath.Join(link, rel)
	got, ok := statusPrintedRootSpelling(path, link, filepath.ToSlash(f.repo))
	if want := filepath.Join(f.repo, rel); !ok || got != want {
		t.Errorf("the spelling = %q, %v, want %q", got, ok, want)
	}
	otherRepo, _ := configRepo(t)
	if err := os.MkdirAll(filepath.Join(otherRepo, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	// A relative answer gives no spelling, also when it names the repository
	// from the working directory of the daemon.
	t.Chdir(filepath.Dir(f.repo))
	if got, ok := statusPrintedRootSpelling(path, link, filepath.Base(f.repo)); ok {
		t.Errorf("a relative answer that names the repository: spelling %q, want none", got)
	}
	for name, row := range map[string][3]string{
		"another folder":        {path, link, filepath.ToSlash(otherRepo)},
		"no answer":             {path, link, ""},
		"a relative answer":     {path, link, "repo"},
		"path is baseRepo":      {link, link, filepath.ToSlash(f.repo)},
		"path outside baseRepo": {f.wt, otherRepo, filepath.ToSlash(f.repo)},
	} {
		if got, ok := statusPrintedRootSpelling(row[0], row[1], row[2]); ok {
			t.Errorf("%s: spelling %q, want none", name, got)
		}
	}
}
