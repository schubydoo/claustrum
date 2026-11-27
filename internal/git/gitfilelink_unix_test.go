//go:build unix

package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// gitFileLinkFixture is a repository T with a linked worktree W and a live
// worktree w1 below W. W's `.git` file names its entry through the path gitDir
// writes there, with `<entry name>` at the end.
func gitFileLinkFixture(t *testing.T, gitDir func(f statusFixture) string) (statusFixture, string) {
	t.Helper()
	f := newStatusFixture(t)
	w1 := filepath.Join(f.W, ".claude", "worktrees", "w1")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "w1", w1)
	writeFile(t, filepath.Join(f.W, ".git"), "gitdir: "+filepath.Join(gitDir(f), "worktrees", "W")+"\n", 0o644)
	return f, w1
}

// gitFileLinkRequests are the five methods on W, as the rows send them: a create
// of w2, a remove of the live w1 and a remove of w2, which the create never made.
func gitFileLinkRequests(f statusFixture, w1 string, off bool) []struct {
	method string
	params map[string]any
} {
	w2 := filepath.Join(f.W, ".claude", "worktrees", "w2")
	reqs := []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.W}},
		{"git.status", map[string]any{"path": f.W, "baseRepo": f.W}},
		{"git.list_branches", map[string]any{"path": f.W}},
		{"git.worktree_create", map[string]any{"baseRepo": f.W, "branchName": "w2", "worktreePath": w2}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.W, "worktreePath": w1, "branchName": "w1"}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.W, "worktreePath": w2, "branchName": "w2"}},
	}
	if off {
		for _, r := range reqs {
			r.params["shadowDirOff"] = true
		}
	}
	return reqs
}

