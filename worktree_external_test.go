package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeErrorField decodes a git.worktree_* response and returns result.error.
// Decoding un-escapes the frame, so an assertion can match the message's literal
// angle brackets and an echoed Windows path (whose backslashes are escaped on the
// wire) without any platform-specific escaping.
func worktreeErrorField(t *testing.T, raw string) string {
	t.Helper()
	var resp struct {
		Result struct {
			Error string `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return resp.Result.Error
}

// External worktrees (7d193f89's "Worktree location" capability): with worktreeRoot
// set, git.worktree_create/_remove place the session folder OUTSIDE the repo at
// exactly <worktreeRoot>/<directory>/<name>. Frames measured byte-for-byte against
// 7d193f89 on an ephemeral VM.
func TestWorktreeCreateExternalRoot(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "mine")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)

	// Wrong depth: worktreePath must be exactly <root>/<dir>/<name>.
	for _, wp := range []string{
		filepath.Join(root, "wt"),               // one level below root
		filepath.Join(root, "d", "e", "wt"),     // three levels below root
		filepath.Join(base, "other", "d", "wt"), // not beneath root
	} {
		raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wp, "worktreeRoot": root}))
		// Match the decoded error so the literal angle brackets and the echoed root
		// (a backslash path on Windows) compare without any wire-escaping.
		want := "is not <worktree location>/<directory>/<name> beneath " + root
		if !strings.Contains(raw, `"errorCode":"unsafe_path"`) ||
			!strings.Contains(worktreeErrorField(t, raw), want) {
			t.Errorf("create(%s) = %s, want the containment refusal", wp, raw)
		}
	}

	// Valid external create: success, and a marker carrying the reference's exact
	// 285-byte body written into the <dir> level.
	wp := filepath.Join(root, "d", "wt")
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "ok", "worktreePath": wp, "worktreeRoot": root}))
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create(%s) = %s, want success", wp, raw)
	}
	got, err := os.ReadFile(filepath.Join(root, "d", managedWorktreesMarker))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if string(got) != managedWorktreesMarkerBody {
		t.Errorf("marker body = %q, want the reference's 285-byte content", got)
	}

	// Remove of the valid external worktree: success, and it is gone.
	rr := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wp, "worktreeRoot": root}))
	if !strings.Contains(rr, `"success":true`) {
		t.Errorf("remove(%s) = %s, want success", wp, rr)
	}
	if _, err := os.Stat(wp); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after remove (err=%v)", wp, err)
	}
}

// The external location checks reject a relative or ".."-bearing worktreeRoot or
// worktreePath (each with its own wording), and a symlinked <directory> component —
// none of which the 2-level containment alone would catch, because filepath.Clean
// would resolve the ".." away and a symlink would carry the create out of the root.
// Measured byte-for-byte against 7d193f89.
func TestWorktreeCreateExternalSpellingAndSymlink(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "mine")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)

	// A relative worktreeRoot names the root with the "worktree location" wording.
	relRoot := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "a", "worktreePath": filepath.Join(root, "d", "wt"), "worktreeRoot": "mine"}))
	if !strings.Contains(worktreeErrorField(t, relRoot), "mine is a relative path; choose the worktree location by its absolute path") {
		t.Errorf("relative worktreeRoot = %s, want the root relative-path refusal", relRoot)
	}

	// A ".." in worktreePath is refused before the containment cleans it away. The
	// path is built by concatenation, not filepath.Join, which would clean the "..".
	dd := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": root + "/d/../d/wt", "worktreeRoot": root}))
	if !strings.Contains(worktreeErrorField(t, dd), `contains a ".." component`) ||
		!strings.Contains(worktreeErrorField(t, dd), "session folder") {
		t.Errorf(`".." worktreePath = %s, want the dotdot refusal`, dd)
	}

	// A symlinked <directory> component is refused and does NOT carry the create out
	// of the root (the worktreeRoot itself may be a symlink, but not the dir beneath).
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dlink")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	sym := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "c", "worktreePath": filepath.Join(root, "dlink", "wt"), "worktreeRoot": root}))
	if !strings.Contains(worktreeErrorField(t, sym), "is a symbolic link; the directory under the worktree location must be a real directory") {
		t.Errorf("symlinked <dir> = %s, want the symlink refusal", sym)
	}
	if _, err := os.Stat(filepath.Join(outside, "wt")); err == nil {
		t.Errorf("create followed the symlink and escaped the root into %s", outside)
	}
}

// The <directory> level of an external worktree must start out empty, or already be
// a managed worktree directory (marker present). An existing directory that holds
// unrelated files with no marker is refused, naming the first entry (os.ReadDir
// order); a marked directory accepts a second worktree beside it.
func TestWorktreeCreateExternalDirMustBeEmpty(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "root")

	// Unmarked, non-empty <directory>: refused, naming the alphabetically-first entry.
	dirty := filepath.Join(root, "dirty")
	if err := os.MkdirAll(dirty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "z.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	wp := filepath.Join(dirty, "wt")
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "d", "worktreePath": wp, "worktreeRoot": root}))
	if !strings.Contains(raw, `"errorCode":"unsafe_path"`) ||
		!strings.Contains(worktreeErrorField(t, raw), `holds other files (for example "a.txt")`) ||
		!strings.Contains(worktreeErrorField(t, raw), "must start out empty") {
		t.Errorf("create into a dirty dir = %s, want the not-empty refusal naming a.txt", raw)
	}
	if _, err := os.Stat(wp); !os.IsNotExist(err) {
		t.Errorf("worktree created despite the not-empty refusal")
	}

	// A directory already holding the marker accepts a worktree beside it.
	marked := filepath.Join(root, "marked")
	if err := os.MkdirAll(marked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marked, managedWorktreesMarker), []byte(managedWorktreesMarkerBody), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "m", "worktreePath": filepath.Join(marked, "wt"), "worktreeRoot": root}))
	if !strings.Contains(ok, `"success":true`) {
		t.Errorf("create into a marked dir = %s, want success", ok)
	}
}

// An external remove whose target carries no `.git` file is refused and LEFT IN
// PLACE — the non-destructive external counterpart to the in-repo delete fallback
// (an in-repo plain directory is still deleted; an external one is not).
func TestWorktreeRemoveExternalMissingGit(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	stray := filepath.Join(base, "mine", "d", "wt")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	rr := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": stray, "worktreeRoot": filepath.Join(base, "mine")}))
	if !strings.Contains(rr, "has no .git file") || !strings.Contains(rr, "left in place") {
		t.Errorf("remove(%s) = %s, want the no-.git refusal", stray, rr)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("external remove deleted a non-worktree (%v); it must be left in place", err)
	}

	// A MISSING external worktree is not the same as a non-worktree directory: the
	// gate fires only when the path exists. Removing a path that is already gone
	// answers success:true (the reference treats it as nothing to do), not the
	// no-.git refusal.
	gone := filepath.Join(base, "mine", "d", "absent")
	rg := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": gone, "worktreeRoot": filepath.Join(base, "mine")}))
	if !strings.Contains(rg, `"success":true`) {
		t.Errorf("remove(%s) = %s, want success for an already-gone external worktree", gone, rg)
	}
}
