package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
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

// TestSocketTildeExpansion pins sweep gap W1: a leading `~` is expanded against
// the daemon user's home at every path binding point, and NOT expanded anywhere
// else. Every clause was probe-measured against the reference at 5db5e4a on
// 2026-07-30 — including two edges the sweep had not recorded ("~/" and "~//f")
// and the fact that results echo the EXPANDED path (files.list entry paths,
// git.info root, worktree_create path).
//
// Before this, claustrum treated `~` as a literal directory name: every call
// below failed or, worse, CREATED a literal `~` directory inside the user's
// repo (observed as "?? ~/" in git.status).
func TestSocketTildeExpansion(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		// The reference was measured on Unix; its behaviour for a "~\" form is
		// unmeasured, so expandPath deliberately handles only "~/" and this
		// golden pins the Unix spelling.
		t.Skip("tilde form is measured on Unix only")
	}
	// The temp root IS the home directory, so one token normalizes everything.
	home := resolveTestRoot(t, t.TempDir())
	t.Setenv("HOME", home)

	writeFile(t, filepath.Join(home, "f.txt"), "hello in home\n", 0o644)
	// A LITERAL "~" directory, to prove a mid-path tilde is a directory name.
	writeFile(t, filepath.Join(home, "~", "f.txt"), "literal tilde dir\n", 0o644)
	makeTarGz(t, filepath.Join(home, "arc.tar.gz"), map[string]string{"one.txt": "1"})
	repo := filepath.Join(home, "repo")
	runGit(t, home, "init", "-b", "main", "repo")
	writeFile(t, filepath.Join(repo, "a.txt"), "x\n", 0o644)
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "init")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "d\n", 0o644)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	calls := []string{
		// Expanded — one per binding point.
		req(1, "files.stat", map[string]any{"path": "~/f.txt"}),
		req(2, "files.validate", map[string]any{"path": "~/f.txt"}),
		req(3, "files.read", map[string]any{"path": "~/f.txt"}),
		req(4, "files.list", map[string]any{"path": "~"}), // entry paths echo expanded
		req(5, "files.extract_tar", map[string]any{
			"archivePath": "~/arc.tar.gz", "destDir": "~/dest",
		}),
		req(6, "git.info", map[string]any{"path": "~/repo"}), // root echoes expanded
		req(7, "git.status", map[string]any{"path": "~/repo"}),
		req(8, "git.list_branches", map[string]any{"path": "~/repo"}),
		req(9, "git.worktree_create", map[string]any{ // both params expand
			"baseRepo": "~/repo", "branchName": "wt", "worktreePath": "~/wt",
		}),
		// Bare "~" and a trailing slash both expand. Asserted with validate,
		// not stat: a directory's reported size is filesystem-dependent (140
		// here, 4096 on the ext4 CI runners), so a stat golden on a DIRECTORY
		// pins a value that is not ours to control. Files are fine — ids 1 and
		// 13 stat regular files, whose sizes are the bytes we wrote.
		req(10, "files.validate", map[string]any{"path": "~"}),
		req(11, "files.validate", map[string]any{"path": "~/"}),
		// NOT expanded.
		req(12, "files.stat", map[string]any{"path": "~nosuchuser/f.txt"}),
		req(13, "files.stat", map[string]any{"path": filepath.Join(home, "~", "f.txt")}),
		req(14, "files.stat", map[string]any{"path": "$HOME/f.txt"}),
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(cl.call(line), home)
	}
	assertGolden(t, "socket_tilde_expansion.golden.json", encodeGolden(t, got))

	// process.spawn's cwd is the tenth binding point. Its reply is a bare
	// {"success":true}, so the expansion is only observable in the child's
	// output — assert it separately rather than pretending the golden covers it.
	cl.call(spawnReqArgsCwd(t, 20, "TILDE", "pwd", "~/repo"))
	frames := cl.waitExit("TILDE")
	if out := strings.TrimSpace(streamBytes(t, frames, "stdout")); out != repo {
		t.Errorf("spawn cwd %q resolved to %q, want %q", "~/repo", out, repo)
	}
}

