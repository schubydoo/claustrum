package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
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
	s := newTestServer(t)

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

	// maxBytes unset does NOT disable the cap — it falls back to
	// defaultReadMaxBytes, which this 10-byte payload is comfortably under.
	// TestFilesReadDefaultMaxBytes covers the fallback boundary itself.
	got = dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path}))
	if !strings.Contains(got, `"exists":true`) {
		t.Errorf("read without maxBytes = %s, want exists", got)
	}
}

// An absent, zero, or negative maxBytes falls back to defaultReadMaxBytes rather
// than reading without limit. Probe-measured against the reference at 5db5e4a:
// 262144 bytes reads, 262145 errors, and an explicit positive maxBytes is
// honored verbatim above the default. Before this was fixed claustrum returned
// the whole file for every one of the "over" cases below.
func TestFilesReadDefaultMaxBytes(t *testing.T) {
	dir := t.TempDir()
	atCap := filepath.Join(dir, "at.txt")
	overCap := filepath.Join(dir, "over.txt")
	if err := os.WriteFile(atCap, bytes.Repeat([]byte("a"), defaultReadMaxBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overCap, bytes.Repeat([]byte("a"), defaultReadMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)

	// Exactly at the default cap: allowed, for every spelling of "no maxBytes".
	for _, params := range []map[string]any{
		{"path": atCap},
		{"path": atCap, "maxBytes": 0},
		{"path": atCap, "maxBytes": -1},
	} {
		if got := dispatchRaw(t, s, rpcLine(t, "files.read", params)); !strings.Contains(got, `"exists":true`) {
			t.Errorf("read at default cap %v = %s, want exists", params, got)
		}
	}

	// One byte over: rejected, for every spelling of "no maxBytes".
	for _, params := range []map[string]any{
		{"path": overCap},
		{"path": overCap, "maxBytes": 0},
		{"path": overCap, "maxBytes": -1},
	} {
		got := dispatchRaw(t, s, rpcLine(t, "files.read", params))
		if !strings.Contains(got, "files.read: file exceeds maxBytes") {
			// Truncate: the failure mode IS a 256 KiB body, so printing it whole
			// buries the assertion in the log.
			t.Errorf("read over default cap %v = %.120s… (%d bytes), want exceeds error", params, got, len(got))
		}
	}

	// An explicit positive maxBytes above the default is honored verbatim: the
	// default is a fallback, not a ceiling.
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": overCap, "maxBytes": defaultReadMaxBytes * 4}))
	if !strings.Contains(got, `"exists":true`) {
		t.Errorf("read with explicit large maxBytes = %.80s…, want exists", got)
	}
}

func TestFilesReadDirectoryAndMissing(t *testing.T) {
	s := newTestServer(t)
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

// D4, both arms on one input. Opted in, files.read refuses a character device
// without reading from it — that guard exists because os.ReadFile on /dev/urandom
// or /dev/zero never reaches EOF and grows until the process OOMs. At the SHIPPED
// default it is off, and /dev/null reads as the reference reads it:
// {"content":"","exists":true}. That row is the divergence D4's flip removed, so
// it is asserted rather than left to the socket golden alone.
//
// /dev/null is deliberately the input for both arms: it is the only non-regular
// shape whose unguarded read terminates with a SUCCESS frame and needs no fixture
// partner, so the off arm can assert a reply instead of a timeout. (Not the only
// one that terminates at all — a paired FIFO returns content and a socket or
// unreadable device returns -32603; both need a fixture this unit test has no
// business building.) The blocking shape is covered in
// integration_fifo_unix_test.go, where a writer is supplied.
func TestFilesReadNonRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("character devices not applicable on Windows")
	}
	fi, err := os.Stat("/dev/null")
	if err != nil || fi.Mode().IsRegular() {
		t.Skip("/dev/null unavailable or unexpectedly regular")
	}
	old := filesReadRegularOnly
	t.Cleanup(func() { filesReadRegularOnly = old })
	s := newTestServer(t)

	filesReadRegularOnly = false
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": "/dev/null"}))
	if !strings.Contains(got, `"content":"","exists":true`) || strings.Contains(got, `"error"`) {
		t.Errorf("files.read(/dev/null) off = %s, want the reference's empty-content result", got)
	}

	filesReadRegularOnly = true
	got = dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": "/dev/null"}))
	if !strings.Contains(got, "not a regular file") {
		t.Errorf("files.read(/dev/null) on = %s, want not-a-regular-file error", got)
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
	s := newTestServer(t)
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

// isFilesystemRoot must recognise the platform's OWN root, not just "/".
//
// The gate it backs guards an os.RemoveAll of destDir, so a root that slips
// through recursively deletes the volume. The previous spelling compared
// against the literal "/", which a Windows volume root never equals — `C:\`
// cleans to itself and passes filepath.IsAbs, so it reached the wipe.
//
// The Windows rows are the point of this test and can only fail on the Windows
// leg; the Unix rows keep it honest there.
func TestIsFilesystemRoot(t *testing.T) {
	var roots, nonRoots []string
	if runtime.GOOS == "windows" {
		roots = []string{`C:\`, `C:\\`, `\\srv\share`}
		nonRoots = []string{`C:\tmp`, `C:\tmp\out`, `\\srv\share\sub`}
	} else {
		roots = []string{"/", "//", "/."}
		nonRoots = []string{"/tmp", "/tmp/out", "/tmp/"}
	}
	for _, r := range roots {
		if !isFilesystemRoot(r) {
			t.Errorf("isFilesystemRoot(%q) = false, want true — a root destDir reaches os.RemoveAll", r)
		}
	}
	for _, n := range nonRoots {
		if isFilesystemRoot(n) {
			t.Errorf("isFilesystemRoot(%q) = true, want false — this would refuse a legitimate destDir", n)
		}
	}
}

// rootDestDirForOS is the platform's own filesystem root, so the "root destDir"
// row below exercises the gate END TO END on every leg.
//
// It used to be the literal "/", which on Windows is not a root at all — the row
// still passed there (a relative path is also refused, by the IsAbs half), so
// nothing asserted that filesExtractTar reaches the root check on the platform
// where that check was broken. TestIsFilesystemRoot covers the predicate; this
// covers the wiring.
func rootDestDirForOS() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

func TestFilesExtractTarErrors(t *testing.T) {
	s := newTestServer(t)
	good := tarGzPath(t, map[string]string{"a.txt": "x"})

	cases := []struct {
		name        string
		archivePath string
		destDir     string
		wantSub     string
	}{
		{"missing fields", "", "", "archivePath and destDir are required"},
		{"relative destDir", good, "relative/out", "destDir must be an absolute"},
		// The archive is DELIBERATELY nonexistent on this row. The destDir gate runs
		// before the archive is opened, so with the guard holding the row is
		// unaffected — but if the guard ever regresses, extraction fails on the
		// missing archive instead of unpacking into a real filesystem root. The
		// wipe seam stops the RemoveAll; this stops the writes that would follow it.
		// Together they make "run this without the fix" a safe thing to do.
		{"root destDir", filepath.Join(t.TempDir(), "no-such.tar.gz"), rootDestDirForOS(), "destDir must be an absolute, non-root path"},
	}
	// Stub the wipe for EVERY row. The root row sends a real filesystem root —
	// C:\ on Windows — so if isFilesystemRoot ever stops holding, an unstubbed
	// run would answer that by recursively deleting the CI runner. The stub also
	// makes the assertion sharper than "an error came back": the guard must
	// refuse BEFORE any filesystem effect, which is what wiped records.
	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(path string) error { wiped = path; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wiped = ""
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": tc.archivePath, "destDir": tc.destDir}))
			if wiped != "" {
				t.Errorf("destDir %q was refused but the wipe still ran on %q — the guard must "+
					"reject before any filesystem effect", tc.destDir, wiped)
			}
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
	s := newTestServer(t)
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

	s := newTestServer(t)
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
	s := newTestServer(t)
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

// The zip-slip guard across the entry shapes an attacker actually sends.
//
// Asserts ONE property: nothing lands outside destDir. Deliberately NOT whether
// each shape is accepted or rejected — which shapes the reference accepts is a
// parity question that has not been measured for most of these, and pinning an
// unmeasured accept/reject here would dress a guess up as a contract.
//
// This exists because CodeQL alert 8 (go/zipslip) reappeared on main without the
// guard changing: its text is byte-identical to 896fd5c, the commit CodeQL
// itself marked as fixing it, and the commit GitHub attributes the reappearance
// to (#167) does not touch this file at all. So the reappearance is an analysis
// change, not a regression — and that conclusion needs evidence stronger than
// reading the guard, which is what this table provides. It is also the safety
// net for any future rewrite of the guard into a form CodeQL recognises.
//
// WHICH ROWS ACTUALLY BITE, established by mutation rather than assumed — do not
// cite this as "seven adversarial shapes, all pinned":
//
//	guard deleted           rows 1-4 fail (a real file lands outside destDir)
//	naive HasPrefix guard   ONLY row 4 fails — and the older
//	                        TestFilesExtractTarZipSlip passes, a false green
//	guard rejects everything all seven pass
//
// So row 4 is the row that earns this test: a prefix-based rewrite is the most
// likely way a future "make CodeQL recognise it" attempt goes wrong, and row 4
// is the only thing in the suite that catches it. Rows 5-7 are controls, not
// escapes — `/absolute.txt` cannot escape at all, because filepath.Join cleans
// the leading slash before the guard is ever consulted.
//
// The last bullet is the honest limit: this table is a ONE-SIDED oracle. It
// asserts only the safety direction, so over-rejection is caught elsewhere
// (TestFilesExtractTarZipSlip's benign-"../" case and TestFilesExtractTarSuccess).
func TestFilesExtractTarZipSlipShapes(t *testing.T) {
	for _, entry := range []string{
		"../escaped.txt",               // 1: one level up
		"../../../../escaped-deep.txt", // 2: deep traversal, still inside root (see below)
		"a/b/../../../escape.txt",      // 3: traversal hidden behind a legitimate prefix
		"../sub-sibling.txt",           // 4: sibling whose name PREFIXES destDir's
		"/absolute.txt",                // 5: control — Join cleans it, cannot escape
		"./ok.txt",                     // 6: control — current-dir form
		"inner/../within.txt",          // 7: control — in-bounds "..", resolves inside
	} {
		t.Run(entry, func(t *testing.T) {
			s := newTestServer(t)
			root := t.TempDir()
			// destDir is nested deep on purpose. With dest directly under root, row
			// 2 resolved ABOVE root, so the WalkDir below could not see its escape
			// and the row could never fail — it passed here only because a non-root
			// test user got EACCES writing to /. Under a root CI container it would
			// have written /escaped-deep.txt on the runner and still reported PASS.
			// Four levels up from here is still inside root, so the escape is
			// observable and the litter stays in the temp tree.
			dest := filepath.Join(root, "a", "b", "c", "d", "sub")
			// Assert the depth rather than trust the comment above it. Row 2 is
			// observable only while four ".." from dest stay inside the walked
			// root; there is exactly one level of slack. Shortening dest must fail
			// HERE, loudly, rather than silently turning that row back into one
			// that can never fail.
			if probe := filepath.Join(dest, "../../../../probe"); !strings.HasPrefix(probe, root+string(os.PathSeparator)) {
				t.Fatalf("dest %q is too shallow: four levels up reaches %q, outside the walked root %q — "+
					"the deep-traversal row would be unobservable", dest, probe, root)
			}
			archive := tarGzPath(t, map[string]string{entry: "payload"})

			// The reply is not asserted: accept and reject are both fine here, so
			// long as the filesystem outside destDir is untouched.
			_ = dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
				map[string]any{"archivePath": archive, "destDir": dest}))

			var stray []string
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil //nolint:nilerr // a missing tree just means nothing was written
				}
				if !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
					stray = append(stray, p)
				}
				return nil
			})
			if len(stray) > 0 {
				t.Errorf("entry %q escaped destDir, wrote: %v", entry, stray)
			}
		})
	}
}

// Two behaviours of the up-then-back-in shape, both MEASURED against 5db5e4a on
// 2026-08-03 rather than assumed. They were an assumption until then, and the
// assumption was load-bearing: PR #224 rewrote the guard in a way that rejected
// the first row, and nothing in the suite noticed.
//
//	entry "../sub/inside.txt"	reference ACCEPTS, writes sub/inside.txt
//	entry "../sub"          	reference errors "create ../sub: open <dest>: is a
//	                        	directory" — note the "create <entry>: " prefix,
//	                        	which claustrum omitted
//
// The entry must name destDir's OWN basename to leave and re-enter it, which is
// why a differential over randomly chosen names cannot find this shape.
func TestFilesExtractTarUpThenBackIn(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	dest := filepath.Join(root, "sub")

	// Climbs out of destDir and back in: normalises inside, so it is accepted
	// and the file lands in destDir.
	archive := tarGzPath(t, map[string]string{"../sub/inside.txt": "payload"})
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
		map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":true`) || !strings.Contains(got, `"fileCount":1`) {
		t.Fatalf("up-then-back-in extract = %s, want success + fileCount:1 (the reference accepts it)", got)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "inside.txt")); err != nil || string(b) != "payload" {
		t.Errorf("inside.txt = %q (err %v), want payload inside destDir", b, err)
	}

	// The bare form resolves onto destDir itself, which is a directory, so the
	// create fails — and the reference names the ENTRY in the prefix.
	dest2 := filepath.Join(root, "sub2")
	archive2 := tarGzPath(t, map[string]string{"../sub2": "payload"})
	got = dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
		map[string]any{"archivePath": archive2, "destDir": dest2}))
	for _, want := range []string{`"success":false`, `"fileCount":0`, "create ../sub2: ", "is a directory"} {
		if !strings.Contains(got, want) {
			t.Errorf("bare up-then-back-in = %s, missing %q", got, want)
		}
	}
}

// The OTHER create-failure prefix, and the one that shows the two are not the
// same string. An archive whose FIRST entry writes a regular file "p" and whose
// second needs "p" as a directory fails the parent mkdir — a blocker the destDir
// wipe cannot remove, because the archive itself creates it.
//
// Measured against 5db5e4a:
//
//	reference : mkdir parent p/child.txt: mkdir <dest>/p: not a directory, fileCount 0
//	claustrum : mkdir <dest>/p: not a directory,                          fileCount 1
//
// fileCount 0 with one entry already on disk is the same shape the zip-slip
// rejection has: the reference reports nothing extracted even when earlier
// entries were written.
func TestFilesExtractTarMkdirParentPrefix(t *testing.T) {
	s := newTestServer(t)
	dest := filepath.Join(t.TempDir(), "sub")
	// Entry ORDER is the fixture: "p" must be written before "p/child.txt", or
	// the mkdir succeeds and nothing is tested. makeTarGz sorts entry names and
	// "p" sorts before "p/child.txt", so the map form is safe here — but the
	// dependency is real, so do not switch this to an unsorted writer.
	archive := tarGzPath(t, map[string]string{"p": "blocker", "p/child.txt": "payload"})

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
		map[string]any{"archivePath": archive, "destDir": dest}))
	// NOT the errno text. "not a directory" is the POSIX rendering of ENOTDIR;
	// Windows renders the same failure "The system cannot find the path
	// specified." and this assertion turned the windows-latest leg red. The OS
	// string is not what this PR adds — the prefix and the count are.
	for _, want := range []string{`"success":false`, `"fileCount":0`, "mkdir parent p/child.txt: "} {
		if !strings.Contains(got, want) {
			t.Errorf("mkdir-parent failure = %s, missing %q", got, want)
		}
	}
}

