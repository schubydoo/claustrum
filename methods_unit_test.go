package main

import (
	"archive/tar"
	"compress/gzip"
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

// repoDir returns BaseRepo when set, otherwise the daemon CWD (".").
func TestGitRepoDir(t *testing.T) {
	if got := (&gitParams{BaseRepo: "/some/repo"}).repoDir(); got != "/some/repo" {
		t.Errorf("repoDir(BaseRepo) = %q, want /some/repo", got)
	}
	if got := (&gitParams{}).repoDir(); got != "." {
		t.Errorf("repoDir(empty) = %q, want .", got)
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

	// Unknown process id.
	got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "missing", "data": base64.StdEncoding.EncodeToString([]byte("x"))}))
	if !strings.Contains(got, "Process not found") {
		t.Errorf("stdin to missing = %s, want not-found error", got)
	}

	// Registered process but invalid base64 payload.
	s.procs.procs["p1"] = &managedProc{id: "p1", subs: map[*conn]struct{}{}}
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "p1", "data": "!!!not base64!!!"}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("stdin bad base64 = %s, want invalid-params error", got)
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