// wantRefused is the frame of each request of gitFileLinkRequests for the
// refusal text.
func wantRefused(t *testing.T, method string, params map[string]any, text string) string {
	t.Helper()
	switch method {
	case "git.worktree_create":
		return resultFrame(t, rpc.WorktreeReply{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	case "git.worktree_remove":
		return resultFrame(t, rpc.WorktreeRemoveReply{Success: false,
			Error: "failed to remove worktree: could not check whether " + params["worktreePath"].(string) + " is locked: " + text})
	}
	return errFrame(t, text)
}

// callRaw is the frame of one request of call.
func callRaw(call func(string, map[string]any) (string, []string), method string, params map[string]any) string {
	raw, _ := call(method, params)
	return raw
}

// checkNothingRemoved fails when w1, its branch or its entry went, or w2 came.
func checkNothingRemoved(t *testing.T, f statusFixture, w1 string) {
	t.Helper()
	if fi, err := os.Lstat(w1); err != nil || !fi.IsDir() {
		t.Errorf("the worktree w1: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.T, ".git", "worktrees", "w1")); err != nil {
		t.Errorf("the entry of w1: %v", err)
	}
	for _, b := range []string{"w1", "w2"} {
		out, _ := exec.Command("git", "-C", f.T, "branch", "--list", b).Output()
		if (len(out) != 0) != (b == "w1") {
			t.Errorf("branch %s: %q", b, out)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.W, ".claude", "worktrees", "w2")); !os.IsNotExist(err) {
		t.Errorf("the worktree w2: %v", err)
	}
}

// A `.git` file that names its entry through a link to a network path. Each of
// the five methods refuses with the text that names the `.git` file and runs no
// git call but the excludes read. Neither remove deletes anything. 87375cc7
// answers so on Windows for a junction and a directory symbolic link to a UNC
// path (measured on Windows). This test turns the Windows test on and fakes the
// reparse read.
func TestGitFileThroughNetworkLinkRefuses(t *testing.T) {
	var link string
	f, w1 := gitFileLinkFixture(t, func(f statusFixture) string {
		link = filepath.Join(f.root, "J")
		symlinkOrSkip(t, filepath.Join(f.T, ".git"), link)
		return link
	})
	call := removeCallLog(t, f.root)
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	text := "Claude doesn't follow the path in `" + filepath.Join(f.W, ".git") + "` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive."
	for _, r := range gitFileLinkRequests(f, w1, false) {
		raw, calls := call(r.method, r.params)
		if want := wantRefused(t, r.method, r.params, text); raw != want {
			t.Errorf("%s %v = %s\nwant %s", r.method, r.params, raw, want)
		}
		for _, l := range calls {
			if !strings.Contains(l, "|config --includes --path core.excludesFile") {
				t.Errorf("%s ran git: %q", r.method, calls)
				break
			}
		}
	}
	checkNothingRemoved(t, f, w1)
	// A link to a local drive gives no refusal.
	fakeLinkTargets(t, map[string]string{link: `\??\C:\work\T\.git`})
	if raw, calls := call("git.info", map[string]any{"path": f.W}); strings.Contains(raw, "doesn't follow") || len(calls) < 2 {
		t.Errorf("git.info through a local link = %s, %d calls", raw, len(calls))
	}
	// With "shadowDirOff":true a junction to a UNC path gets the hooks text that
	// names the reparse error on all six requests, the remove after a
	// colon (87375cc7, measured on Windows). A link to a regular file stands in for
	// that junction here, and the seam takes its ENOTDIR. The create through a
	// directory symbolic link (Git can't use) is not staged here: git on Linux uses
	// the linked folder, so the create succeeds (TestGitFileThroughNetworkLinkIsUnusable).
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.root, "afile")
	writeFile(t, file, "x\n", 0o644)
	symlinkOrSkip(t, file, link)
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	old := walkStatRefuses
	walkStatRefuses = func(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
	t.Cleanup(func() { walkStatRefuses = old })
	off := hooksPinPrefix + "this repository's git directory could not be examined (not a directory); if .git is a file, check the path written in it"
	for _, r := range gitFileLinkRequests(f, w1, true) {
		if raw, want := callRaw(call, r.method, r.params), wantRefused(t, r.method, r.params, off); raw != want {
			t.Errorf("%s %v = %s\nwant %s", r.method, r.params, raw, want)
		}
	}
	checkNothingRemoved(t, f, w1)
}

// A `.git` file whose git directory fails its stat with an error of
// walkStatRefuses. Each of the five methods refuses with the unexaminedGitDir
// text, and a remove puts it after a colon. Neither remove deletes anything.
// 87375cc7 answers so on Windows with "shadowDirOff":true for a junction to a UNC
// path, with the reparse error in its text (measured on Windows).
// This test fails the stat with ENOTDIR and lets the seam take that error. The
// requests without "shadowDirOff":true are claustrum's own, not measured: there
// 87375cc7 answers the link text.
func TestGitFileStatFailureRefuses(t *testing.T) {
	f, w1 := gitFileLinkFixture(t, func(f statusFixture) string {
		file := filepath.Join(f.root, "afile")
		writeFile(t, file, "x\n", 0o644)
		return file
	})
	call := removeCallLog(t, f.root)
	old := walkStatRefuses
	walkStatRefuses = func(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
	t.Cleanup(func() { walkStatRefuses = old })
	text := hooksPinPrefix + "this repository's git directory could not be examined (not a directory); if .git is a file, check the path written in it"
	for _, off := range []bool{true, false} {
		for _, r := range gitFileLinkRequests(f, w1, off) {
			raw, calls := call(r.method, r.params)
			if want := wantRefused(t, r.method, r.params, text); raw != want {
				t.Errorf("%s %v = %s\nwant %s", r.method, r.params, raw, want)
			}
			for _, l := range calls {
				if !strings.Contains(l, "|config --includes --path core.excludesFile") {
					t.Errorf("%s ran git: %q", r.method, calls)
					break
				}
			}
		}
	}
	checkNothingRemoved(t, f, w1)
}

// A create that git answers as no repository names a `.git` file whose git
// directory goes through a link to a network path (gitFileUnusable). 87375cc7
// answers so on Windows with "shadowDirOff":true for a directory symbolic link to
// a UNC path (measured on Windows). A link to a local drive and the test turned
// off keep not_a_repo.
func TestGitFileThroughNetworkLinkIsUnusable(t *testing.T) {
	var link string
	f, _ := gitFileLinkFixture(t, func(f statusFixture) string {
		link = filepath.Join(f.root, "J")
		symlinkOrSkip(t, filepath.Join(f.T, ".git"), link)
		return link
	})
	for _, k := range daemonGitKeys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	if g, noLook := unusableDotGit(f.W); g != filepath.Join(f.W, ".git") || noLook {
		t.Errorf("through a network link: %q %v", g, noLook)
	}
	fakeLinkTargets(t, map[string]string{link: `\??\C:\work\T\.git`})
	if g, _ := unusableDotGit(f.W); g != "" {
		t.Errorf("through a local link: %q", g)
	}
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	networkLinkChecks = false
	if g, _ := unusableDotGit(f.W); g != "" {
		t.Errorf("test off: %q", g)
	}
}

// gitFileLinkRefusal gives no refusal where the walk finds no `.git` file, or
// where its target has no hop to a network or device path. A folder with no
// repository, a bare repository, a `.git` file with a local link or none, a link
// that cannot be read, a chain of linkChainCap hops and a walk start that fails
// pass (claustrum's own, not measured).
func TestGitFileLinkRefusalPasses(t *testing.T) {
	root := realTempDir(t)
	plain := filepath.Join(root, "plain")
	bare := filepath.Join(root, "bare.git")
	for _, d := range []string{plain, filepath.Join(bare, "objects"), filepath.Join(bare, "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n", 0o644)
	// A `.git` file in W names its git directory through the link L.
	w := filepath.Join(root, "W")
	if err := os.MkdirAll(filepath.Join(root, "G", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "L")
	symlinkOrSkip(t, filepath.Join(root, "G"), link)
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w, ".git"), "gitdir: "+filepath.Join(link, "x")+"\n", 0o644)
	// The same link refuses with a target on a share: the cases below differ only
	// in the one step that each names.
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	if got := gitFileLinkRefusal(w); got != gitFileLinkText(filepath.Join(w, ".git")) {
		t.Fatalf("through a link to a share: %q", got)
	}
	for _, c := range []struct {
		name    string
		dir     string
		targets map[string]string
	}{
		{"a folder with no repository", plain, map[string]string{link: `\??\UNC\localhost\share`}},
		{"a bare repository", bare, map[string]string{link: `\??\UNC\localhost\share`}},
		{"a local link", w, map[string]string{link: "rel:G"}},
		{"a link that cannot be read", w, map[string]string{}},
		{"a chain of linkChainCap hops", w, map[string]string{link: "rel:L"}},
	} {
		fakeLinkTargets(t, c.targets)
		if got := gitFileLinkRefusal(c.dir); got != "" {
			t.Errorf("%s: %q", c.name, got)
		}
	}
	fakeLinkTargets(t, map[string]string{link: `\??\UNC\localhost\share`})
	old := resolveWalkLinks
	resolveWalkLinks = func(p string) (string, error) {
		if p == w {
			return "", errors.New("no resolve")
		}
		return old(p)
	}
	t.Cleanup(func() { resolveWalkLinks = old })
	if got := gitFileLinkRefusal(w); got != "" {
		t.Errorf("a walk start that fails: %q", got)
	}
}