// spawnReqArgsCwd is spawnReqArgs with an explicit cwd.
func spawnReqArgsCwd(t *testing.T, id int, procID, mode, cwd string) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{
			"id": procID, "command": exe, "args": []string{}, "env": env, "cwd": cwd,
		},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// TestSocketStatErrorPropagation pins sweep gap W6: a stat failure that is NOT
// "does not exist" is reported, not flattened into exists:false.
//
// Three triggers, all probe-measured against the reference at 5db5e4a on
// 2026-07-30. ENOTDIR is the one that motivated the change — it needs no
// adversarial input, just a client joining a path against a regular file:
//
//	<dir>/a.txt/../a.txt   ENOTDIR        "stat <p>: not a directory"
//	a 300-character name   ENAMETOOLONG   "stat <p>: file name too long"
//	a NUL byte in the path EINVAL         "stat <p>: invalid argument"
//
// files.stat and files.read report these as -32603; files.validate keeps its own
// result shape and puts the stat text in the error field instead of "Path does
// not exist". The genuine-ENOENT rows are pinned alongside, because the change
// must NOT disturb them.
func TestSocketStatErrorPropagation(t *testing.T) {
	if runtime.GOOS == "windows" {
		// "not a directory" / "file name too long" / "invalid argument" are the
		// Unix errno strings the reference was measured against.
		t.Skip("golden pins Unix errno text")
	}
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "a.txt"), "content here\n", 0o644)

	// Built by concatenation, NOT filepath.Join: Join calls Clean, which
	// resolves "a.txt/../a.txt" back to "a.txt" — a path that exists, so the
	// ENOTDIR case would silently never fire. The kernel does no such cleaning;
	// it walks the components and fails on the file in the middle.
	notDir := root + "/a.txt/../a.txt"
	tooLong := filepath.Join(root, strings.Repeat("L", 300))
	withNUL := filepath.Join(root, "a\x00b")
	absent := filepath.Join(root, "missing")

	sock := startSocketServer(t)
	cl := dial(t, sock)
	var calls []string
	id := 0
	for _, m := range []string{"files.stat", "files.read", "files.validate"} {
		for _, path := range []string{notDir, tooLong, withNUL, absent} {
			id++
			calls = append(calls, req(id, m, map[string]any{"path": path}))
		}
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		// The 300-char component would otherwise make the golden unreadable.
		got[i] = json.RawMessage(strings.ReplaceAll(
			string(normPath(cl.call(line), root)), strings.Repeat("L", 300), "<LONG>"))
	}
	assertGolden(t, "socket_stat_error_propagation.golden.json", encodeGolden(t, got))
}

// TestSocketGitRepoDetection pins sweep gap W3: git.status and git.list_branches
// gate on `rev-parse --git-dir`, which succeeds for a BARE repo and from inside
// a `.git` directory, where `--is-inside-work-tree` reports false.
//
// git.info is deliberately NOT changed — it agreed with the reference in all
// four cases, so the helper is not blanket-swapped. All twelve rows below were
// measured against the reference at 5db5e4a on 2026-07-30.
func TestSocketGitRepoDetection(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	work := filepath.Join(root, "work")
	runGit(t, root, "init", "-b", "master", "work")
	writeFile(t, filepath.Join(work, "a.txt"), "x\n", 0o644)
	runGit(t, work, "add", "a.txt")
	runGit(t, work, "commit", "-m", "init")
	runGit(t, work, "branch", "wtbranch")
	runGit(t, root, "init", "-b", "master", "--bare", "bare.git")
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}

	sock := startSocketServer(t)
	cl := dial(t, sock)
	var calls []string
	id := 0
	for _, m := range []string{"git.info", "git.status", "git.list_branches"} {
		for _, dir := range []string{
			work,                            // ordinary work tree
			filepath.Join(root, "bare.git"), // bare: no work tree
			filepath.Join(work, ".git"),     // inside the .git directory
			filepath.Join(root, "plain"),    // not a repo at all
		} {
			id++
			calls = append(calls, req(id, m, map[string]any{"path": dir}))
		}
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(cl.call(line), root)
	}
	assertGolden(t, "socket_git_repo_detection.golden.json", encodeGolden(t, got))
}

