package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// zstdOf compresses b with the same library the daemon uses for -install.
func zstdOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeCLI returns the bytes of a stand-in CLI that exits with the given code
// when invoked (`<cli> --version`, the isRunnable probe). On Unix it is a tiny
// sh script. Windows can't exec a shebang script, so there it is a copy of
// this very test binary, steered into helper mode via CLAUSTRUM_TEST_HELPER
// (helperproc_test.go) — set with t.Setenv so the probed CLI inherits it.
// Call it right before the isRunnable/ensureCLI step it backs: a later call
// with a different exit code overrides the mode for the whole test process.
func fakeCLI(t *testing.T, exitCode int) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode))
	}
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:"+strconv.Itoa(exitCode))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestZstdDecompress(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("hello zstd payload")
	dest := filepath.Join(dir, "out")
	if err := zstdDecompressBytes(t, zstdOf(t, payload), dest); err != nil {
		t.Fatalf("decompress: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("decompressed = %q, want %q", got, payload)
	}
	if err := zstdDecompressBytes(t, []byte("not a zstd stream"), filepath.Join(dir, "x")); err == nil {
		t.Error("expected error decompressing non-zstd input")
	}
}

// zstdDecompress must reject a blob that decompresses beyond the cap. Without
// this guard a tiny .zst file expands unbounded on disk (a zstd bomb): the
// reference was measured writing past 200 MB from a small blob and still going
// when the probe was stopped — S6.
//
// The comment used to say "hundreds of GB". Nothing measured that; the probe was
// stopped at 200 MB. The unbounded direction is what S6 supports, not a figure.
// fetchBytes runs the production download path and returns the bytes it landed,
// so the assertions below keep testing what they always did now that
// fetchToFile streams to a file instead of returning a buffer.
func fetchBytes(t *testing.T, url string) ([]byte, error) {
	t.Helper()
	path, _, err := fetchToFile(url, t.TempDir())
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(path) }()
	return os.ReadFile(path)
}

// zstdDecompressBytes stages blob as a file and decompresses it, for the same
// reason: zstdDecompress now takes a path so ensureCLI can re-read it on retry.
func zstdDecompressBytes(t *testing.T, blob []byte, dest string) error {
	t.Helper()
	src := filepath.Join(t.TempDir(), "blob.zst")
	if err := os.WriteFile(src, blob, 0o600); err != nil {
		t.Fatalf("stage blob: %v", err)
	}
	return zstdDecompress(src, dest)
}