// fileCount is 0 on a create failure even when an EARLIER entry was already
// written — the partial count is not reported. Measured against 5db5e4a with
// an archive whose first entry succeeds ("!ok.txt" sorts before "../sub",
// 0x21 < 0x2E, so makeTarGz's sort puts it first) and whose second lands on
// destDir itself:
//
//	reference : create ../sub: open <dest>: is a directory, fileCount 0
//	claustrum : (before this change)                        fileCount 1
//
// The single-entry fixture above cannot see this: with nothing written first,
// the partial count IS 0 and both spellings agree.
func TestFilesExtractTarCreateFailureReportsZeroCount(t *testing.T) {
	s := newTestServer(t)
	dest := filepath.Join(t.TempDir(), "sub")
	archive := tarGzPath(t, map[string]string{"!ok.txt": "payload", "../sub": "x"})

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
		map[string]any{"archivePath": archive, "destDir": dest}))
	for _, want := range []string{`"success":false`, `"fileCount":0`, "create ../sub: "} {
		if !strings.Contains(got, want) {
			t.Errorf("create failure after a successful entry = %s, missing %q", got, want)
		}
	}
}

// A cap of MaxInt64 must behave like a very large cap, not like a zero-byte one.
// The bound is maxExtractBytes-totalWritten+1, and Go WRAPS signed overflow: at
// MaxInt64 that sum used to become MinInt64, io.LimitReader returns EOF for any
// N <= 0, and io.Copy does not report EOF as an error — so every entry was
// created at 0 bytes, totalWritten stayed 0 so the cap check never fired, and the
// reply was success:true over a destDir of empty files. Silent data loss reported
// as success, reachable through -max-extract-bytes and the claustrum.conf key
// the moment the cap became settable.
func TestFilesExtractTarCapMaxInt64DoesNotOverflow(t *testing.T) {
	old := maxExtractBytes
	maxExtractBytes = math.MaxInt64
	defer func() { maxExtractBytes = old }()

	s := newTestServer(t)
	const body = "not empty"
	archive := tarGzPath(t, map[string]string{"f.bin": body})
	dest := filepath.Join(t.TempDir(), "out")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":true`) {
		t.Fatalf("extract with a MaxInt64 cap = %s, want success", got)
	}
	// The assertion that matters: success alone was TRUE while the bug was live.
	b, err := os.ReadFile(filepath.Join(dest, "f.bin"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(b) != body {
		t.Errorf("extracted %d bytes (%q), want %d (%q) — the cap bound overflowed", len(b), b, len(body), body)
	}
}

// cappedCopy's bound is `maxExtractBytes - totalWritten`, then +1 so the caller's
// `totalWritten > maxExtractBytes` test can fire at all. The SUBTRACTION is what
// makes the cap apply to the archive as a whole rather than to each entry: it is
// the running total already written that shrinks the next entry's allowance.
//
// A single-entry fixture cannot see that operator. At totalWritten == 0 the bound
// is the same whether the code subtracts or adds, so the multi-entry case is the
// only one that distinguishes them — with the cap at 10 and 4 bytes already
// written, subtracting bounds the next read at 7 (6+1) while adding bounds it at
// 15, letting an archive overrun a cap the operator set.
func TestFilesExtractTarCapCountsBytesAlreadyWritten(t *testing.T) {
	old := maxExtractBytes
	maxExtractBytes = 10
	defer func() { maxExtractBytes = old }()

	// 20 source bytes against a 10-byte cap with 4 already written: enough to
	// overrun either bound, so the returned count IS the bound under test.
	src := bytes.NewReader(bytes.Repeat([]byte("z"), 20))
	var out bytes.Buffer
	n, err := cappedCopy(&out, src, 4)
	if err != nil {
		t.Fatalf("cappedCopy: %v", err)
	}
	// 10 - 4 = 6, +1 to let the caller's over-cap test fire = 7.
	if n != 7 {
		t.Errorf("cappedCopy wrote %d bytes with cap=10 totalWritten=4, want 7 "+
			"(bound = cap - written + 1); a bound of 15 means the running total is "+
			"being added rather than subtracted, so the cap applies per entry "+
			"instead of per archive", n)
	}

	// Control: at totalWritten == 0 both operators agree, so this row must NOT be
	// the only one in the test — it is here to pin the no-entries-yet bound.
	var out0 bytes.Buffer
	n0, err := cappedCopy(&out0, bytes.NewReader(bytes.Repeat([]byte("z"), 20)), 0)
	if err != nil {
		t.Fatalf("cappedCopy at totalWritten=0: %v", err)
	}
	if n0 != 11 {
		t.Errorf("cappedCopy wrote %d bytes with cap=10 totalWritten=0, want 11", n0)
	}
}

// The cap is OFF by default, which is the parity position: measured, the
// reference applies no cap at any size the probe could reach (629 MB), so any
// non-zero default makes claustrum fail an extraction the reference completes.
// That measurement disproves a 512 MiB cap; it does not prove there is none
// above 629 MB, and this comment must not say otherwise. This asserts the
// default itself, because that constant IS the divergence —
// TestFilesExtractTarSizeLimit and TestFilesExtractTarCapMaxInt64DoesNotOverflow
// are the two tests that override it.
func TestFilesExtractTarCapDefaultsOff(t *testing.T) {
	if maxExtractBytes != 0 {
		t.Fatalf("maxExtractBytes default = %d, want 0 (cap off = reference parity)", maxExtractBytes)
	}

	// And the disabled path copies straight through rather than through a
	// LimitReader, so a file lands whole.
	s := newTestServer(t)
	body := strings.Repeat("x", 4096)
	archive := tarGzPath(t, map[string]string{"whole.bin": body})
	dest := filepath.Join(t.TempDir(), "out")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":true`) || !strings.Contains(got, `"fileCount":1`) {
		t.Fatalf("extract with cap off = %s, want success:true fileCount:1", got)
	}
	written, err := os.ReadFile(filepath.Join(dest, "whole.bin"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if len(written) != len(body) {
		t.Errorf("extracted %d bytes, want %d (cap-off path must not truncate)", len(written), len(body))
	}
}

// With the cap opted in, extractTarGz must reject archives whose total
// uncompressed size exceeds it. A crafted .tar.gz can have a tiny compressed
// payload that expands to fill a disk; the cap bounds that damage. The var is
// overridden to a small value here so the test does not write gigabytes to disk.
func TestFilesExtractTarSizeLimit(t *testing.T) {
	old := maxExtractBytes
	maxExtractBytes = 1024
	defer func() { maxExtractBytes = old }()

	// Archive with a single 1025-byte file — one byte over the 1 KB cap.
	s := newTestServer(t)
	archive := tarGzPath(t, map[string]string{"big.bin": strings.Repeat("x", 1025)})
	dest := filepath.Join(t.TempDir(), "out")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if !strings.Contains(got, `"success":false`) || !strings.Contains(got, "size limit exceeded") {
		t.Errorf("extract over cap = %s, want success:false and size-limit error", got)
	}
	// fileCount 0, matching the four arms that reject the archive outright
	// (create, mkdir-parent, zip-slip, unsupported type) — not every arm; the
	// note at the cap arm in methods_files.go has the full split.
	if !strings.Contains(got, `"fileCount":0`) {
		t.Errorf("extract over cap = %s, want fileCount:0", got)
	}
	// And nothing truncated is left behind: the entry that tripped the cap was
	// written up to cap+1 bytes before the check fired.
	if _, err := os.Stat(filepath.Join(dest, "big.bin")); !os.IsNotExist(err) {
		t.Errorf("truncated entry survived the cap failure (stat err = %v)", err)
	}

	// The fileCount claim needs an entry to have SUCCEEDED first, or 0 is just
	// the count that was already there. Sorted archive order makes this
	// deterministic: a-first.bin lands (count 1), then b-over.bin takes the
	// total past the cap.
	archivePartial := tarGzPath(t, map[string]string{
		"a-first.bin": strings.Repeat("x", 600),
		"b-over.bin":  strings.Repeat("x", 600),
	})
	destPartial := filepath.Join(t.TempDir(), "outPartial")
	got = dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archivePartial, "destDir": destPartial}))
	if !strings.Contains(got, `"fileCount":0`) {
		t.Errorf("cap tripped after a successful entry = %s, want fileCount:0 (not the partial count)", got)
	}
	if _, err := os.Stat(filepath.Join(destPartial, "b-over.bin")); !os.IsNotExist(err) {
		t.Errorf("truncated entry survived the cap failure (stat err = %v)", err)
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
	s := newTestServer(t)
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

// files.list omits hidden entries (any name beginning with "."), matching the
// reference daemon — probe-confirmed it filters .git/.env/etc. from a listing.
func TestFilesListSkipsDotfiles(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "normal.txt"), []byte("x"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644))
	must(os.Mkdir(filepath.Join(dir, ".git"), 0o755))

	got := dispatchRaw(t, s, rpcLine(t, "files.list", map[string]any{"path": dir}))
	for _, want := range []string{`"name":"normal.txt"`, `"name":"sub"`} {
		if !strings.Contains(got, want) {
			t.Errorf("files.list dropped non-hidden entry %q\n  in %s", want, got)
		}
	}
	for _, hidden := range []string{`"name":".hidden"`, `"name":".git"`} {
		if strings.Contains(got, hidden) {
			t.Errorf("files.list leaked hidden entry %q (reference filters it)\n  in %s", hidden, got)
		}
	}
}

