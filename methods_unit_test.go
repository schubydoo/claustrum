package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// rpcLine builds a single authenticated JSON-RPC request line. params is
// marshaled as-is (pass a map or struct); a nil params is sent as `{}` so the
// needParams presence gate is satisfied.
func rpcLine(t *testing.T, method string, params interface{}) string {
	t.Helper()
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  raw,
		Auth:    testToken,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

// files.read enforces maxBytes with a strict greater-than: a file whose size
// equals maxBytes must read successfully; one byte over must error. This locks
// the boundary so a `>`→`>=` mutation is caught.
func TestFilesReadMaxBytesBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	const payload = "0123456789" // 10 bytes
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()

	// maxBytes exactly equals the file size: allowed.
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path, "maxBytes": len(payload)}))
	if !strings.Contains(got, `"content":"`+payload+`"`) || !strings.Contains(got, `"exists":true`) {
		t.Errorf("read at boundary = %s, want content+exists", got)
	}

	// maxBytes one below the file size: rejected.
	got = dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path, "maxBytes": len(payload) - 1}))
	if !strings.Contains(got, "files.read: file exceeds maxBytes") {
		t.Errorf("read over maxBytes = %s, want exceeds error", got)
	}

	// maxBytes unset (0) disables the cap entirely.
	got = dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path}))
	if !strings.Contains(got, `"exists":true`) {
		t.Errorf("read without maxBytes = %s, want exists", got)
	}
}

func TestFilesReadDirectoryAndMissing(t *testing.T) {
	s := newTestServer()
	dir := t.TempDir()

	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": dir}))
	if !strings.Contains(got, "files.read: path is a directory") {
		t.Errorf("read dir = %s, want directory error", got)
	}

	// A missing path is not an error: it returns an empty (exists:false) result.
	missing := filepath.Join(dir, "nope")
	got = dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": missing}))
	if !strings.Contains(got, `"exists":false`) || strings.Contains(got, `"error"`) {
		t.Errorf("read missing = %s, want exists:false and no error", got)
	}
}

// files.read must reject non-regular files (character devices, FIFOs, sockets)
// without reading from them. Without this guard, os.ReadFile on /dev/urandom or
// /dev/zero loops until the process OOMs (probe-verified against reference binary).
func TestFilesReadRejectsNonRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("character devices not applicable on Windows")
	}
	fi, err := os.Stat("/dev/null")
	if err != nil || fi.Mode().IsRegular() {
		t.Skip("/dev/null unavailable or unexpectedly regular")
	}
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": "/dev/null"}))
	if !strings.Contains(got, "not a regular file") {
		t.Errorf("files.read(/dev/null) = %s, want not-a-regular-file error", got)
	}
}

// tarGzPath builds a gzip-tar from name→content entries (reusing the shared
// makeTarGz helper) and returns its path.
func tarGzPath(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	makeTarGz(t, path, entries)
	return path
}

func TestFilesExtractTarSuccess(t *testing.T) {
	s := newTestServer()
	archive := tarGzPath(t, map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"})
	dest := filepath.Join(t.TempDir(), "out")

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":true`) || !strings.Contains(got, `"fileCount":2`) {
		t.Fatalf("extract = %s, want success + fileCount:2", got)
	}
	for name, want := range map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"} {
		b, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(b) != want {
			t.Errorf("extracted %s = %q (err %v), want %q", name, b, err, want)
		}
	}
}

func TestFilesExtractTarErrors(t *testing.T) {
	s := newTestServer()
	good := tarGzPath(t, map[string]string{"a.txt": "x"})

	cases := []struct {
		name        string
		archivePath string
		destDir     string
		wantSub     string
	}{
		{"missing fields", "", "", "archivePath and destDir are required"},
		{"relative destDir", good, "relative/out", "destDir must be an absolute"},
		{"root destDir", good, "/", "destDir must be an absolute, non-root path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": tc.archivePath, "destDir": tc.destDir}))
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("extract = %s, want substring %q", got, tc.wantSub)
			}
		})
	}

	// A non-gzip archive yields the doubled-prefix gzip error ("gzip: gzip: ...")
	// that the reference daemon emits, surfaced in the result's error field.
	bad := filepath.Join(t.TempDir(), "not.gz")
	if err := os.WriteFile(bad, []byte("plain text, not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "dest")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": bad, "destDir": abs}))
	if !strings.Contains(got, `"success":false`) || !strings.Contains(got, "gzip: gzip:") {
		t.Errorf("extract bad gzip = %s, want success:false + doubled gzip prefix", got)
	}
}