// fetchToFile must land the body on disk and return the sha256 it computed
// WHILE streaming — the point of the change is that no caller ever holds the
// blob, so the hash cannot come from a second pass over a buffer.
func TestFetchToFileStreamsAndHashes(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 128*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, sum, err := fetchToFile(srv.URL, dir)
	if err != nil {
		t.Fatalf("fetchToFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	if got := filepath.Dir(path); got != dir {
		t.Errorf("temp landed in %s, want %s (beside the install destination)", got, dir)
	}
	want := sha256.Sum256(body)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("streamed sum = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Errorf("temp holds %d bytes, want %d", len(onDisk), len(body))
	}
}

// An unusable target directory must NOT turn into a download failure. ensureCLI
// creates the cli-dir after the download and reports its own "mkdir cli dir: "
// error there; failing here would move that error to a different string on a
// reachable path, so fetchToFile falls back to the OS temp dir instead.
func TestFetchToFileFallsBackWhenDirUnusable(t *testing.T) {
	body := []byte("payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	path, _, err := fetchToFile(srv.URL, missing)
	if err != nil {
		t.Fatalf("fetchToFile with an unusable dir must fall back, got: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	if filepath.Dir(path) == missing {
		t.Errorf("temp claims to be in the missing dir %s", missing)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
		t.Errorf("fallback temp = %q (err %v), want %q", got, err, body)
	}
}

// A body over the cap must leave nothing behind: the temp is partially written
// by the time the limit is detected.
func TestFetchToFileRemovesTempWhenOverCap(t *testing.T) {
	old := maxCLIBytes
	maxCLIBytes = 5
	defer func() { maxCLIBytes = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, _, err := fetchToFile(srv.URL, dir); err == nil {
		t.Fatal("over-cap body succeeded, want an error")
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d file(s) left behind after an over-cap download, want 0", len(left))
	}
}

// The cap is OFF by default, which is the parity position: measured at 5db5e4a
// with a 600 MiB payload on the -cli-zst path, the reference decompressed all of
// it and failed only at the runnability check, while a capped claustrum answered
// "decompressing: decompressed CLI exceeds 536870912 bytes" — a cliError the
// reference cannot produce. Both halves were measured: on the -cli-url path a
// 629 MB incompressible body downloads fully on both binaries with the cap off,
// and the cap-on control answers "download failed: response exceeds 536870912
// bytes", proving the probe reaches the download limit at all.
//
// This asserts the default itself, because that constant IS the divergence;
// every other test here overrides it.
func TestCLISizeCapDefaultsOff(t *testing.T) {
	if maxCLIBytes != 0 {
		t.Fatalf("maxCLIBytes default = %d, want 0 (cap off = reference parity)", maxCLIBytes)
	}

	// Both disabled paths must copy straight through rather than through a
	// LimitReader, so neither truncates.
	t.Run("zstd_decompress_whole", func(t *testing.T) {
		dir := t.TempDir()
		body := bytes.Repeat([]byte("x"), 4096)
		dest := filepath.Join(dir, "whole")
		if err := zstdDecompressBytes(t, zstdOf(t, body), dest); err != nil {
			t.Fatalf("decompress with cap off: %v", err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(got) != len(body) {
			t.Errorf("decompressed %d bytes, want %d (cap-off path must not truncate)", len(got), len(body))
		}
	})

	t.Run("download_whole", func(t *testing.T) {
		body := bytes.Repeat([]byte("y"), 4096)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		got, err := fetchBytes(t, srv.URL)
		if err != nil {
			t.Fatalf("download with cap off: %v", err)
		}
		if len(got) != len(body) {
			t.Errorf("got %d bytes, want %d (cap-off path must not truncate)", len(got), len(body))
		}
	})
}

func TestZstdDecompressSizeLimit(t *testing.T) {
	old := maxCLIBytes
	maxCLIBytes = 1024
	defer func() { maxCLIBytes = old }()

	dir := t.TempDir()
	// 1025 bytes decompressed — one byte over the 1 KB cap.
	bomb := zstdOf(t, bytes.Repeat([]byte("x"), 1025))
	if err := zstdDecompressBytes(t, bomb, filepath.Join(dir, "over")); err == nil {
		t.Error("expected error for oversized zstd payload, got nil")
	}

	// Exactly at the cap (1024 bytes) must succeed.
	ok := zstdOf(t, bytes.Repeat([]byte("x"), 1024))
	if err := zstdDecompressBytes(t, ok, filepath.Join(dir, "at")); err != nil {
		t.Errorf("zstdDecompress at cap: %v", err)
	}
}

func TestIsRegularFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isRegularFile(f) {
		t.Error("a regular file should report true")
	}
	if isRegularFile(dir) {
		t.Error("a directory is not a regular file")
	}
	if isRegularFile(filepath.Join(dir, "missing")) {
		t.Error("a missing path is not a regular file")
	}
}

func TestIsRunnable(t *testing.T) {
	dir := t.TempDir()
	// .exe suffix: Go's exec on Windows only resolves paths that carry an
	// extension (harmless on Unix, where the shebang decides).
	ok := filepath.Join(dir, "ok.exe")
	if err := os.WriteFile(ok, fakeCLI(t, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isRunnable(ok) {
		t.Error("an exit-0 CLI should be runnable")
	}
	bad := filepath.Join(dir, "bad.exe")
	if err := os.WriteFile(bad, fakeCLI(t, 3), 0o755); err != nil {
		t.Fatal(err)
	}
	if isRunnable(bad) {
		t.Error("an exit-3 CLI should not be runnable")
	}
	if isRunnable(filepath.Join(dir, "missing")) {
		t.Error("a missing path is not runnable")
	}
}

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = w.Write([]byte("body-bytes"))
			return
		}
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	b, err := fetchBytes(t, srv.URL+"/ok")
	if err != nil || string(b) != "body-bytes" {
		t.Fatalf("httpGet ok = %q, %v", b, err)
	}
	if _, err := fetchBytes(t, srv.URL+"/missing"); err == nil {
		t.Error("a non-200 response should error")
	}

	t.Run("size_limit_exceeded", func(t *testing.T) {
		old := maxCLIBytes
		maxCLIBytes = 5
		defer func() { maxCLIBytes = old }()

		large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 7)) // 7 > maxCLIBytes(5)
		}))
		defer large.Close()

		if _, err := fetchBytes(t, large.URL); err == nil {
			t.Error("expected error when response exceeds maxCLIBytes")
		}
	})

	t.Run("size_at_exact_limit_ok", func(t *testing.T) {
		old := maxCLIBytes
		maxCLIBytes = 5
		defer func() { maxCLIBytes = old }()

		exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 5)) // exactly maxCLIBytes(5)
		}))
		defer exact.Close()

		b, err := fetchBytes(t, exact.URL)
		if err != nil {
			t.Fatalf("a response of exactly maxCLIBytes must succeed, got: %v", err)
		}
		if int64(len(b)) != maxCLIBytes {
			t.Errorf("body len = %d, want %d", len(b), maxCLIBytes)
		}
	})
}