// On a non-repo path, git.info returns the bare {isRepo:false} and
// git.list_branches its full branches:[] shape, matching the reference. Since
// 7d193f89 git.status instead REQUIRES baseRepo, so a path-only call rejects with
// -32602 before any repo check. git.worktree_create reports a clean not_a_repo
// error instead of leaking git's raw "not a git repository" output.
func TestGitNonRepoResults(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir() // under /tmp — not a git repo

	for _, tc := range []struct{ method, wantResult string }{
		// git.info gained repoSlug/defaultBranch in 7c2f88d — always present, empty
		// for a non-repo. git.list_branches keeps its own non-repo shape; git.status
		// now demands baseRepo first.
		{"git.info", `"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`},
		{"git.status", `"error":{"code":-32602,"message":"baseRepo is required"}`},
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
	s := newTestServer(t)

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

// worktree_create off an empty (unborn-HEAD) repo FAILS at 7d193f89 (it succeeded
// with an orphan branch at 5db5e4a). The two-step create passes --no-track, and on
// an unborn HEAD git infers --orphan, which cannot be combined with --track — so git
// worktree add fails and the daemon surfaces that. Measured byte-for-byte against the
// pinned 7d193f89 reference.
func TestGitWorktreeCreateEmptyRepo(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)
	base := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-b", "trunk")
	wt := filepath.Join(base, ".claude", "worktrees", "wt") // 7d193f89: inside the repo
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "newbr", "worktreePath": wt,
	}))
	if !strings.Contains(got, `"errorCode":"worktree_add_failed"`) ||
		!strings.Contains(got, `options '--orphan' and '--track' cannot be used together`) {
		t.Errorf("worktree off empty repo = %s, want the --orphan/--track failure", got)
	}
}