// files.extract_tar carries reference side effects beyond the response frame:
// it wipes destDir first, forces owner-only modes (files 0600 / dirs 0700),
// drops an empty ".synced" marker at the root on success, and consumes the
// source archive. These are invisible to a frame-only diff, so lock them here.
func TestFilesExtractTarSideEffects(t *testing.T) {
	s := newTestServer()
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := tarGzPath(t, map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"})

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":true`) || !strings.Contains(got, `"fileCount":2`) {
		t.Fatalf("extract = %s, want success/fileCount:2 (.synced is not counted)", got)
	}

	// destDir was wiped: the stale file is gone.
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); err == nil {
		t.Error("stale file survived; destDir was not wiped")
	}
	// .synced marker: empty, at destDir root, NOT inside subdirs.
	si, err := os.Stat(filepath.Join(dest, ".synced"))
	if err != nil || si.Size() != 0 {
		t.Errorf(".synced marker = (%v, %v), want present and empty", si, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", ".synced")); err == nil {
		t.Error(".synced was written inside a subdir; want root only")
	}
	// Source archive consumed.
	if _, err := os.Stat(archive); err == nil {
		t.Error("source archive survived; reference consumes it")
	}
	// Owner-only modes (skip the numeric check on Windows, where Go's FileMode
	// bits don't map to POSIX permissions).
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dest, "a.txt")); fi != nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o, want 600", fi.Mode().Perm())
		}
		if di, _ := os.Stat(filepath.Join(dest, "sub")); di != nil && di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %o, want 700", di.Mode().Perm())
		}
	}
}

// extract_tar supports only regular files and directories: any other entry
// type (symlink, hardlink, device) aborts the whole extraction with fileCount 0
// and the reference's "unsupported tar entry type <c>: <name>" error, writing no
// .synced marker (earlier regular entries still land on disk).
func TestFilesExtractTarUnsupportedEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "sym.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := "data"
	if err := tw.WriteHeader(&tar.Header{Name: "real.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "link.txt", Linkname: "real.txt", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	s := newTestServer()
	dest := filepath.Join(t.TempDir(), "out")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":false`) ||
		!strings.Contains(got, `"fileCount":0`) ||
		!strings.Contains(got, "unsupported tar entry type 2: link.txt") {
		t.Fatalf("extract = %s, want unsupported-entry error with fileCount:0", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "real.txt")); err != nil {
		t.Error("real.txt should have been written before the error")
	}
	if _, err := os.Stat(filepath.Join(dest, ".synced")); err == nil {
		t.Error(".synced must not be written when extraction fails")
	}
}

// An archive entry that would escape destDir ("zip slip") is rejected with the
// reference daemon's exact error and fileCount 0, and nothing is written outside
// destDir. A "../" that resolves back inside destDir is still allowed.
func TestFilesExtractTarZipSlip(t *testing.T) {
	s := newTestServer()
	root := t.TempDir()
	dest := filepath.Join(root, "sub")

	// Escaping entry: rejected, and the target one level up is never created.
	archive := tarGzPath(t, map[string]string{"../escaped.txt": "pwned"})
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":false`) ||
		!strings.Contains(got, `"fileCount":0`) ||
		!strings.Contains(got, "unsafe path in archive: ../escaped.txt") {
		t.Fatalf("zip-slip extract = %s, want success:false/fileCount:0/unsafe-path error", got)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); err == nil {
		t.Error("zip-slip wrote a file outside destDir")
	}

	// Benign "../" that normalizes back inside destDir is permitted.
	ok := tarGzPath(t, map[string]string{"inner/../within.txt": "fine"})
	got = dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": ok, "destDir": filepath.Join(root, "dest2")}))
	if !strings.Contains(got, `"success":true`) {
		t.Errorf("benign ../ extract = %s, want success", got)
	}
	if b, err := os.ReadFile(filepath.Join(root, "dest2", "within.txt")); err != nil || string(b) != "fine" {
		t.Errorf("within.txt = %q (err %v), want fine", b, err)
	}
}

// extractTarGz must reject archives whose total uncompressed size exceeds the cap.
// A crafted .tar.gz can have a tiny compressed payload that expands to fill a disk;
// the cap bounds that damage. The var is overridden to a small value here so the test
// does not write gigabytes to disk.
func TestFilesExtractTarSizeLimit(t *testing.T) {
	old := maxExtractBytes
	maxExtractBytes = 1024
	defer func() { maxExtractBytes = old }()

	// Archive with a single 1025-byte file — one byte over the 1 KB cap.
	s := newTestServer()
	archive := tarGzPath(t, map[string]string{"big.bin": strings.Repeat("x", 1025)})
	dest := filepath.Join(t.TempDir(), "out")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":false`) || !strings.Contains(got, "size limit exceeded") {
		t.Errorf("extract over cap = %s, want success:false and size-limit error", got)
	}

	// Archive with a 1024-byte file — exactly at the cap — must succeed.
	archive2 := tarGzPath(t, map[string]string{"ok.bin": strings.Repeat("x", 1024)})
	dest2 := filepath.Join(t.TempDir(), "out2")
	got = dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive2, "destDir": dest2}))
	if !strings.Contains(got, `"success":true`) {
		t.Errorf("extract at cap = %s, want success", got)
	}
}

