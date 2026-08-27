package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every params-taking method must route a present-but-mistyped params object
// through bindParams to the shared -32602 Invalid params error (probe-verified
// contract; the value-typed cousins live in rpc_test.go). This sweeps the
// methods the earlier suites didn't reach one by one.
func TestBindParamsMistypedPerMethod(t *testing.T) {
	s := newTestServer(t)
	cases := map[string]map[string]any{
		"files.stat":          {"path": 123},
		"files.list":          {"path": 123},
		"files.validate":      {"path": 123},
		"files.extract_tar":   {"archivePath": 123},
		"git.info":            {"path": 123},
		"git.list_branches":   {"path": 123},
		"git.worktree_create": {"branchName": 123},
		"process.stdin":       {"id": 123},
		"process.killAndWait": {"id": 123},
	}
	for method, params := range cases {
		got := dispatchRaw(t, s, rpcLine(t, method, params))
		if !strings.Contains(got, "Invalid params") {
			t.Errorf("dispatch %s with mistyped params = %s, want Invalid params", method, got)
		}
	}
}

// git.worktree_create validates branchName presence right after bindParams,
// before any repo probing.
func TestGitWorktreeCreateMissingBranchName(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{"path": t.TempDir()}))
	if !strings.Contains(got, "branchName is required") {
		t.Errorf("worktree_create without branchName = %s, want branchName is required", got)
	}
}

// files.stat on a path that does not exist reports the zero facts object with
// exists:false — a result, never an error (matching the reference).
func TestFilesStatNonexistent(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.stat",
		map[string]any{"path": filepath.Join(t.TempDir(), "absent")}))
	if !strings.Contains(got, `"exists":false`) {
		t.Errorf("files.stat nonexistent = %s, want exists:false result", got)
	}
}

// files.list surfaces an unreadable directory as -32603 (unlike files.stat's
// soft empty result — probe-verified asymmetry).
func TestFilesListNonexistentDir(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.list",
		map[string]any{"path": filepath.Join(t.TempDir(), "absent")}))
	if !strings.Contains(got, `"code":-32603`) {
		t.Errorf("files.list nonexistent = %s, want -32603", got)
	}
}

// git.status on a clean repo (committed tree, no changes) takes the empty
// porcelain fast path: {isRepo:true, clean:true} with no changes array.
func TestGitStatusCleanRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "hi\n", 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	// 7d193f89 reports status for a session worktree of baseRepo, not the repo.
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "add", "-q", wt)

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	if !strings.Contains(got, `"clean":true`) {
		t.Errorf("git.status clean repo = %s, want clean:true", got)
	}
	if strings.Contains(got, `"changes"`) {
		t.Errorf("git.status clean repo = %s, want no changes field", got)
	}
}

// 7d193f89 runs status with --untracked-files=all, so an untracked file inside an
// untracked directory is listed individually ("?? sub/u.txt") rather than as the
// directory ("?? sub/"). Plain `git status --porcelain` reports the directory, so
// this is a wire change the frame battery (top-level untracked only) did not
// cover. Measured against the reference on an ephemeral VM.
func TestGitStatusUntrackedSubdirListsFiles(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "hi\n", 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	wt := filepath.Join(dir, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "add", "-q", wt)
	writeFile(t, filepath.Join(wt, "sub", "u.txt"), "u\n", 0o644) // untracked, one dir deep

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": dir}))
	if !strings.Contains(got, `?? sub/u.txt`) {
		t.Errorf("git.status = %s, want the file listed individually (?? sub/u.txt)", got)
	}
	if strings.Contains(got, `?? sub/"`) {
		t.Errorf("git.status = %s, listed the directory (?? sub/) — --untracked-files=all is missing", got)
	}
}

// tgzEntry describes one member of a crafted archive for the extractTarGz
// error-path tests; makeTarGz can't express directories or member ordering.
type tgzEntry struct {
	name string
	dir  bool
	body string
}

// writeTgz materializes entries into a .tar.gz at path, optionally truncating
// the inner tar stream to truncAt bytes (0 = no truncation) to simulate an
// archive cut off mid-member.
func writeTgz(t *testing.T, path string, entries []tgzEntry, truncAt int) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		if !e.dir {
			hdr = &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if truncAt > 0 {
		raw = raw[:truncAt]
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// extractTarGz's per-member failure arms: each malicious/degenerate archive
// must surface an error instead of a partial silent success.
func TestExtractTarGzMemberErrors(t *testing.T) {
	cases := []struct {
		name    string
		entries []tgzEntry
	}{
		// a directory member whose path runs through an already-extracted
		// regular file -> TypeDir MkdirAll fails with ENOTDIR
		{"dir through file", []tgzEntry{{name: "f", body: "x"}, {name: "f/sub", dir: true}}},
		// a file member nested under an already-extracted regular file ->
		// parent MkdirAll fails
		{"file under file", []tgzEntry{{name: "f", body: "x"}, {name: "f/sub/x", body: "y"}}},
		// a file member colliding with an already-extracted directory ->
		// OpenFile fails
		{"file over dir", []tgzEntry{{name: "d", dir: true}, {name: "d", body: "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "a.tar.gz")
			writeTgz(t, archive, tc.entries, 0)
			if _, err := extractTarGz(archive, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Error("extractTarGz succeeded, want error")
			}
		})
	}
}

// An archive truncated mid-member data errors out of the io.Copy arm
// (unexpected EOF from the tar reader), not a silent short extraction.
func TestExtractTarGzTruncatedMember(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "trunc.tar.gz")
	// One 600-byte file: header block [0,512), data [512,1536). Cutting at 900
	// leaves the member's advertised size unsatisfiable.
	writeTgz(t, archive, []tgzEntry{{name: "f", body: strings.Repeat("x", 600)}}, 900)
	if _, err := extractTarGz(archive, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Error("extractTarGz on truncated archive succeeded, want error")
	}
}

// An archive that ships a ".synced" *directory* leaves the success-marker
// WriteFile nowhere to go — the extraction must report that failure.
func TestExtractTarGzSyncedMarkerBlocked(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "synced.tar.gz")
	writeTgz(t, archive, []tgzEntry{{name: ".synced", dir: true}}, 0)
	if _, err := extractTarGz(archive, filepath.Join(t.TempDir(), "out")); err == nil ||
		!strings.Contains(err.Error(), ".synced") {
		t.Errorf("extractTarGz = %v, want .synced write error", err)
	}
}