// git.worktree_create accepts a caller-supplied timeoutMs (4534d86): absent is
// byte-identical to the pre-4534d86 success reply, and a fired deadline reports
// errorCode "timeout" rather than worktree_add_failed. A 1ms deadline is below
// the cost of spawning git at all, so it fires deterministically on every OS.
func TestGitWorktreeCreateTimeoutMs(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)
	base := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-b", "trunk")
	runGit(t, base, "-c", "user.email=a@b.c", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")

	// Absent timeoutMs: succeeds, byte-identical to the pre-4534d86 reply.
	wt := filepath.Join(base, ".claude", "worktrees", "ok")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "b1", "worktreePath": wt,
	}))
	if !strings.Contains(got, `"success":true`) {
		t.Fatalf("worktree_create without timeoutMs = %s, want success", got)
	}

	// A 1ms deadline fires before git can finish, yielding errorCode "timeout".
	wt2 := filepath.Join(base, ".claude", "worktrees", "slow")
	got = dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "b2", "worktreePath": wt2, "timeoutMs": 1,
	}))
	if !strings.Contains(got, `"errorCode":"timeout"`) ||
		!strings.Contains(got, "git worktree add timed out after 1ms") {
		t.Errorf("worktree_create timeoutMs=1 = %s, want errorCode timeout", got)
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
	m := newTestProcManager(t)

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
	m := newTestProcManager(t)
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
	s := newTestServer(t)

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
	s := newTestServer(t)
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
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"fromSeq": 0}))
	if !strings.Contains(got, "Process ID is required") {
		t.Errorf("reattach without id = %s, want id-required error", got)
	}
}