// TestSocketWorktreeSourceBranch pins sweep gap W4: a sourceBranch is honoured
// ONLY when it names an existing local branch. Anything else is ignored and the
// worktree is created off HEAD, with the fallback reported in sourceBranch —
// where claustrum used to fail the whole request with git's "invalid reference".
//
// The accepted set is narrower than "a resolvable rev", which is why the fix
// tests refs/heads/<source> existence rather than `rev-parse --verify`. Measured
// against the reference at 5db5e4a; "diverged" is accepted although it is not an
// ancestor of HEAD.
func TestSocketWorktreeSourceBranch(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "master", "repo")
	writeFile(t, filepath.Join(repo, "a.txt"), "x\n", 0o644)
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "init")
	runGit(t, repo, "branch", "feature")
	runGit(t, repo, "tag", "v1")
	// A branch that diverges from master, so it is NOT an ancestor of HEAD.
	runGit(t, repo, "checkout", "-q", "-b", "diverged")
	writeFile(t, filepath.Join(repo, "b.txt"), "y\n", 0o644)
	runGit(t, repo, "add", "b.txt")
	runGit(t, repo, "commit", "-m", "diverge")
	runGit(t, repo, "checkout", "-q", "master")
	runGit(t, repo, "update-ref", "refs/remotes/origin/rbranch", "master")

	sock := startSocketServer(t)
	cl := dial(t, sock)
	// value == "" means the param is omitted entirely.
	sources := []string{"feature", "diverged", "no-such-branch", "HEAD",
		"refs/heads/feature", "v1", "origin/rbranch", ""}
	got := make([]json.RawMessage, 0, len(sources))
	for i, src := range sources {
		params := map[string]any{
			"baseRepo":     repo,
			"branchName":   fmt.Sprintf("w%d", i),
			"worktreePath": filepath.ToSlash(filepath.Join(root, fmt.Sprintf("wt%d", i))),
		}
		if src != "" {
			params["sourceBranch"] = src
		}
		got = append(got, normPath(cl.call(req(i+1, "git.worktree_create", params)), root))
	}
	assertGolden(t, "socket_worktree_source_branch.golden.json", encodeGolden(t, got))
}

