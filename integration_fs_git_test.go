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

// TestSocketGitStatusPorcelain pins every XY status column shape git.status can
// emit. The battery above only ever produced "?? dirty.txt" — an untracked entry,
// the one shape that carries no leading space — so a trim of the porcelain line
// went unnoticed. The leading space is positional data: " M f" (unstaged) and
// "M  f" (staged) differ only in where the letter sits.
func TestSocketGitStatusPorcelain(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")

	// Committed baseline. rename_src.txt needs enough content for git's rename
	// detection to pair it with rename_dst.txt at 100% similarity.
	writeFile(t, filepath.Join(repo, "mod_unstaged.txt"), "one\n", 0o644)
	writeFile(t, filepath.Join(repo, "mod_both.txt"), "one\n", 0o644)
	writeFile(t, filepath.Join(repo, "del_unstaged.txt"), "one\n", 0o644)
	writeFile(t, filepath.Join(repo, "del_staged.txt"), "one\n", 0o644)
	writeFile(t, filepath.Join(repo, "rename_src.txt"), "rename me\nsecond line\nthird line\n", 0o644)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")

	// One mutation per XY shape.
	writeFile(t, filepath.Join(repo, "mod_unstaged.txt"), "two\n", 0o644) // " M"
	writeFile(t, filepath.Join(repo, "mod_both.txt"), "two\n", 0o644)     // staged half of "MM"
	runGit(t, repo, "add", "mod_both.txt")
	writeFile(t, filepath.Join(repo, "mod_both.txt"), "three\n", 0o644) // unstaged half of "MM"
	if err := os.Remove(filepath.Join(repo, "del_unstaged.txt")); err != nil {
		t.Fatal(err) // " D"
	}
	runGit(t, repo, "rm", "-q", "del_staged.txt")                     // "D "
	runGit(t, repo, "mv", "rename_src.txt", "rename_dst.txt")         // "R "
	writeFile(t, filepath.Join(repo, "staged_new.txt"), "n\n", 0o644) // "A "
	runGit(t, repo, "add", "staged_new.txt")
	writeFile(t, filepath.Join(repo, "untracked.txt"), "u\n", 0o644) // "??"

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		normPath(cl.call(req(1, "git.status", map[string]any{"path": repo})), root),
	}
	assertGolden(t, "socket_git_status.golden.json", encodeGolden(t, got))
}

// TestSocketErrorTextParity pins the reference's error text for three failures
// that claustrum reported differently (sweep gaps W5, W7, W8). All three are
// -32603/error-field strings a client may surface verbatim, so the wording is
// part of the contract.
//
//	W7 extract_tar, missing archive   ref: "open archive: open <p>: ..."
//	                                  was: "open <p>: ..."
//	W8 spawn, missing cwd             ref: "chdir <p>: stat <p>: no such file..."
//	   spawn, cwd is a regular file   ref: "chdir <p>: not a directory"
//	                                  was: "fork/exec <command>: ..." for both
//
// The two W8 strings differ in shape — the file case carries no "stat <p>:"
// segment. That was measured against the reference binary at 5db5e4a on
// 2026-07-30, not inferred from the missing-dir case.
//
// The two W5 cases live in their own tests below: each has a narrower
// precondition (linux-only op names, non-root), and folding them in here would
// make those preconditions skip these four contracts too.
func TestSocketErrorTextParity(t *testing.T) {
	if runtime.GOOS == "windows" {
		// no-such-file / not-a-directory are Unix wordings; Windows reports its
		// own. Nothing here depends on the running uid.
		t.Skip("golden pins Unix syscall error text")
	}
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "regular.txt"), "x\n", 0o644)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	calls := []string{
		req(4, "files.list", map[string]any{"path": filepath.Join(root, "missing")}),
		// W7
		req(5, "files.extract_tar", map[string]any{
			"archivePath": filepath.Join(root, "nosuch.tar.gz"),
			"destDir":     filepath.Join(root, "dest"),
		}),
		// W8 — the two shapes differ; both are pinned.
		req(6, "process.spawn", map[string]any{
			"id": "CWD1", "command": "/bin/pwd", "args": []string{},
			"cwd": filepath.Join(root, "missingdir"),
		}),
		req(7, "process.spawn", map[string]any{
			"id": "CWD2", "command": "/bin/pwd", "args": []string{},
			"cwd": filepath.Join(root, "regular.txt"),
		}),
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(cl.call(line), root)
	}
	assertGolden(t, "socket_error_text_parity.golden.json", encodeGolden(t, got))
}

// TestSocketListNonDirErrorText pins the W5 non-directory wording, which is
// linux-only because the syscall op name is platform-specific: Go reports
// "readdirent" on Linux and "fdopendir" on Darwin for the same failure.
//
// That is NOT a claustrum-vs-reference divergence. The reference daemon is also
// Go, so it goes through the same stdlib path and reports the same op on each
// platform; claustrum matches it per-OS. What is pinned here is that we reach
// the readdir stage at all — the bug this replaced reported an "open" error,
// because os.ReadDir collapses both failures into one PathError.
func TestSocketListNonDirErrorText(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall op name is platform-specific (linux readdirent / darwin fdopendir)")
	}
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "regular.txt"), "x\n", 0o644)
	if err := os.Symlink(filepath.Join(root, "regular.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		normPath(cl.call(req(1, "files.list", map[string]any{"path": filepath.Join(root, "regular.txt")})), root),
		normPath(cl.call(req(2, "files.list", map[string]any{"path": filepath.Join(root, "link")})), root),
	}
	assertGolden(t, "socket_list_nondir_linux.golden.json", encodeGolden(t, got))
}

// TestSocketListPermissionDeniedErrorText pins the remaining W5 case: an
// unreadable file must report the open failure ("permission denied"), where the
// old os.ReadDir path reported "not a directory" for it — describing the wrong
// problem entirely.
//
// Split out because it is the ONLY assertion in this group that depends on the
// running uid; keeping it inside the shared test made a root environment skip
// the extract_tar and spawn contracts too, which do not care who runs them.
func TestSocketListPermissionDeniedErrorText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not the access control on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("runs as root; mode 000 is readable so the open would succeed")
	}
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "noperm.txt"), "x\n", 0o000)
	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		normPath(cl.call(req(3, "files.list", map[string]any{"path": filepath.Join(root, "noperm.txt")})), root),
	}
	assertGolden(t, "socket_list_permission_denied.golden.json", encodeGolden(t, got))
}