// Each namespace handler routes an unrecognized method to the shared
// "Unknown method" error (its switch default).
func TestRoutingUnknownMethodPerNamespace(t *testing.T) {
	s := newTestServer(t)
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
	s := newTestServer(t)
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
	if runtime.GOOS != "linux" {
		// There is no probe to fall back FROM off linux: detectLibc returns ""
		// without consulting ldd, matching the reference, whose detectLibc does
		// not exist in the darwin or windows builds. TestDetectLibc covers that.
		t.Skip("the libc probe is linux-only")
	}
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
	// Atomic extract (#4): the `.fetch-*` staging file must not leak on failure
	// either.
	assertNoStagingLeftover(t, cliPath)
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
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"id": "p1", "command": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("spawn with wrong command type = %s, want Invalid params", got)
	}
}

// process.spawn: valid params but the binary doesn't exist → codeInternal error.
func TestProcessSpawnFailed(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{
		"id": "p_err", "command": "/nonexistent_binary_xyz_claustrum",
	}))
	if !strings.Contains(got, `"error"`) {
		t.Errorf("spawn nonexistent binary = %s, want error result", got)
	}
}

// process.kill: a JSON type mismatch in params hits the bindParams error path.
func TestProcessKillBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.kill", map[string]any{"id": "p1", "signal": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("kill with wrong signal type = %s, want Invalid params", got)
	}
}

// process.reattach: a JSON type mismatch in params hits the bindParams error path.
func TestProcessReattachBindParamsError(t *testing.T) {
	s := newTestServer(t)
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
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "main", "worktreePath": filepath.Join(repo, ".claude", "worktrees", "wt"),
	}))
	if !strings.Contains(got, "worktree_add_failed") {
		t.Errorf("worktree_create with existing branch = %s, want worktree_add_failed", got)
	}
}

// git.worktree_remove: a JSON type mismatch in params hits the bindParams error path.
func TestGitWorktreeRemoveBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{"worktreePath": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("worktree_remove wrong type = %s, want Invalid params", got)
	}
}

// zstdDecompress: os.Create fails when the destination parent directory doesn't exist.
func TestZstdDecompressDestError(t *testing.T) {
	err := zstdDecompressBytes(t, zstdOf(t, []byte("payload")), filepath.Join(t.TempDir(), "nonexistent", "out"))
	if err == nil {
		t.Fatal("expected error for missing parent dir, got nil")
	}
}