// TestWorktreeResultFieldOrder pins sweep gap L1. No input populates
// sourceBranch together with error/errorCode, so the order is unreachable over
// the socket — assert it directly on the struct instead, because ordered result
// structs ARE the wire contract and an unreachable field order is still one that
// a future input could expose.
func TestWorktreeResultFieldOrder(t *testing.T) {
	b, err := json.Marshal(worktreeResult{
		Success: true, Path: "/p", Error: "e", ErrorCode: "c", SourceBranch: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"success":true,"path":"/p","error":"e","errorCode":"c","sourceBranch":"s"}`
	if string(b) != want {
		t.Errorf("worktreeResult field order:\n got %s\nwant %s", b, want)
	}
}

// TestSocketWorktreeRemoveBranch pins sweep gap F2: git.worktree_remove deletes
// the branch named by branchName, forcefully — an UNMERGED branch goes too.
// claustrum ignored the parameter entirely and left both branches behind.
//
// The reply is a bare {"success":true} either way, so this gap is invisible in
// the frame it returns. It is pinned through git.list_branches before and after,
// which is where a client would actually notice.
func TestSocketWorktreeRemoveBranch(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "master", "repo")
	writeFile(t, filepath.Join(repo, "a.txt"), "x\n", 0o644)
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "init")

	sock := startSocketServer(t)
	cl := dial(t, sock)
	wt := func(n string) string { return filepath.ToSlash(filepath.Join(root, n)) }

	// Two worktrees: one whose branch stays at master, one that diverges.
	cl.call(req(1, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "merged", "worktreePath": wt("wtm")}))
	cl.call(req(2, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "unmerged", "worktreePath": wt("wtu")}))
	writeFile(t, filepath.Join(root, "wtu", "z.txt"), "z\n", 0o644)
	runGit(t, filepath.Join(root, "wtu"), "add", "z.txt")
	runGit(t, filepath.Join(root, "wtu"), "commit", "-m", "diverge")

	got := []json.RawMessage{
		// before: master + both worktree branches
		normPath(cl.call(req(3, "git.list_branches", map[string]any{"path": repo})), root),
		normPath(cl.call(req(4, "git.worktree_remove", map[string]any{
			"baseRepo": repo, "worktreePath": wt("wtm"), "branchName": "merged"})), root),
		// the unmerged branch must go too — `git branch -d` would refuse it
		normPath(cl.call(req(5, "git.worktree_remove", map[string]any{
			"baseRepo": repo, "worktreePath": wt("wtu"), "branchName": "unmerged"})), root),
		// a branch that does not exist is still {"success":true}, not an error
		normPath(cl.call(req(6, "git.worktree_remove", map[string]any{
			"baseRepo": repo, "worktreePath": wt("nope"), "branchName": "ghost"})), root),
		// after: only master survives
		normPath(cl.call(req(7, "git.list_branches", map[string]any{"path": repo})), root),
	}
	assertGolden(t, "socket_worktree_remove_branch.golden.json", encodeGolden(t, got))
}

// TestWorktreeRemoveResultShape pins the declared reply shape. No probed input
// populates `error` — removing a nonexistent worktree still answers
// {"success":true} — but the field is part of the reference's declared struct,
// so its presence and order are asserted directly rather than left unpinned.
func TestWorktreeRemoveResultShape(t *testing.T) {
	b, err := json.Marshal(worktreeRemoveResult{Success: true, Error: "e"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"success":true,"error":"e"}`; string(b) != want {
		t.Errorf("worktreeRemoveResult = %s, want %s", b, want)
	}
	// omitempty: the reachable reply must stay a bare {"success":true}.
	b, _ = json.Marshal(worktreeRemoveResult{Success: true})
	if want := `{"success":true}`; string(b) != want {
		t.Errorf("worktreeRemoveResult (no error) = %s, want %s", b, want)
	}
}

// TestSocketWorktreeCreatePopulates proves the WIRING, not just the copy logic:
// git.worktree_create must actually seed the new worktree. TestPopulateWorktree
// calls populateWorktree directly, so it passes even if gitWorktreeCreate never
// invokes it — this test is what fails in that case.
//
// The reply is byte-identical either way (F1 is a filesystem-side gap), so the
// assertion is on the resulting tree, reached over the real socket.
func TestSocketWorktreeCreatePopulates(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX modes; see TestPopulateWorktree")
	}
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "master", "repo")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n", 0o644)
	runGit(t, repo, "add", "tracked.txt")
	writeFile(t, filepath.Join(repo, ".claude", "settings.json"), "{}\n", 0o644)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), "local.env\n", 0o644)
	writeFile(t, filepath.Join(repo, "local.env"), "K=V\n", 0o644)
	writeFile(t, filepath.Join(repo, "unlisted.txt"), "no\n", 0o644)
	runGit(t, repo, "commit", "-m", "init")

	sock := startSocketServer(t)
	cl := dial(t, sock)
	wt := filepath.ToSlash(filepath.Join(root, "wt"))
	reply := cl.call(req(1, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "wt", "worktreePath": wt}))
	if !strings.Contains(string(reply), `"success":true`) {
		t.Fatalf("worktree_create failed: %s", reply)
	}

	got := treeOf(t, filepath.Join(root, "wt"))
	want := []string{".claude", ".claude/settings.json", "local.env", "tracked.txt"}
	eqTree(t, got, want, "worktree_create over the socket")
}