// files.list resolves isDir via Stat (following symlinks), matching the
// reference: a symlink to a directory is isDir:true, a symlink to a file is
// false, and a dangling symlink (Stat fails) is false.
func TestFilesListFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	s := newTestServer()
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(dir, "realdir"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "realfile"), []byte("x"), 0o644))
	must(os.Symlink("realdir", filepath.Join(dir, "to-dir")))
	must(os.Symlink("realfile", filepath.Join(dir, "to-file")))
	must(os.Symlink("nope", filepath.Join(dir, "dangling")))

	got := dispatchRaw(t, s, rpcLine(t, "files.list", map[string]any{"path": dir}))
	for _, want := range []string{
		`"name":"to-dir","path":"` + filepath.Join(dir, "to-dir") + `","isDir":true`,
		`"name":"to-file","path":"` + filepath.Join(dir, "to-file") + `","isDir":false`,
		`"name":"dangling","path":"` + filepath.Join(dir, "dangling") + `","isDir":false`,
		`"name":"realdir","path":"` + filepath.Join(dir, "realdir") + `","isDir":true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("files.list missing %q\n  in %s", want, got)
		}
	}
}

// On a non-repo path, git.info returns the bare {isRepo:false}, but git.status
// and git.list_branches return their FULL shapes (clean:false / branches:[]),
// matching the reference. git.worktree_create reports a clean not_a_repo error
// instead of leaking git's raw "not a git repository" output.
func TestGitNonRepoResults(t *testing.T) {
	s := newTestServer()
	dir := t.TempDir() // under /tmp — not a git repo

	for _, tc := range []struct{ method, wantResult string }{
		{"git.info", `"result":{"isRepo":false}}`},
		{"git.status", `"result":{"isRepo":false,"clean":false}}`},
		{"git.list_branches", `"result":{"isRepo":false,"branches":[]}}`},
	} {
		got := dispatchRaw(t, s, rpcLine(t, tc.method, map[string]any{"path": dir}))
		if !strings.Contains(got, tc.wantResult) {
			t.Errorf("%s on non-repo = %s, want result %s", tc.method, got, tc.wantResult)
		}
	}

	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": dir, "branchName": "b", "worktreePath": filepath.Join(dir, "wt"),
	}))
	if !strings.Contains(got, `"success":false`) ||
		!strings.Contains(got, `"error":"not a git repository"`) ||
		!strings.Contains(got, `"errorCode":"not_a_repo"`) {
		t.Errorf("worktree_create on non-repo = %s, want not_a_repo error", got)
	}
}

// git.info resolves the branch via symbolic-ref, which works on an unborn HEAD
// (empty repo → the init branch name) and lets a detached HEAD be reported as
// "detached:<short-sha>" — where `rev-parse --abbrev-ref HEAD` would fail (and
// previously leaked git's error text) or return "HEAD".
func TestGitInfoBranchEdges(t *testing.T) {
	requireGit(t)
	s := newTestServer()

	// Empty repo (no commits): the init branch name still resolves.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, empty, "init", "-b", "trunk")
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": empty})); !strings.Contains(got, `"branch":"trunk"`) {
		t.Errorf("empty-repo git.info = %s, want branch=trunk", got)
	}

	// Detached HEAD → "detached:<short-sha>".
	det := filepath.Join(t.TempDir(), "det")
	if err := os.MkdirAll(det, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, det, "init", "-b", "trunk")
	writeFile(t, filepath.Join(det, "a.txt"), "x", 0o644)
	runGit(t, det, "add", "-A")
	runGit(t, det, "commit", "-m", "init")
	sha, _ := git(det, "rev-parse", "--short", "HEAD")
	runGit(t, det, "-c", "advice.detachedHead=false", "checkout", sha)
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": det})); !strings.Contains(got, `"branch":"detached:`+sha+`"`) {
		t.Errorf("detached git.info = %s, want branch=detached:%s", got, sha)
	}
}

// worktree_create off an empty (unborn-HEAD) repo with no sourceBranch succeeds —
// git infers an orphan branch — and the result omits sourceBranch, rather than
// failing on the unresolvable HEAD.
func TestGitWorktreeCreateEmptyRepo(t *testing.T) {
	requireGit(t)
	s := newTestServer()
	base := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-b", "trunk")
	wt := filepath.Join(t.TempDir(), "wt")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "newbr", "worktreePath": wt,
	}))
	if !strings.Contains(got, `"success":true`) {
		t.Errorf("worktree off empty repo = %s, want success", got)
	}
	if strings.Contains(got, `"sourceBranch"`) {
		t.Errorf("worktree off empty repo should omit sourceBranch: %s", got)
	}
}

// repoDir returns BaseRepo when set, otherwise the daemon CWD (".").
func TestGitRepoDir(t *testing.T) {
	if got := (&gitParams{BaseRepo: "/some/repo"}).repoDir(); got != "/some/repo" {
		t.Errorf("repoDir(BaseRepo) = %q, want /some/repo", got)
	}
	if got := (&gitParams{}).repoDir(); got != "." {
		t.Errorf("repoDir(empty) = %q, want .", got)
	}
}

// gitContext must report failure (ok=false) when its context is already done —
// the guard that keeps a wedged git from hanging a request goroutine forever. An
// already-cancelled context makes exec.CommandContext refuse to start, so this is
// deterministic and never spawns a real git.
func TestGitContextTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, ok := gitContext(ctx, ".", "rev-parse", "--is-inside-work-tree"); ok {
		t.Errorf("gitContext(cancelled) = (%q, true), want ok=false", out)
	}
}

// The procManager mutators must be no-ops (not panics) for unknown ids and
// procs that never got a running command/stdin.
func TestProcManagerEdgeCases(t *testing.T) {
	m := newProcManager()

	if m.writeStdin("missing", []byte("x")) {
		t.Error("writeStdin(missing) = true, want false")
	}
	// A proc with no stdin pipe rejects writes without panicking.
	m.procs["nostdin"] = &managedProc{id: "nostdin", subs: map[*conn]struct{}{}}
	if m.writeStdin("nostdin", []byte("x")) {
		t.Error("writeStdin(no stdin) = true, want false")
	}

	// kill on an unknown id and on a proc with no started process are both no-ops.
	m.kill("missing", "TERM")
	m.kill("nostdin", "KILL")

	// killAll over procs that never started must not panic either.
	m.killAll()
}

// detachConn unsubscribes a conn from every managed proc's replay fan-out.
func TestProcManagerDetachConn(t *testing.T) {
	m := newProcManager()
	c, _ := pipeConn(t)
	p := &managedProc{id: "p", subs: map[*conn]struct{}{c: {}}}
	m.procs["p"] = p

	m.detachConn(c)
	p.mu.Lock()
	_, still := p.subs[c]
	p.mu.Unlock()
	if still {
		t.Error("detachConn left the conn subscribed")
	}
}

func TestProcessSpawnValidation(t *testing.T) {
	s := newTestServer()

	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"command": "echo"}))
	if !strings.Contains(got, "Process ID is required") {
		t.Errorf("spawn without id = %s, want id-required error", got)
	}
	got = dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"id": "p1"}))
	if !strings.Contains(got, "Command is required") {
		t.Errorf("spawn without command = %s, want command-required error", got)
	}
}

func TestProcessStdinErrors(t *testing.T) {
	s := newTestServer()
	goodB64 := base64.StdEncoding.EncodeToString([]byte("x"))

	// Unknown process id (valid payload) → Process not found.
	got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "missing", "data": goodB64}))
	if !strings.Contains(got, "Process not found") {
		t.Errorf("stdin to missing = %s, want not-found error", got)
	}

	// Invalid base64 is rejected BEFORE the process lookup (probe-verified
	// precedence): even an unknown id reports the decode error, not not-found.
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "missing", "data": "!!!not base64!!!"}))
	if !strings.Contains(got, "Invalid base64 data") {
		t.Errorf("stdin bad base64 (unknown id) = %s, want Invalid base64 data", got)
	}

	// Registered but NOT running (running:false) → Process not running.
	s.procs.procs["p1"] = &managedProc{id: "p1", subs: map[*conn]struct{}{}, running: false}
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "p1", "data": goodB64}))
	if !strings.Contains(got, "Process not running") {
		t.Errorf("stdin to exited = %s, want not-running error", got)
	}
	// ...and bad base64 still wins over the running check (decode is first).
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "p1", "data": "!!!not base64!!!"}))
	if !strings.Contains(got, "Invalid base64 data") {
		t.Errorf("stdin bad base64 (not-running id) = %s, want Invalid base64 data", got)
	}
}

func TestProcessReattachValidation(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"fromSeq": 0}))
	if !strings.Contains(got, "Process ID is required") {
		t.Errorf("reattach without id = %s, want id-required error", got)
	}
}

// Each namespace handler routes an unrecognized method to the shared
// "Unknown method" error (its switch default).
func TestRoutingUnknownMethodPerNamespace(t *testing.T) {
	s := newTestServer()
	for _, m := range []string{"files.bogus", "git.bogus", "process.bogus"} {
		got := dispatchRaw(t, s, rpcLine(t, m, map[string]any{"x": 1}))
		if want := "Unknown method: " + m; !strings.Contains(got, want) {
			t.Errorf("dispatch %s = %s, want %q", m, got, want)
		}
	}
}

// Each namespace handler rejects a known method that arrives with no params
// object via the shared needParams gate.
func TestRoutingMissingParamsPerNamespace(t *testing.T) {
	s := newTestServer()
	auth := `"auth":"` + testToken + `"`
	for _, m := range []string{"files.read", "git.info", "process.spawn"} {
		line := `{"jsonrpc":"2.0","id":1,"method":"` + m + `",` + auth + `}`
		got := dispatchRaw(t, s, line)
		if !strings.Contains(got, "Invalid params") {
			t.Errorf("dispatch %s without params = %s, want Invalid params", m, got)
		}
	}
}

// extractTarGz must materialize TypeDir entries (without counting them) and only
// tally regular files, matching the reference daemon's fileCount semantics.
func TestExtractTarGzDirEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "d.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "dir/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	body := "inside"
	if err := tw.WriteHeader(&tar.Header{Name: "dir/f.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(t.TempDir(), "out")
	count, err := extractTarGz(archive, dest)
	if err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	if count != 1 { // only the regular file is counted, not the directory entry
		t.Errorf("fileCount = %d, want 1", count)
	}
	if fi, err := os.Stat(filepath.Join(dest, "dir")); err != nil || !fi.IsDir() {
		t.Errorf("dir entry not materialized: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "dir", "f.txt")); err != nil || string(b) != body {
		t.Errorf("nested file = %q (err %v), want %q", b, err, body)
	}
}

// detectLibc's fallback branch: when `ldd` can't be executed, it probes for the
// musl loader and otherwise reports glibc. Emptying PATH forces that branch.
func TestDetectLibcLddMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a dir with no `ldd`
	if got := detectLibc(); got != "glibc" && got != "musl" {
		t.Errorf("detectLibc() with no ldd = %q, want glibc or musl", got)
	}
}

// A .zst that decompresses to a non-executable file fails the post-install
// `<cli> --version` runnable check and is removed.
func TestEnsureCLINotRunnable(t *testing.T) {
	zstFile := filepath.Join(t.TempDir(), "cli.zst")
	// Plain text with no shebang: chmod +x can't make it runnable.
	if err := os.WriteFile(zstFile, zstdOf(t, []byte("not an executable")), 0o644); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(t.TempDir(), "claude")
	err := ensureCLI(installOpts{cliVersion: "v1", cliZst: zstFile}, cliPath)
	if err == nil || !strings.Contains(err.Error(), "not runnable") {
		t.Fatalf("ensureCLI(not runnable) err = %v, want not-runnable error", err)
	}
	if _, statErr := os.Stat(cliPath); statErr == nil {
		t.Error("non-runnable cli was left in place; want it removed")
	}
}

// --- error-path coverage ---

// extractTarGz: os.Open fails when the archive doesn't exist.
func TestExtractTarGzMissingArchive(t *testing.T) {
	_, err := extractTarGz(filepath.Join(t.TempDir(), "nonexistent.tar.gz"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing archive, got nil")
	}
}

// extractTarGz: valid gzip wrapping non-tar content causes tr.Next() to return
// a non-EOF error, which is wrapped with the "gzip: " prefix.
func TestExtractTarGzCorruptTar(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("not valid tar")); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := extractTarGz(archive, t.TempDir())
	if err == nil || !strings.HasPrefix(err.Error(), "gzip: ") {
		t.Fatalf("extractTarGz corrupt tar = %v, want gzip-prefixed error", err)
	}
}

// process.spawn: a JSON type mismatch in params hits the bindParams error path.
func TestProcessSpawnBindParamsError(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"id": "p1", "command": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("spawn with wrong command type = %s, want Invalid params", got)
	}
}

// process.spawn: valid params but the binary doesn't exist → codeInternal error.
func TestProcessSpawnFailed(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{
		"id": "p_err", "command": "/nonexistent_binary_xyz_claustrum",
	}))
	if !strings.Contains(got, `"error"`) {
		t.Errorf("spawn nonexistent binary = %s, want error result", got)
	}
}

// process.kill: a JSON type mismatch in params hits the bindParams error path.
func TestProcessKillBindParamsError(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "process.kill", map[string]any{"id": "p1", "signal": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("kill with wrong signal type = %s, want Invalid params", got)
	}
}

// process.reattach: a JSON type mismatch in params hits the bindParams error path.
func TestProcessReattachBindParamsError(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "p1", "fromSeq": "nope"}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("reattach with wrong fromSeq type = %s, want Invalid params", got)
	}
}

// git.worktree_create: trying to create a worktree with a branch that already
// exists causes git worktree add to fail → worktree_add_failed error code.
func TestGitWorktreeCreateFailed(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f")
	runGit(t, repo, "commit", "-m", "init")
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "main", "worktreePath": filepath.Join(root, "wt"),
	}))
	if !strings.Contains(got, "worktree_add_failed") {
		t.Errorf("worktree_create with existing branch = %s, want worktree_add_failed", got)
	}
}

// git.worktree_remove: a JSON type mismatch in params hits the bindParams error path.
func TestGitWorktreeRemoveBindParamsError(t *testing.T) {
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{"worktreePath": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("worktree_remove wrong type = %s, want Invalid params", got)
	}
}

// zstdDecompress: os.Create fails when the destination parent directory doesn't exist.
func TestZstdDecompressDestError(t *testing.T) {
	err := zstdDecompress(zstdOf(t, []byte("payload")), filepath.Join(t.TempDir(), "nonexistent", "out"))
	if err == nil {
		t.Fatal("expected error for missing parent dir, got nil")
	}
}
