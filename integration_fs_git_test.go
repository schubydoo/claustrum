package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Increment B: hermetic coverage of the env-dependent namespaces (files.* and
// git.*). Each test builds a throwaway fixture under t.TempDir(), drives the
// methods over the real socket (serialized via call() so repo-mutating ops are
// deterministic), normalizes the tmp-root absolute paths to <DIR>, and locks
// the result against a committed golden.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

// runGit runs a setup git command against dir with a fully isolated identity and
// config, so neither the developer's global gitconfig nor system config can
// perturb the fixture (gpgsign, default branch, hooks, …).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // defeat umask for a stable stat mode
		t.Fatal(err)
	}
}

func makeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic archive order
	for _, n := range names {
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{
			Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// req builds an authed request line; params key order is irrelevant to the
// server, and json.Marshal escapes paths safely.
func req(id int, method string, params map[string]any) string {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "auth": testToken}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// normPath tokenizes the per-run temp root so the golden is host-stable. On
// Windows the root can appear in three spellings — native backslashes,
// git's forward slashes (rev-parse output), and JSON-escaped backslashes —
// so all three are replaced; on Unix the variants collapse to the same string.
func normPath(b json.RawMessage, tmpRoot string) json.RawMessage {
	s := string(b)
	for _, root := range []string{
		tmpRoot,
		filepath.ToSlash(tmpRoot),
		strings.ReplaceAll(tmpRoot, `\`, `\\`),
	} {
		s = strings.ReplaceAll(s, root, "<DIR>")
	}
	return json.RawMessage(s)
}

func TestSocketFilesBattery(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The golden pins the Unix reference capture, including files.stat's
		// mode string ("-rw-r--r--"); Windows stat reports its own modes.
		t.Skip("golden fixture pins Unix stat modes")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "hello.txt"), "hi\n", 0o644)
	writeFile(t, filepath.Join(root, "fdir", "a.txt"), "a", 0o644)
	writeFile(t, filepath.Join(root, "fdir", "b.txt"), "b", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "fdir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeTarGz(t, filepath.Join(root, "arc.tar.gz"), map[string]string{
		"one.txt": "1", "two.txt": "2",
	})

	sock := startSocketServer(t)
	cl := dial(t, sock)

	calls := []string{
		req(1, "files.stat", map[string]any{"path": filepath.Join(root, "hello.txt")}),
		req(2, "files.list", map[string]any{"path": filepath.Join(root, "fdir")}),
		req(3, "files.read", map[string]any{"path": filepath.Join(root, "hello.txt")}),
		req(4, "files.validate", map[string]any{"path": filepath.Join(root, "hello.txt")}),
		req(5, "files.validate", map[string]any{"path": filepath.Join(root, "nope")}),
		req(6, "files.extract_tar", map[string]any{
			"archivePath": filepath.Join(root, "arc.tar.gz"),
			"destDir":     filepath.Join(root, "extracted"),
		}),
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(cl.call(line), root)
	}
	assertGolden(t, "socket_files.golden.json", encodeGolden(t, got))
}

func TestSocketGitBattery(t *testing.T) {
	requireGit(t)
	// Canonicalize the temp root to the spelling git.info's "root" (git
	// rev-parse --show-toplevel) will report: symlink-resolved on macOS
	// (/var -> /private/var), 8.3 short names expanded on Windows
	// (C:\Users\RUNNER~1\...). No-op on Linux.
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "myrepo")
	runGit(t, root, "init", "-b", "main", "myrepo")
	writeFile(t, filepath.Join(repo, "README.md"), "hello\n", 0o644)
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	runGit(t, repo, "branch", "feature")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "x\n", 0o644) // untracked -> a status change
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}

	sock := startSocketServer(t)
	cl := dial(t, sock)

	// Serialized: list_branches must observe the repo BEFORE worktree_create
	// adds the "wt" branch. baseRepo is mandatory on worktree ops — omitting it
	// would fall back to the daemon cwd (this very repo) and leak a worktree.
	// Slash form: worktree_create echoes the request path verbatim in its
	// result, so a native backslash on Windows would leak past normPath into
	// the golden ("<DIR>\\wt"); git and the OS both accept forward slashes.
	wtPath := filepath.ToSlash(filepath.Join(root, "wt"))
	calls := []string{
		req(1, "git.info", map[string]any{"path": repo}),
		req(2, "git.status", map[string]any{"path": repo}),
		req(3, "git.list_branches", map[string]any{"path": repo}),
		req(4, "git.worktree_create", map[string]any{
			"baseRepo": repo, "branchName": "wt", "worktreePath": wtPath,
		}),
		req(5, "git.worktree_remove", map[string]any{"baseRepo": repo, "worktreePath": wtPath}),
		req(6, "git.info", map[string]any{"path": filepath.Join(root, "plain")}),
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(cl.call(line), root)
	}
	assertGolden(t, "socket_git.golden.json", encodeGolden(t, got))
}