func TestDetectLibc(t *testing.T) {
	got := detectLibc()
	if runtime.GOOS != "linux" {
		// The reference reports an empty libc off linux; claustrum used to answer
		// "glibc" on Windows and macOS.
		//
		// Pointer-class, and deliberately labelled as such: it was settled by
		// inspecting the non-linux reference builds, not by a probe, because the
		// reference cannot be run on this host's darwin or windows targets. The
		// observable it predicts is that the `libc` key is always present and
		// always empty there — which is exactly what this assertion pins.
		if got != "" {
			t.Errorf("detectLibc() = %q on %s, want \"\" (the reference reports no libc off linux)", got, runtime.GOOS)
		}
		return
	}
	if got != "glibc" && got != "musl" {
		t.Errorf("detectLibc() = %q, want glibc or musl", got)
	}
}

// TestDetectLibcWithTimeout verifies the probe returns promptly with a fallback
// classification when `ldd` hangs, instead of blocking forever. A runner that
// blocks until ctx is cancelled stands in for a stalled ldd; detectLibcWith must
// still return within its deadline.
func TestDetectLibcWithTimeout(t *testing.T) {
	hung := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done() // mimic a stalled `ldd`: unblocks only when the deadline fires
		return nil, ctx.Err()
	}
	// The glob MUST report no match here. detectLibcWith short-circuits to "musl"
	// when the loader marker is present and never calls the runner, so on a musl
	// host a real glob would match and this test would pass without ever entering
	// the hung runner.
	//
	// The stub is load-bearing, not defensive, and that was checked rather than
	// argued: with a real glob on a host that HAS the loader, replacing the runner
	// with `select {}` — no deadline honoured at all, the exact regression this
	// test exists to catch — still passed, in under a millisecond.
	//
	// Deliberately not phrased in terms of "this host": CI's Linux leg is glibc,
	// so a reader who checks for the loader there finds none and could conclude
	// the stub is unnecessary. That is the one conclusion that would re-break it.
	noMusl := func(string) ([]string, error) { return nil, nil }
	done := make(chan string, 1)
	go func() { done <- detectLibcWith(20*time.Millisecond, hung, noMusl) }()
	select {
	case got := <-done:
		if got != "glibc" && got != "musl" {
			t.Errorf("detectLibcWith(timeout) = %q, want glibc or musl fallback", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detectLibcWith did not return after its deadline — ldd timeout not enforced")
	}
}

// classifyLibc's branches with an injected glob, so the musl paths are
// exercised on a glibc host too. The mixed-host row is the one that motivated
// W9: a loader present while ldd SUCCEEDS and reports glibc.
func TestClassifyLibc(t *testing.T) {
	found := func(string) ([]string, error) { return []string{"/lib/ld-musl-aarch64.so.1"}, nil }
	none := func(string) ([]string, error) { return nil, nil }
	broken := func(string) ([]string, error) { return nil, filepath.ErrBadPattern }
	cases := []struct {
		name string
		out  []byte
		err  error
		glob func(string) ([]string, error)
		want string
	}{
		{"ldd ok, musl banner", []byte("musl libc (x86_64)\nVersion 1.2.3"), nil, none, "musl"},
		{"ldd ok, glibc banner", []byte("ldd (GNU libc) 2.31"), nil, none, "glibc"},
		{"ldd fails, loader present", nil, io.EOF, found, "musl"},
		{"ldd fails, no loader", nil, io.EOF, none, "glibc"},
		// The mixed host: glibc's ldd succeeds AND a musl loader is installed.
		// The reference answers musl; claustrum used to answer glibc because it
		// consulted the marker only on the ldd-failure path.
		{"mixed host: ldd says glibc, loader present", []byte("ldd (Debian GLIBC 2.41) 2.41"), nil, found, "musl"},
		// A non-x86_64 loader must match — the old hardcoded path could not.
		{"arm64 loader", nil, io.EOF, found, "musl"},
		// A glob error is not a match.
		{"glob errors", []byte("ldd (GNU libc) 2.31"), nil, broken, "glibc"},
	}
	for _, tc := range cases {
		if got := classifyLibc(tc.out, tc.err, tc.glob); got != tc.want {
			t.Errorf("%s: classifyLibc = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPruneCLI(t *testing.T) {
	dir := t.TempDir()
	// Four "version" files with strictly increasing mtimes.
	names := []string{"v1", "v2", "v3", "v4"}
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(int64(1000+i*100), 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	pruneCLI(dir, 2) // keep the 2 newest (v3, v4)
	for _, n := range []string{"v1", "v2"} {
		if isRegularFile(filepath.Join(dir, n)) {
			t.Errorf("%s should have been pruned", n)
		}
	}
	for _, n := range []string{"v3", "v4"} {
		if !isRegularFile(filepath.Join(dir, n)) {
			t.Errorf("%s should have been kept", n)
		}
	}
}

// runInstall's prune step is gated on `o.cliKeep > 0` AND on an install having
// actually succeeded. The reference touches the cli-dir only when it attempts an
// install: a cache-hit run neither sweeps orphans nor prunes, and a FAILED
// install sweeps but does not prune (probe-measured at 5db5e4a).
//
// This test previously drove runInstall with an already-present CLI and asserted
// that it pruned — encoding claustrum's divergence. It now exercises the guard on
// the install path, where prune belongs, and pins the cache-hit case separately.
func TestRunInstallHonorsCliKeepGuard(t *testing.T) {
	mk := func(t *testing.T, dir, name string, ageSec int) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, fakeCLI(t, 0), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(int64(1000+ageSec), 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	count := func(t *testing.T, dir string) int {
		t.Helper()
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	// blob returns a -cli-zst source, so the install path runs without a network.
	blob := func(t *testing.T) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "cli.zst")
		if err := os.WriteFile(f, zstdOf(t, fakeCLI(t, 0)), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Version-style names (with a dot) so a present CLI is actually runnable on
	// Windows too — exec there only resolves paths carrying an extension, and an
	// extensionless name would silently take the not-present branch.
	t.Run("keep0_does_not_prune", func(t *testing.T) {
		dir := t.TempDir()
		mk(t, dir, "3.0.0", 30)
		mk(t, dir, "2.0.0", 20)
		mk(t, dir, "1.0.0", 10)
		_ = captureInstallFacts(t, installOpts{
			cliDir: dir, cliVersion: "9.0.0", cliZst: blob(t), cliKeep: 0})
		if n := count(t, dir); n != 4 {
			t.Errorf("keep=0 left %d files, want 4 (3 existing + the new one; a >=0 guard would wipe versions)", n)
		}
	})

	t.Run("keep2_prunes_to_newest", func(t *testing.T) {
		dir := t.TempDir()
		mk(t, dir, "4.0.0", 40)
		mk(t, dir, "3.0.0", 30)
		mk(t, dir, "2.0.0", 20)
		mk(t, dir, "1.0.0", 10)
		_ = captureInstallFacts(t, installOpts{
			cliDir: dir, cliVersion: "9.0.0", cliZst: blob(t), cliKeep: 2})
		if n := count(t, dir); n != 2 {
			t.Errorf("keep=2 left %d files, want 2 (cliKeep>0 guard regressed, skipping prune)", n)
		}
	})

	// The reference leaves a cache-hit run's cli-dir completely alone.
	t.Run("cache_hit_does_not_prune", func(t *testing.T) {
		dir := t.TempDir()
		mk(t, dir, "4.0.0", 40) // newest → the present CLI
		mk(t, dir, "3.0.0", 30)
		mk(t, dir, "2.0.0", 20)
		mk(t, dir, "1.0.0", 10)
		f := captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: "4.0.0", cliKeep: 2})
		if !f.CliWasPresent {
			t.Fatal("fixture did not take the cache-hit branch")
		}
		if n := count(t, dir); n != 4 {
			t.Errorf("cache hit left %d files, want 4: the reference does not prune when it installs nothing", n)
		}
	})
}

// TestSweepFetchTemps pins the orphan sweep, including the asymmetry that made
// it worth measuring: os.Remove clears files and EMPTY directories, and silently
// leaves a POPULATED ".fetch-dir/" behind. Real CLI versions must never be
// touched by the sweep.
func TestSweepFetchTemps(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".fetch-abc123")
	write(".fetch-") // the empty-suffix form
	write("1.0.0")   // a real version
	if err := os.Mkdir(filepath.Join(dir, ".fetch-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".fetch-full"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(".fetch-full", "inner"))

	sweepFetchTemps(dir)

	got := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		got[e.Name()] = true
	}
	for _, gone := range []string{".fetch-abc123", ".fetch-", ".fetch-empty"} {
		if got[gone] {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	if !got[".fetch-full"] {
		t.Error(".fetch-full was removed; a populated orphan directory must survive, matching os.Remove")
	}
	if !got["1.0.0"] {
		t.Error("the sweep deleted a real CLI version")
	}
}

// TestPruneBudgetIgnoresOrphans is the regression for the bug behind F3: orphan
// temps counted as CLI *versions*, so they consumed the -cli-keep budget. With
// three orphans, four versions and keep=3, every real version was evicted —
// including the one just installed.
func TestPruneBudgetIgnoresOrphans(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, fakeCLI(t, 0), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(int64(1000+i*10), 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// Orphans NEWER than every version, so a prune that counted them would keep
	// the orphans and delete the real binaries.
	for _, name := range []string{".fetch-a", ".fetch-b", ".fetch-c"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(9999, 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	zstFile := filepath.Join(t.TempDir(), "cli.zst")
	if err := os.WriteFile(zstFile, zstdOf(t, fakeCLI(t, 0)), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = captureInstallFacts(t, installOpts{
		cliDir: dir, cliVersion: "5.0.0", cliZst: zstFile, cliKeep: 3})

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range ents {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	want := []string{"3.0.0", "4.0.0", "5.0.0"}
	if len(left) != len(want) {
		t.Fatalf("cli-dir = %v, want %v (orphans must not consume the keep budget)", left, want)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("cli-dir = %v, want %v", left, want)
		}
	}
}

func TestEnsureCLIFromZst(t *testing.T) {
	dir := t.TempDir()
	zst := zstdOf(t, fakeCLI(t, 0))
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zst, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zst)
	cliPath := filepath.Join(dir, "cli", "1.0.0")
	o := installOpts{cliZst: zstFile, cliChecksum: hex.EncodeToString(sum[:])}

	if err := ensureCLI(o, cliPath); err != nil {
		t.Fatalf("ensureCLI: %v", err)
	}
	if !isRegularFile(cliPath) || !isRunnable(cliPath) {
		t.Error("the CLI should be installed and runnable")
	}
	if isRegularFile(zstFile) {
		t.Error("the source .zst should be consumed on success")
	}
	// Atomic extract (#4): the .tmp staging file is renamed into place, not left behind.
	if isRegularFile(cliPath + ".tmp") {
		t.Error("the .tmp staging file should be renamed away, not left behind")
	}
}

func TestEnsureCLIFromURL(t *testing.T) {
	zst := zstdOf(t, fakeCLI(t, 0))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	cliPath := filepath.Join(t.TempDir(), "1.0.0")
	sum := sha256.Sum256(zst)
	o := installOpts{cliURL: srv.URL + "/claude.zst", cliChecksum: hex.EncodeToString(sum[:])}
	if err := ensureCLI(o, cliPath); err != nil {
		t.Fatalf("ensureCLI from url: %v", err)
	}
	if !isRunnable(cliPath) {
		t.Error("the downloaded CLI should be runnable")
	}
}

// Claustrum's opt-in divergence (IMPROVEMENTS D1): the -cli-zst (SFTP) path
// verifies -cli-checksum WHEN one is supplied — a wrong checksum is rejected like
// the -cli-url path, and the source blob is left intact. An ABSENT/empty checksum
// stays trusting, matching the reference (which never verifies this path), so
// honest callers are unaffected.
func TestEnsureCLIZstVerifiesChecksumWhenSupplied(t *testing.T) {
	mkZst := func(t *testing.T) (dir, zstFile string) {
		dir = t.TempDir()
		zstFile = filepath.Join(dir, "cli.zst")
		if err := os.WriteFile(zstFile, zstdOf(t, fakeCLI(t, 0)), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, zstFile
	}

	// .exe suffix on the install targets: Go's exec on Windows only resolves
	// paths that carry an extension (harmless on Unix).
	t.Run("wrong checksum is rejected, blob preserved", func(t *testing.T) {
		dir, zstFile := mkZst(t)
		o := installOpts{cliZst: zstFile, cliChecksum: "deadbeef"} // wrong on purpose
		err := ensureCLI(o, filepath.Join(dir, "out.exe"))
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("zst + wrong checksum = %v, want a checksum-mismatch error", err)
		}
		if !isRegularFile(zstFile) {
			t.Error("a failed checksum must not consume the source .zst")
		}
	})

	t.Run("absent checksum stays trusting (reference parity)", func(t *testing.T) {
		dir, zstFile := mkZst(t)
		cliPath := filepath.Join(dir, "out.exe")
		if err := ensureCLI(installOpts{cliZst: zstFile}, cliPath); err != nil {
			t.Fatalf("zst + no checksum must install: %v", err)
		}
		if !isRunnable(cliPath) {
			t.Error("the CLI should install when no checksum is supplied")
		}
	})

	t.Run("matching checksum installs", func(t *testing.T) {
		dir, zstFile := mkZst(t)
		zst, _ := os.ReadFile(zstFile)
		sum := sha256.Sum256(zst)
		cliPath := filepath.Join(dir, "out.exe")
		o := installOpts{cliZst: zstFile, cliChecksum: hex.EncodeToString(sum[:])}
		if err := ensureCLI(o, cliPath); err != nil {
			t.Fatalf("zst + matching checksum: %v", err)
		}
		if !isRunnable(cliPath) {
			t.Error("the CLI should install with a matching checksum")
		}
	})
}

// The -cli-url (download) path verifies UNCONDITIONALLY: a wrong checksum and an
// empty checksum both fail (the reference checks even when -cli-checksum is "").
func TestEnsureCLIURLVerifiesUnconditionally(t *testing.T) {
	zst := zstdOf(t, fakeCLI(t, 0))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	for _, tc := range []struct{ name, checksum string }{
		{"wrong checksum", "deadbeef"},
		{"empty checksum", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := installOpts{cliURL: srv.URL + "/claude.zst", cliChecksum: tc.checksum}
			err := ensureCLI(o, filepath.Join(t.TempDir(), "1.0.0"))
			if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
				t.Errorf("download with %s = %v, want a checksum-mismatch error", tc.name, err)
			}
		})
	}
}

// ensureCLI wraps the input-read and decompress failures the way the reference
// does ("opening input: …" / "decompressing: …").
func TestEnsureCLIErrorWrapping(t *testing.T) {
	dir := t.TempDir()
	if err := ensureCLI(installOpts{cliZst: filepath.Join(dir, "nope.zst")}, filepath.Join(dir, "out")); err == nil ||
		!strings.HasPrefix(err.Error(), "opening input:") {
		t.Errorf("missing zst = %v, want an 'opening input:' error", err)
	}
	badZst := filepath.Join(dir, "bad.zst")
	if err := os.WriteFile(badZst, []byte("not a zst"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: badZst}, filepath.Join(dir, "out2")); err == nil ||
		!strings.HasPrefix(err.Error(), "decompressing:") {
		t.Errorf("bad zst = %v, want a 'decompressing:' error", err)
	}
	// The KEEP side of the consume boundary. A failure before decompression
	// produced a staged file leaves the blob alone — measured on the reference
	// too. Without this, an over-correction to unconditional consumption would
	// pass every other test.
	if !isRegularFile(badZst) {
		t.Error("a blob that failed to decompress was consumed; it must be kept")
	}
}

func TestEnsureCLINoSource(t *testing.T) {
	o := installOpts{cliVersion: "1.0.0"} // no -cli-zst and no -cli-url
	if err := ensureCLI(o, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Error("ensureCLI with no source should error")
	}
}

func TestRunInstallFacts(t *testing.T) {
	dir := t.TempDir()
	zst := zstdOf(t, fakeCLI(t, 0))
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zst, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zst)
	o := installOpts{
		cliDir:      filepath.Join(dir, "clidir"),
		cliVersion:  "1.2.3",
		cliZst:      zstFile,
		cliChecksum: hex.EncodeToString(sum[:]),
		cliKeep:     3,
	}

	// First install: freshly materialized → not previously present, no error.
	f := captureInstallFacts(t, o)
	if f.CliWasPresent {
		t.Error("a fresh install should report cliWasPresent=false")
	}
	if f.CliError != "" {
		t.Errorf("unexpected cliError: %s", f.CliError)
	}
	if f.OS == "" || f.Arch == "" {
		t.Errorf("facts missing os/arch: %+v", f)
	}
	// libc is reported on linux only — off linux the reference has no
	// detectLibc at all and emits an empty string, so "" is the correct value
	// there rather than a missing one.
	if runtime.GOOS == "linux" && f.Libc == "" {
		t.Errorf("facts missing libc on linux: %+v", f)
	}
	if runtime.GOOS != "linux" && f.Libc != "" {
		t.Errorf("facts carry libc=%q on %s, want empty", f.Libc, runtime.GOOS)
	}
	if f.CliPath != filepath.Join(o.cliDir, o.cliVersion) {
		t.Errorf("cliPath = %q", f.CliPath)
	}

	// Second install: the CLI is now present + runnable → reported as present.
	f2 := captureInstallFacts(t, o)
	if !f2.CliWasPresent {
		t.Error("a second install should report cliWasPresent=true")
	}
}

// captureInstallFacts runs runInstall and parses the __INSTALL_RESULT__ JSON it
// prints to stdout.
func captureInstallFacts(t *testing.T, o installOpts) installFacts {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runInstall(o)
	_ = w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	const marker = "__INSTALL_RESULT__"
	i := strings.Index(string(out), marker)
	if i < 0 {
		t.Fatalf("no %s in output: %q", marker, out)
	}
	var f installFacts
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out)[i+len(marker):])), &f); err != nil {
		t.Fatalf("unmarshal facts: %v", err)
	}
	return f
}

// TestSweepFetchTempsUnreadableDir covers the early return: an unreadable or
// missing cli-dir must be a no-op, never a panic, because the sweep runs on the
// install path where the directory may not exist yet.
func TestSweepFetchTempsUnreadableDir(t *testing.T) {
	sweepFetchTemps(filepath.Join(t.TempDir(), "no-such-dir"))
}

// A non-200 download is reported with the reference's exact wording, and it is
// NOT prefixed "download failed: " — that prefix belongs to transport failures
// only. Measured at 5db5e4a:
//
//	404 response        -> cliError "download failed with status 404"
//	connection refused  -> cliError "download failed: Get \"http://…\": dial tcp …"
//
// claustrum used to emit "download failed: download <url>: 404 File not found"
// for the first case, which also leaked the (possibly signed) URL onto the
// __INSTALL_RESULT__ line.
func TestDownloadStatusErrorWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchBytes(t, srv.URL+"/missing")
	if err == nil {
		t.Fatal("httpGet on a 404 returned no error")
	}
	if got := err.Error(); got != "download failed with status 404" {
		t.Errorf("httpGet 404 error = %q, want %q", got, "download failed with status 404")
	}

	// ensureCLI must pass a status error through unwrapped.
	dir := t.TempDir()
	err = ensureCLI(installOpts{cliURL: srv.URL + "/missing", cliChecksum: "irrelevant"},
		filepath.Join(dir, "v1"))
	if err == nil {
		t.Fatal("ensureCLI with a 404 download succeeded")
	}
	if got := err.Error(); got != "download failed with status 404" {
		t.Errorf("ensureCLI 404 cliError = %q, want it unwrapped and exactly %q",
			got, "download failed with status 404")
	}

	// A transport failure keeps the "download failed: " prefix. refusedURL binds a
	// port, closes it, and hands back the address, so the connection is refused
	// immediately. A hardcoded port cannot promise that: if anything listens there
	// the test takes a different path, and if the host DROPS rather than refuses
	// it blocks for the 5-minute client timeout instead of failing.
	err = ensureCLI(installOpts{cliURL: refusedURL(t), cliChecksum: "x"},
		filepath.Join(dir, "v2"))
	if err == nil || !strings.HasPrefix(err.Error(), "download failed: ") {
		t.Errorf("transport failure = %v, want a \"download failed: \" prefix", err)
	}
}

// The loader glob decides ALONE: when it matches, `ldd` is never executed.
//
// This is the behaviour, not an optimisation. Measured 2026-08-02 against
// 5db5e4a with a stand-in `ldd` on PATH that records its own invocation: with a
// musl loader present the reference did not run it and claustrum did. With the
// marker masked BOTH reached the stand-in, so "not run" is an observation and
// not a fixture that never fired. Executing a PATH-resolved binary the reference
// does not execute on this path matters in `-install`, which is the one mode
// with a network-facing threat model.
func TestDetectLibcSkipsLddWhenLoaderGlobMatches(t *testing.T) {
	called := false
	run := func(context.Context) ([]byte, error) {
		called = true
		return []byte("ldd (Debian GLIBC 2.41-1) 2.41"), nil
	}
	muslPresent := func(string) ([]string, error) {
		return []string{"/lib/ld-musl-x86_64.so.1"}, nil
	}

	if got := detectLibcWith(time.Second, run, muslPresent); got != "musl" {
		t.Errorf("detectLibcWith = %q, want musl (the loader marker decides)", got)
	}
	if called {
		t.Error("ldd was executed even though the loader glob matched — " +
			"the reference does not run it on this path")
	}
}

// The control for the test above: with no marker, the probe IS run. Without this
// row, a detectLibcWith that never ran ldd under any condition would pass the
// skip test and still be wrong.
func TestDetectLibcRunsLddWhenLoaderGlobDoesNotMatch(t *testing.T) {
	called := false
	run := func(context.Context) ([]byte, error) {
		called = true
		return []byte("ldd (Debian GLIBC 2.41-1) 2.41"), nil
	}
	noMusl := func(string) ([]string, error) { return nil, nil }

	if got := detectLibcWith(time.Second, run, noMusl); got != "glibc" {
		t.Errorf("detectLibcWith = %q, want glibc", got)
	}
	if !called {
		t.Error("ldd was not executed with no loader marker present — " +
			"then the skip test above proves nothing")
	}
}

// The CONSUME side of the boundary: once decompression has produced a staged
// file, the uploaded blob is consumed even though the install then fails.
//
// Measured 2026-08-02 against 5db5e4a across four fixtures — a runnable CLI
// (consumed, the control), one that exits 1 (consumed), a blob that is not valid
// zstd (kept), and an absent blob. claustrum kept the blob on row two.
//
// This is the only cell that discriminates. Every other -cli-zst test either
// fails before decompression (where consume-on-success and consume-on-decompress
// agree) or succeeds outright (where they also agree), which is why the fix
// shipped uncovered until a mutant run went looking. Exit code 1 is the load
// bearing choice: it is the one post-decompress failure provokable without
// permission games that break the Windows leg.
func TestEnsureCLIConsumesBlobWhenExtractedCLIIsNotRunnable(t *testing.T) {
	dir := t.TempDir()
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zstdOf(t, fakeCLI(t, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	// .exe so Go's exec resolves it on the Windows leg, as the sibling tests do.
	err := ensureCLI(installOpts{cliZst: zstFile}, filepath.Join(dir, "out.exe"))
	if err == nil || !strings.Contains(err.Error(), "not runnable") {
		t.Fatalf("want a not-runnable error, got %v", err)
	}
	if isRegularFile(zstFile) {
		t.Error("the blob must be consumed once decompression succeeded — " +
			"the reference consumes it even when the extracted CLI does not run")
	}
}

// The OTHER post-decompress failure: chmod fails after the staged file exists.
//
// docs/PROTOCOL.md records the window between decompress and rename as sitting
// on the consumed side "by construction, not by observation" — because no
// fixture provokes it. A permission game would provoke it on unix and be a no-op
// on the Windows leg (os.Chmod there only toggles a read-only attribute), which
// is the fixture trap this repo has hit three times, so the failure is injected
// through the chmodStaged seam instead. That keeps the test identical on every
// OS and turns the one unobserved cell of the boundary into an observed one.
func TestEnsureCLIConsumesBlobWhenChmodFails(t *testing.T) {
	old := chmodStaged
	chmodStaged = func(string, os.FileMode) error { return errors.New("chmod refused") }
	t.Cleanup(func() { chmodStaged = old })

	dir := t.TempDir()
	zstFile := filepath.Join(dir, "cli.zst")
	// A CLI that WOULD run: the failure must come from chmod, not runnability,
	// or this test would duplicate the sibling above instead of covering a new
	// branch.
	if err := os.WriteFile(zstFile, zstdOf(t, fakeCLI(t, 0)), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureCLI(installOpts{cliZst: zstFile}, filepath.Join(dir, "out.exe"))
	if err == nil || !strings.Contains(err.Error(), "chmod refused") {
		t.Fatalf("want the injected chmod error, got %v", err)
	}
	if isRegularFile(zstFile) {
		t.Error("the blob must be consumed: decompression had already produced a " +
			"staged file, which is the side of the boundary this branch is on")
	}
	// And the staging file must not survive the failure — the same cleanup the
	// not-runnable branch does.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Errorf("staging file %s survived a chmod failure", e.Name())
		}
	}
}
