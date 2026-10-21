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
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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
// when invoked (`<cli> --version`, the -install run). On Unix it is a tiny
// sh script. Windows can't exec a shebang script, so there it is a copy of
// this very test binary, steered into helper mode via CLAUSTRUM_TEST_HELPER
// (helperproc_test.go) — set with t.Setenv so the probed CLI inherits it.
// Call it right before the ensureCLI step it backs: a later call
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

// fakeCLIZst is zstdOf(fakeCLI(t, exitCode)), with the same caveat as fakeCLI.
// On Windows fakeCLI is this whole test binary for every exit code, and the
// helper mode picks the code. So the blob is stubZst, compressed once per test
// run. Each caller gets its own copy of the bytes.
func fakeCLIZst(t *testing.T, exitCode int) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return zstdOf(t, fakeCLI(t, exitCode))
	}
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:"+strconv.Itoa(exitCode))
	return bytes.Clone(stubZst(t))
}

// assertNoStagingLeftover fails if stageAndInstall's staging file survived in
// the cli-dir — the directory holding cliPath, which is where the staging file
// is created: os.CreateTemp(filepath.Dir(cliPath), ".fetch-*").
//
// The NAME is the whole point of this helper. Staging is `.fetch-<random>`, not
// `<cliPath>.tmp`: the `.fetch-` prefix is what makes the file reclaimable by
// sweepFetchTemps via isSweptName, and stageAndInstall says so explicitly. Both
// call sites — TestEnsureCLIFromZst here and TestEnsureCLINotRunnable in
// methods_unit_test.go — used to assert on `<cliPath>.tmp`, which was the real
// staging name until 52f1431 moved it and left the assertions behind. Against a
// name nothing creates they could not fail, which is how they survived.
// Confirmed by mutation, each caught by ONE of the two: replacing the rename
// with a hard link leaks past TestEnsureCLIFromZst, and dropping the
// not-runnable branch's os.Remove leaks past TestEnsureCLINotRunnable. The old
// assertions passed under both.
//
// The sweep cannot hide a leak from this helper. It removes only entries more
// than ten minutes old (sweepMinAge), and a leaked staging file is fresh.
//
// Prefix-matched rather than counting entries: a caller may legitimately have
// other files in the cli-dir (installed versions), and only the staging
// leftover is under test here.
func assertNoStagingLeftover(t *testing.T, cliPath string) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Dir(cliPath))
	if err != nil {
		t.Fatalf("reading cli-dir: %v", err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Errorf("staging file %q leaked into the cli-dir", e.Name())
		}
	}
}

// slowCLI returns the bytes of a stand-in CLI that answers `--version` with exit
// 0 only after `seconds`: the honest-but-slow shape.
// Same Unix/Windows split as fakeCLI above, and the same "call it right before
// the step it backs" caveat on Windows.
// ⚠️ `seconds` is a process-leak budget on the Unix legs, not just a duration.
// The sh branch makes `sleep` a GRANDCHILD, and a stop that kills only the
// direct `sh` child leaves it. Measured with `sleep 30`: a direct kill returned
// `signal: killed` at 100 ms. The sleeper was reparented to PID 1 and was still
// alive a second later. At 1 s it self-exits and nothing leaks. A higher value
// leaks one process per run on macOS and linux under such a stop. It changes
// nothing on Windows, whose branch is a single process.
func slowCLI(t *testing.T, seconds int) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return []byte(fmt.Sprintf("#!/bin/sh\nsleep %d\nexit 0\n", seconds))
	}
	t.Setenv("CLAUSTRUM_TEST_HELPER", "slow:"+strconv.Itoa(seconds))
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

// isRunnable reports whether `<path> --version` exits 0. It is the direct run of
// -install under a bound that no fixture of these tests reaches.
func isRunnable(path string) bool { return runCLIVersion(path, time.Minute) == cliRunOK }

// The shipped bounds of the direct `<cli> --version` run are the reference's.
// They are 30 s on a cache hit and 120 s after an install in the same run. Measured on
// f6010b97 and 89cb6289 (rows B03, B04, B15, F04, F05, F15). The tests shrink the
// two vars, so this pins the values a user gets.
func TestCLIRunBoundsAreTheReferenceValues(t *testing.T) {
	if cliRunBound != 30*time.Second {
		t.Errorf("cliRunBound = %s, want 30s", cliRunBound)
	}
	if cliFirstRunBound != 120*time.Second {
		t.Errorf("cliFirstRunBound = %s, want 120s", cliFirstRunBound)
	}
	if installHeaderTimeout != 60*time.Second {
		t.Errorf("installHeaderTimeout = %s, want 60s", installHeaderTimeout)
	}
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

// fetchBytes runs the production download path and returns the bytes it landed:
// the decompressed body.
func fetchBytes(t *testing.T, url string) ([]byte, error) {
	t.Helper()
	path, _, err := fetchCLI(url, t.TempDir())
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

// fetchCLI must land the decompressed body in a staging file of the cli-dir. It
// returns the sha256 of the body that it computed WHILE streaming. No caller
// holds the body, so the hash cannot come from a second pass over a buffer.
func TestFetchCLIStreamsAndHashes(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 128*1024)
	blob := zstdOf(t, body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(blob)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, sum, err := fetchCLI(srv.URL, dir)
	if err != nil {
		t.Fatalf("fetchCLI: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	if got := filepath.Dir(path); got != dir || !isSweptName(filepath.Base(path)) {
		t.Errorf("staging file is %s, want a .fetch-<digits> name in %s", path, dir)
	}
	want := sha256.Sum256(blob)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("streamed sum = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Errorf("staging file holds %d bytes, want %d", len(onDisk), len(body))
	}
	if lastInstallFetch == nil || lastInstallFetch.Bytes != int64(len(blob)) {
		t.Errorf("fetch = %+v, want bytes %d", lastInstallFetch, len(blob))
	}
}

// A body over the cap must leave nothing behind: the staging file is partially
// written by the time the limit is detected.
func TestFetchCLIRemovesTempWhenOverCap(t *testing.T) {
	old := maxCLIBytes
	maxCLIBytes = 5
	defer func() { maxCLIBytes = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zstdOf(t, bytes.Repeat([]byte("x"), 64)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, _, err := fetchCLI(srv.URL, dir); err == nil || err.Error() != "response exceeds 5 bytes" {
		t.Fatalf("over-cap body = %v, want the cap error", err)
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
		blob := zstdOf(t, body)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(blob)
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

// zstdDecompress must reject a blob that decompresses beyond the cap. Without
// this guard a tiny .zst file expands unbounded on disk (a zstd bomb): the
// reference was measured writing past 200 MB from a small blob and still going
// when the probe was stopped — S6.
//
// The comment used to say "hundreds of GB". Nothing measured that; the probe was
// stopped at 200 MB. The unbounded direction is what S6 supports, not a figure.
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

// runCLIVersion tells three outcomes apart: exit 0, a failed run and a run that
// the bound stopped. A CLI under the bound is not stopped.
func TestRunCLIVersion(t *testing.T) {
	dir := t.TempDir()
	// .exe suffix: Go's exec on Windows only resolves paths that carry an
	// extension (harmless on Unix, where the shebang decides).
	ok := filepath.Join(dir, "ok.exe")
	if err := os.WriteFile(ok, fakeCLI(t, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runCLIVersion(ok, time.Minute); got != cliRunOK {
		t.Errorf("an exit-0 CLI: result %d, want cliRunOK", got)
	}
	bad := filepath.Join(dir, "bad.exe")
	if err := os.WriteFile(bad, fakeCLI(t, 3), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runCLIVersion(bad, time.Minute); got != cliRunFailed {
		t.Errorf("an exit-3 CLI: result %d, want cliRunFailed", got)
	}
	if got := runCLIVersion(filepath.Join(dir, "missing"), time.Minute); got != cliRunFailed {
		t.Errorf("a missing path: result %d, want cliRunFailed", got)
	}
	// A 1 s CLI (the leak budget of slowCLI) against both sides of a bound.
	slow := filepath.Join(dir, "slow.exe")
	if err := os.WriteFile(slow, slowCLI(t, 1), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runCLIVersion(slow, 100*time.Millisecond); got != cliRunStopped {
		t.Errorf("a 1s CLI under a 100ms bound: result %d, want cliRunStopped", got)
	}
	if got := runCLIVersion(slow, 30*time.Second); got != cliRunOK {
		t.Errorf("a 1s CLI under a 30s bound: result %d, want cliRunOK", got)
	}
}

// setCLIDownloadTimeout sets the package-level download bound for one test and
// restores it afterwards, so a failure cannot leak a bound into the rest of the
// suite (every other fetchCLI caller here expects the default: none).
func setCLIDownloadTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := cliDownloadTimeout
	cliDownloadTimeout = d
	t.Cleanup(func() { cliDownloadTimeout = old })
}

// D12: the download bound is opt-in and OFF by default, so an honest-but-slow
// download completes — which is what the reference does (measured at 5db5e4a: it
// was still downloading at 400 s against a server that never sends a body).
//
// The three arms discriminate against different mistakes. Arm 1 fails if the
// bound is ever applied by default. Arm 2 fails if an opted-in bound stops being
// applied. Arm 3 fails if the bound is applied with the wrong sign or unit. An
// assertion of only "rejected" cannot tell an applied deadline from one that
// always expires.
func TestFetchCLI_DownloadTimeoutOptIn(t *testing.T) {
	// Sends headers immediately, then stalls before the body. http.Client.Timeout
	// covers the whole exchange, so this is what an honest-but-slow link looks
	// like to it.
	blob := zstdOf(t, []byte("payload-bytes"))
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(600 * time.Millisecond)
		_, _ = w.Write(blob)
	}))
	defer slow.Close()

	setCLIDownloadTimeout(t, 0)
	if _, _, err := fetchCLI(slow.URL, t.TempDir()); err != nil {
		t.Errorf("with the bound disabled (the default), a slow-but-honest download must succeed, got: %v", err)
	}

	setCLIDownloadTimeout(t, 100*time.Millisecond)
	if _, _, err := fetchCLI(slow.URL, t.TempDir()); err == nil {
		t.Error("with a 100ms bound opted in, a 600ms download must fail")
	}

	setCLIDownloadTimeout(t, 10*time.Second)
	if _, _, err := fetchCLI(slow.URL, t.TempDir()); err != nil {
		t.Errorf("a 600ms download must still succeed under a 10s opted-in bound, got: %v", err)
	}
}

// The default must be 0 — no bound — because that is the parity position and the
// whole point of the flip. Asserted on the package variable AND on the flag's
// DECLARED default: moving a non-zero value into flag.Duration's default argument
// ships a bounded binary while leaving the package variable at zero.
func TestCLIDownloadTimeoutDefaultsOff(t *testing.T) {
	if cliDownloadTimeout != 0 {
		t.Fatalf("cliDownloadTimeout default = %s, want 0 (no bound = reference parity)", cliDownloadTimeout)
	}
	if got := (config{}).effectiveCLIDownloadTimeout(0, false); got != 0 {
		t.Fatalf("resolved default = %s, want 0", got)
	}
	// The declared default, not the resolved one: a real claustrum.conf sets the
	// resolved value legitimately, and a test reading it fails for a correct reason.
	if _, exited := runMain(t, "-install"); exited {
		t.Fatal("-install should return, not exit")
	}
	f := lastMainFlagSet.Lookup("cli-download-timeout")
	if f == nil {
		t.Fatal("main() did not register -cli-download-timeout")
	}
	if f.DefValue != "0s" {
		t.Fatalf("-cli-download-timeout declared default = %q, want \"0s\" (no bound = reference parity)", f.DefValue)
	}
}

func TestHTTPGet(t *testing.T) {
	blob := zstdOf(t, []byte("body-bytes"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = w.Write(blob)
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

	// The cap applies to the body and to the decompressed CLI. A body of one
	// byte decompresses to fewer bytes than the body has.
	small := zstdOf(t, []byte("x"))
	exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(small)
	}))
	defer exact.Close()

	t.Run("size_limit_exceeded", func(t *testing.T) {
		old := maxCLIBytes
		maxCLIBytes = int64(len(small)) - 1
		defer func() { maxCLIBytes = old }()
		if _, err := fetchBytes(t, exact.URL); err == nil {
			t.Error("expected error when response exceeds maxCLIBytes")
		}
	})

	t.Run("size_at_exact_limit_ok", func(t *testing.T) {
		old := maxCLIBytes
		maxCLIBytes = int64(len(small))
		defer func() { maxCLIBytes = old }()
		b, err := fetchBytes(t, exact.URL)
		if err != nil {
			t.Fatalf("a response of exactly maxCLIBytes must succeed, got: %v", err)
		}
		if string(b) != "x" {
			t.Errorf("body = %q, want %q", b, "x")
		}
	})
}

func TestDetectLibc(t *testing.T) {
	got := detectLibc()
	if runtime.GOOS != "linux" {
		// claustrum reports an empty libc off linux. It used to answer "glibc" on
		// Windows and macOS. The reference reports an empty libc on a Windows guest
		// (see libc_other.go). The darwin side is not probe-measured. This assertion
		// pins that the `libc` key is always present and always empty there.
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
// still return within its bound.
func TestDetectLibcWithTimeout(t *testing.T) {
	hung := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done() // mimic a stalled `ldd`: unblocks only when the bound fires
		return nil, ctx.Err()
	}
	noMusl := func(string) ([]string, error) { return nil, nil }
	done := make(chan string, 1)
	go func() { done <- detectLibcWith(20*time.Millisecond, hung, noMusl) }()
	select {
	case got := <-done:
		if got != "glibc" {
			t.Errorf("detectLibcWith(timeout) = %q, want the glibc fallback", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detectLibcWith did not return after its bound — ldd timeout not enforced")
	}
}

// The shipped bound is the reference's 5 s. Measured on a Linux VM from 19f30c46
// on: a 4.9 s stall answers, and a 5.1 s stall is cut at 5.0 s.
func TestLddProbeTimeoutIsFiveSeconds(t *testing.T) {
	if lddProbeTimeout != 5*time.Second {
		t.Errorf("lddProbeTimeout = %s, want 5s, the reference's measured bound", lddProbeTimeout)
	}
}

// Both sides of the bound on ONE runner shape. The runner prints a musl banner
// after a delay. Inside the bound its output decides: musl. Past the bound the
// output is dropped and the loader glob (which misses here) gives glibc.
func TestDetectLibcAnswerInsideVersusPastTheBound(t *testing.T) {
	slowMusl := func(ctx context.Context) ([]byte, error) {
		select {
		case <-time.After(200 * time.Millisecond):
			return []byte("musl libc (x86_64)\nVersion 1.2.4"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	noMusl := func(string) ([]string, error) { return nil, nil }
	if got := detectLibcWith(5*time.Second, slowMusl, noMusl); got != "musl" {
		t.Errorf("inside the bound detectLibcWith = %q, want musl — a 200ms ldd must be waited for", got)
	}
	if got := detectLibcWith(20*time.Millisecond, slowMusl, noMusl); got != "glibc" {
		t.Errorf("past the bound detectLibcWith = %q, want the glibc fallback", got)
	}
}

// When the bound fires, output that ldd already wrote is DROPPED, and the loader
// glob decides. Measured from 19f30c46 on: a stub that prints a musl banner at
// once and then stalls reports glibc with no marker and musl with the marker.
// The runner here returns its partial output when the context ends, which is
// what CombinedOutput hands back after a kill.
func TestDetectLibcDropsPartialOutputAtTheBound(t *testing.T) {
	partial := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return []byte("musl libc (x86_64)\n"), ctx.Err()
	}
	noMusl := func(string) ([]string, error) { return nil, nil }
	found := func(string) ([]string, error) { return []string{"/lib/ld-musl-x86_64.so.1"}, nil }
	if got := detectLibcWith(20*time.Millisecond, partial, noMusl); got != "glibc" {
		t.Errorf("partial musl banner, no marker: detectLibcWith = %q, want glibc — output "+
			"written before the bound must be dropped", got)
	}
	if got := detectLibcWith(20*time.Millisecond, partial, found); got != "musl" {
		t.Errorf("partial output, marker present: detectLibcWith = %q, want musl from the glob", got)
	}
}

// classifyLibc's branches with an injected glob, so the musl paths are exercised
// on a glibc host too. The order matches reference build 3ef9370: the `ldd`
// output decides first, and the loader glob is only the empty-output fallback.
func TestClassifyLibc(t *testing.T) {
	found := func(string) ([]string, error) { return []string{"/lib/ld-musl-aarch64.so.1"}, nil }
	none := func(string) ([]string, error) { return nil, nil }
	broken := func(string) ([]string, error) { return nil, filepath.ErrBadPattern }
	cases := []struct {
		name string
		out  []byte
		glob func(string) ([]string, error)
		want string
	}{
		{"ldd musl banner", []byte("musl libc (x86_64)\nVersion 1.2.3"), none, "musl"},
		{"ldd glibc banner", []byte("ldd (GNU libc) 2.31"), none, "glibc"},
		{"no ldd output, loader present", nil, found, "musl"},
		{"no ldd output, no loader", nil, none, "glibc"},
		// The mixed host: glibc's ldd produces glibc output AND a musl loader is
		// installed. Build 4534d86 answered musl (glob first); 3ef9370 answers
		// glibc, because ldd produced non-musl output and the glob is never
		// reached. This row flips with the ordering.
		{"mixed host: ldd says glibc, loader present", []byte("ldd (Debian GLIBC 2.41) 2.41"), found, "glibc"},
		// A musl banner in any ldd output outranks the loader lookup, which is not
		// consulted while output is present.
		{"musl banner beats loader lookup", []byte("musl libc"), found, "musl"},
		// A non-x86_64 loader must match on the empty-output fallback — the old
		// hardcoded path could not.
		{"arm64 loader, no ldd output", nil, found, "musl"},
		// A glob error on the fallback is not a match.
		{"no ldd output, glob errors", nil, broken, "glibc"},
	}
	for _, tc := range cases {
		if got := classifyLibc(tc.out, tc.glob); got != tc.want {
			t.Errorf("%s: classifyLibc = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDetectLibcOrderingLddFirst pins the ldd-first ordering of build 3ef9370
// through detectLibcWith, whose signature did not change with the reorder, so
// reverting the reorder still compiles here and fails on these two rows — the
// mutant-faithful discriminators. Both were measured against the 3ef9370 binary.
//
//   - Mixed host: glibc `ldd` output plus a musl loader marker. Build 4534d86
//     (glob first) reports musl; 3ef9370 reports glibc, because ldd produced
//     non-musl output and the glob is never reached.
//   - Musl banner with a non-zero ldd exit, measured in a glob-miss environment
//     (no loader marker, so no fallback can rescue the answer). The old glob-first
//     code gated the banner on lddErr == nil and reported glibc; 3ef9370 ignores
//     the exit code, so the banner alone gives musl.
func TestDetectLibcOrderingLddFirst(t *testing.T) {
	found := func(string) ([]string, error) { return []string{"/lib/ld-musl-x86_64.so.1"}, nil }
	none := func(string) ([]string, error) { return nil, nil }

	glibcLdd := func(context.Context) ([]byte, error) {
		return []byte("ldd (Debian GLIBC 2.41) 2.41"), nil
	}
	if got := detectLibcWith(lddProbeTimeout, glibcLdd, found); got != "glibc" {
		t.Errorf("mixed host: detectLibcWith = %q, want glibc — ldd output must decide "+
			"before the loader glob is consulted", got)
	}

	muslBannerExit1 := func(context.Context) ([]byte, error) {
		return []byte("musl libc (x86_64)\nVersion 1.2.5"), io.EOF
	}
	if got := detectLibcWith(lddProbeTimeout, muslBannerExit1, none); got != "musl" {
		t.Errorf("musl banner, ldd exit 1: detectLibcWith = %q, want musl — the exit code "+
			"must not be consulted", got)
	}
}

func TestPruneCLI(t *testing.T) {
	dir := t.TempDir()
	// Four version files with strictly increasing mtimes.
	names := []string{vf("1.0.1"), vf("1.0.2"), vf("1.0.3"), vf("1.0.4")}
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(n), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(int64(1000+i*100), 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	pruneCLI(dir, 2, "") // keep the 2 newest
	for _, n := range names[:2] {
		if isRegularFile(filepath.Join(dir, n)) {
			t.Errorf("%s should have been pruned", n)
		}
	}
	for _, n := range names[2:] {
		if !isRegularFile(filepath.Join(dir, n)) {
			t.Errorf("%s should have been kept", n)
		}
	}
}

// runInstall's prune step is gated on an install having actually succeeded.
// A cache-hit run neither sweeps orphans nor prunes, and a FAILED
// install sweeps but does not prune (probe-measured at 5db5e4a). A keep of 0
// prunes every older version file, and the new CLI stays (cells L5, M2a and
// W4a, 5fd08069).
//
// This test previously drove runInstall with an already-present CLI and asserted
// that it pruned — encoding claustrum's divergence. It now exercises the guard on
// the install path, where prune belongs, and pins the cache-hit case separately.
func TestRunInstallHonorsCliKeepGuard(t *testing.T) {
	mk := func(t *testing.T, dir, name string, ageSec int) {
		t.Helper()
		p := filepath.Join(dir, vf(name))
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
		if err := os.WriteFile(f, fakeCLIZst(t, 0), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Version-style names (with a dot) so a present CLI is actually runnable on
	// Windows too — exec there only resolves paths carrying an extension, and an
	// extensionless name would silently take the not-present branch.
	t.Run("keep0_prunes_every_older_version", func(t *testing.T) {
		dir := t.TempDir()
		mk(t, dir, "3.0.0", 30)
		mk(t, dir, "2.0.0", 20)
		mk(t, dir, "1.0.0", 10)
		f := captureInstallFacts(t, installOpts{
			cliDir: dir, cliVersion: "9.0.0", cliZst: blob(t), cliKeep: keepZero})
		if n := count(t, dir); n != 1 || !isRegularFile(installCLIPath(dir, "9.0.0")) {
			t.Errorf("keep=0 left %d files, want the new CLI only (cell L5)", n)
		}
		if f.CliError != "" || f.CliPath != installCLIPath(dir, "9.0.0") {
			t.Errorf("cliError %q, cliPath %q: want the result line of a good install", f.CliError, f.CliPath)
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
			t.Errorf("keep=2 left %d files, want 2: the prune did not run", n)
		}
	})

	// A cache-hit run does not prune. It removes only an old "*.zst.part" entry.
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

// TestSweepFetchTemps pins the orphan sweep against a fixed "now", so the ages
// are exact and no test sleeps. The rules:
//   - A swept name goes only when its mtime is more than 600 s old. 599 s stays,
//     601 s goes, and a future mtime stays (Linux VM, 4534d86 through f6010b97).
//   - Only a regular file goes. An empty folder ".fetch-9" and a folder
//     ".fetch-e" with content stay (5fd08069, cells L8, M3a and W5).
//   - The names are ".fetch-<digits>" and "<version>.zst", case-sensitive
//     (5fd08069, the same cells).
func TestSweepFetchTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-601 * time.Second)
	young := now.Add(-599 * time.Second)
	future := now.Add(24 * time.Hour)
	age := func(name string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(dir, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		age(name, at)
	}
	mkdir := func(name string, at time.Time) {
		t.Helper()
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		age(name, at)
	}
	write(".fetch-1", old)
	write(".fetch-2", young)
	write(".fetch-3", future)
	write("1.0.0.zst", old)
	write("1.0.1.zst", young)
	mkdir(".fetch-4", old)
	mkdir("1.0.2.zst", old)
	if err := os.Mkdir(filepath.Join(dir, ".fetch-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(".fetch-5", "inner"), old)
	age(".fetch-5", old) // after the inner write, which moved the dir's mtime
	// Names the sweep never claims, all old enough to go if it did.
	controls := []string{"1.0.0", ".fetch-", ".zst", ".fetch-old", "old.zst",
		"x.fetch-1", "fetch-1", ".fetch1", "1.0.0.zst.bak", "zst"}
	// On a folder that ignores letter case, ".FETCH-1" names the file
	// ".fetch-1". The letter-case controls run only where the case counts.
	if folderIsCaseSensitive(t) {
		controls = append(controls, ".FETCH-1", "1.0.0.ZST")
	} else {
		t.Log("the temp folder ignores letter case: the letter-case controls do not run")
	}
	for _, c := range controls {
		write(c, old)
	}

	sweepFetchTemps(dir, now)

	got := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		got[e.Name()] = true
	}
	for _, gone := range []string{".fetch-1", "1.0.0.zst"} {
		if got[gone] {
			t.Errorf("%s (601 s old) survived the sweep", gone)
		}
	}
	for _, kept := range []string{".fetch-2", "1.0.1.zst"} {
		if !got[kept] {
			t.Errorf("%s (599 s old) was swept; only entries more than 600 s old go", kept)
		}
	}
	if !got[".fetch-3"] {
		t.Error(".fetch-3 (mtime in the future) was swept; a future mtime is not old")
	}
	for _, kept := range []string{".fetch-4", "1.0.2.zst", ".fetch-5"} {
		if !got[kept] {
			t.Errorf("the folder %s was swept; only a regular file goes", kept)
		}
	}
	if !isRegularFile(filepath.Join(dir, ".fetch-5", "inner")) {
		t.Error("the file in .fetch-5 was removed")
	}
	for _, c := range controls {
		if !got[c] {
			t.Errorf("the sweep removed %q, a name it does not claim", c)
		}
	}
}

// TestPruneBudgetIgnoresOrphans is the regression for the bug behind F3: orphan
// temps counted as CLI *versions*, so they consumed the -cli-keep budget. With
// three orphans, four versions and keep=3, every real version was evicted —
// including the one just installed.
func TestPruneBudgetIgnoresOrphans(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"} {
		p := filepath.Join(dir, vf(name))
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
	if err := os.WriteFile(zstFile, fakeCLIZst(t, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	facts := captureInstallFacts(t, installOpts{
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
	want := []string{".fetch-a", ".fetch-b", ".fetch-c", vf("3.0.0"), vf("4.0.0"), vf("5.0.0")}
	if len(left) != len(want) {
		t.Fatalf("cli-dir = %v, want %v (orphans must not consume the keep budget); install result: %+v", left, want, facts)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("cli-dir = %v, want %v", left, want)
		}
	}
}

func TestEnsureCLIFromZst(t *testing.T) {
	dir := t.TempDir()
	zst := fakeCLIZst(t, 0)
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
	// Atomic extract (#4): the `.fetch-*` staging file is renamed into place, not
	// left behind.
	assertNoStagingLeftover(t, cliPath)
}

func TestEnsureCLIFromURL(t *testing.T) {
	zst := fakeCLIZst(t, 0)
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

// The -cli-zst (SFTP) path verifies -cli-checksum WHEN one is supplied, as the
// reference does since 7d193f89: a wrong checksum is rejected like the -cli-url
// path, and the source blob is left intact. An ABSENT/empty checksum verifies
// nothing. The compare is case-sensitive, measured on a Linux VM.
func TestEnsureCLIZstVerifiesChecksumWhenSupplied(t *testing.T) {
	mkZst := func(t *testing.T) (dir, zstFile string) {
		dir = t.TempDir()
		zstFile = filepath.Join(dir, "cli.zst")
		if err := os.WriteFile(zstFile, fakeCLIZst(t, 0), 0o644); err != nil {
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

	// The reference compares case-sensitively, so the RIGHT digest in upper case
	// fails, and the message carries both spellings. Measured on a Linux VM
	// against 7d193f89 through f6010b97. strings.EqualFold installed here.
	t.Run("upper-case right checksum is rejected", func(t *testing.T) {
		dir, zstFile := mkZst(t)
		zst, _ := os.ReadFile(zstFile)
		sum := sha256.Sum256(zst)
		lower := hex.EncodeToString(sum[:])
		upper := strings.ToUpper(lower)
		cliDir := filepath.Join(dir, "cli")
		err := ensureCLI(installOpts{cliZst: zstFile, cliChecksum: upper}, filepath.Join(cliDir, "out.exe"))
		want := "checksum mismatch: expected=" + upper + ", actual=" + lower
		if err == nil || err.Error() != want {
			t.Fatalf("zst + upper-case checksum = %v, want %q", err, want)
		}
		if !isRegularFile(zstFile) {
			t.Error("a failed checksum must not consume the source .zst")
		}
		if ents, rerr := os.ReadDir(cliDir); rerr != nil || len(ents) != 0 {
			t.Errorf("cli-dir after the mismatch: entries=%v err=%v, want an empty dir", ents, rerr)
		}
	})

	t.Run("absent checksum verifies nothing", func(t *testing.T) {
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

// The -cli-zst path creates the cli-dir, mode 0700 with its parents, BEFORE it
// opens the blob. So a failed attempt past the version check leaves the cli-dir,
// empty if it was new. Measured on a Linux VM against 5db5e4a through f6010b97,
// a missing blob included, and with absent parents. claustrum created it only at
// the decompress step.
func TestEnsureCLIZstCreatesTheCliDirFirst(t *testing.T) {
	for _, tc := range []struct{ name, blob, checksum, wantPrefix string }{
		{"missing blob", "absent.zst", "", "opening input:"},
		{"wrong checksum", "cli.zst", "deadbeef", "checksum mismatch:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "cli.zst"), fakeCLIZst(t, 0), 0o600); err != nil {
				t.Fatal(err)
			}
			cliDir := filepath.Join(root, "p1", "p2", "cli")
			o := installOpts{cliZst: filepath.Join(root, tc.blob), cliChecksum: tc.checksum}
			err := ensureCLI(o, filepath.Join(cliDir, "1.0.0"))
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Fatalf("ensureCLI = %v, want a %q error", err, tc.wantPrefix)
			}
			ents, rerr := os.ReadDir(cliDir)
			if rerr != nil || len(ents) != 0 {
				t.Fatalf("cli-dir after a failed attempt: entries=%v err=%v, want it created and empty", ents, rerr)
			}
			if runtime.GOOS == "windows" {
				return // Windows has no POSIX mode bits to compare
			}
			for _, d := range []string{filepath.Join(root, "p1"), filepath.Join(root, "p1", "p2"), cliDir} {
				fi, serr := os.Stat(d)
				if serr != nil {
					t.Fatal(serr)
				}
				if fi.Mode().Perm() != 0o700 {
					t.Errorf("%s mode = %v, want 0700", d, fi.Mode().Perm())
				}
			}
		})
	}
}

// The -cli-url (download) path verifies UNCONDITIONALLY: a wrong checksum and an
// empty checksum both fail (the reference checks even when -cli-checksum is "").
func TestEnsureCLIURLVerifiesUnconditionally(t *testing.T) {
	zst := fakeCLIZst(t, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	for _, tc := range []struct{ name, checksum string }{
		{"wrong checksum", "deadbeef"},
		{"empty checksum", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A folder with content at the CLI path. The body decompresses into
			// the staging file. The checksum comes before the clear and the
			// rename, so the folder and its file stay (claustrum's own reading).
			cliPath := filepath.Join(t.TempDir(), "1.0.0")
			inner := filepath.Join(cliPath, "inner.txt")
			if err := os.Mkdir(cliPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inner, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			o := installOpts{cliURL: srv.URL + "/claude.zst", cliChecksum: tc.checksum}
			err := ensureCLI(o, cliPath)
			if err == nil || !strings.HasPrefix(err.Error(), "checksum mismatch: ") {
				t.Errorf("download with %s = %v, want a checksum-mismatch error", tc.name, err)
			}
			if !isRegularFile(inner) {
				t.Error("the folder at the CLI path was cleared before the checksum passed")
			}
			assertNoStagingLeftover(t, cliPath)
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
	// A DIRECTORY at -cli-zst opens, and its read error comes from the decoder.
	// On 5fd08069 the text is "decompressing: read <p>: is a directory" (cell
	// L13, Linux VM). Other systems get a prefix check only.
	asDir := filepath.Join(dir, "adir.zst")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "decompressing: read " + asDir + ": is a directory"
	if err := ensureCLI(installOpts{cliZst: asDir}, filepath.Join(dir, "out3")); err == nil ||
		!strings.HasPrefix(err.Error(), "decompressing: ") || (runtime.GOOS == "linux" && err.Error() != want) {
		t.Errorf("directory as zst = %v, want %q", err, want)
	}
	// An EMPTY blob gets past the open. An empty zstd stream yields no bytes, so
	// it fails at the runnability check, not with "opening input:".
	emptyZst := filepath.Join(dir, "empty.zst")
	if err := os.WriteFile(emptyZst, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: emptyZst}, filepath.Join(dir, "out4")); err == nil ||
		strings.HasPrefix(err.Error(), "opening input:") {
		t.Errorf("empty zst = %v, want a later failure, not 'opening input:'", err)
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
	zst := fakeCLIZst(t, 0)
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
	// libc is reported on linux only. Off linux claustrum emits an empty string,
	// so "" is the correct value there rather than a missing one.
	if runtime.GOOS == "linux" && f.Libc == "" {
		t.Errorf("facts missing libc on linux: %+v", f)
	}
	if runtime.GOOS != "linux" && f.Libc != "" {
		t.Errorf("facts carry libc=%q on %s, want empty", f.Libc, runtime.GOOS)
	}
	if f.CliPath != filepath.Join(o.cliDir, o.cliVersion+cliExeSuffix) {
		t.Errorf("cliPath = %q", f.CliPath)
	}

	// Second install: the CLI is now present + runnable → reported as present.
	f2 := captureInstallFacts(t, o)
	if !f2.CliWasPresent {
		t.Error("a second install should report cliWasPresent=true")
	}
}

// keepZero is a -cli-keep of 0 for the two capture helpers. A keep of 0 prunes
// every older version file (cells L5, M2a and W4a). A test that leaves cliKeep
// unset wants no prune, so testKeep turns the unset value into the largest
// count, with which the prune removes nothing. A negative value counts as 0.
const keepZero = math.MinInt

func testKeep(o installOpts) installOpts {
	switch o.cliKeep {
	case 0:
		o.cliKeep = math.MaxInt
	case keepZero:
		o.cliKeep = 0
	}
	return o
}

// captureInstallFacts runs runInstall and parses the __INSTALL_RESULT__ JSON it
// prints to stdout. See keepZero for an unset cliKeep.
func captureInstallFacts(t *testing.T, o installOpts) installFacts {
	t.Helper()
	o = testKeep(o)
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
	sweepFetchTemps(filepath.Join(t.TempDir(), "no-such-dir"), time.Now())
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
	// it blocks for the transport's 30 s dial timeout instead of failing.
	err = ensureCLI(installOpts{cliURL: refusedURL(t), cliChecksum: "x"},
		filepath.Join(dir, "v2"))
	if err == nil || !strings.HasPrefix(err.Error(), "download failed: ") {
		t.Errorf("transport failure = %v, want a \"download failed: \" prefix", err)
	}
}

// A read-idle stall (4534d86's download abort) is reported with the reference's
// bare wording — "download stalled: no data for Ns after G/T bytes" — and is NOT
// prefixed "download failed: ". ensureCLI wrapped every non-status fetch error, so
// the stall used to reach __INSTALL_RESULT__ as "download failed: download stalled:
// …", changing the Desktop-facing bytes. Measured bare against 4534d86
// (scratch/probe/install-frames-4534d86.md).
func TestDownloadStallErrorWording(t *testing.T) {
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Second
	oldProg := installProgressInterval
	installProgressInterval = time.Hour // only the leading bytes:0 line, quiet output
	t.Cleanup(func() { installIdleTimeout = oldIdle; installProgressInterval = oldProg })

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zstdOf(t, []byte("payload"))[:4]) // 4 bytes, then go silent
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block // stall: never send the rest
	}))
	// srv.Close blocks until the handler returns, so unblock it FIRST (defers run
	// LIFO — this runs before srv.Close).
	defer srv.Close()
	defer close(block)

	err := ensureCLI(installOpts{cliURL: srv.URL, cliChecksum: "x"},
		filepath.Join(t.TempDir(), "v1"))
	if err == nil {
		t.Fatal("ensureCLI on a stalled download succeeded")
	}
	const want = "download stalled: no data for 1s after 4/1000000 bytes"
	if got := err.Error(); got != want {
		t.Errorf("ensureCLI stall cliError = %q, want it unwrapped and exactly %q", got, want)
	}
	if strings.HasPrefix(err.Error(), "download failed: ") {
		t.Errorf("stall error carries the transport-only \"download failed: \" prefix: %q", err.Error())
	}
}

// `ldd` runs on EVERY call since build 3ef9370, and its output outranks the
// loader glob: a glibc banner wins even when a musl loader marker is present.
//
// This is the behaviour, not an optimisation. Build 4534d86 did the reverse — a
// present marker skipped ldd entirely — and claustrum matched that until this
// build. This test guarded the older skip and now guards its replacement: that
// ldd IS executed and its output decides while any output is present.
func TestDetectLibcRunsLddEvenWhenLoaderGlobMatches(t *testing.T) {
	called := false
	run := func(context.Context) ([]byte, error) {
		called = true
		return []byte("ldd (Debian GLIBC 2.41-1) 2.41"), nil
	}
	muslPresent := func(string) ([]string, error) {
		return []string{"/lib/ld-musl-x86_64.so.1"}, nil
	}

	if got := detectLibcWith(time.Second, run, muslPresent); got != "glibc" {
		t.Errorf("detectLibcWith = %q, want glibc — ldd output outranks a present loader marker", got)
	}
	if !called {
		t.Error("ldd was not executed — it runs on every call since 3ef9370")
	}
}

// The companion: with no marker either, the probe still runs and its output
// decides. The pair proves ldd runs regardless of the marker state.
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
		t.Error("ldd was not executed with no loader marker present")
	}
}

// The CONSUME side of the boundary: once the new CLI is in place, the uploaded
// blob is consumed even though the install then fails.
//
// Measured 2026-08-02 against 5db5e4a across four fixtures — a runnable CLI
// (consumed, the control), one that exits 1 (consumed), a blob that is not valid
// zstd (kept), and an absent blob. claustrum kept the blob on row two.
func TestEnsureCLIConsumesBlobWhenExtractedCLIIsNotRunnable(t *testing.T) {
	dir := t.TempDir()
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, fakeCLIZst(t, 1), 0o644); err != nil {
		t.Fatal(err)
	}

	// .exe so Go's exec resolves it on the Windows leg, as the sibling tests do.
	err := ensureCLI(installOpts{cliZst: zstFile}, filepath.Join(dir, "out.exe"))
	if err == nil || !strings.Contains(err.Error(), "not runnable") {
		t.Fatalf("want a not-runnable error, got %v", err)
	}
	if isRegularFile(zstFile) {
		t.Error("the blob must be consumed once the new CLI is in place — " +
			"the reference consumes it even when the extracted CLI does not run")
	}
}

// A failed chmod of the staging file keeps the -cli-zst blob. On 5fd08069 every
// failure before the new CLI is in place keeps it (cells L3, L12, M5, W1 and
// W6). The chmod failure itself is claustrum's own (not measured). The failure
// is injected through the chmodStaged seam, so the test is identical on every
// OS.
func TestEnsureCLIKeepsBlobWhenChmodFails(t *testing.T) {
	old := chmodStaged
	chmodStaged = func(string, os.FileMode) error { return errors.New("chmod refused") }
	t.Cleanup(func() { chmodStaged = old })

	dir := t.TempDir()
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zstdOf(t, []byte("#\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureCLI(installOpts{cliZst: zstFile}, filepath.Join(dir, "out.exe"))
	if err == nil || !strings.Contains(err.Error(), "chmod refused") {
		t.Fatalf("want the injected chmod error, got %v", err)
	}
	if !isRegularFile(zstFile) {
		t.Error("the blob was consumed by an install that failed before the rename")
	}
	assertNoStagingLeftover(t, filepath.Join(dir, "out.exe"))
}

// captureInstallOutput runs runInstall and returns its raw stdout. See keepZero
// for an unset cliKeep.
func captureInstallOutput(t *testing.T, o installOpts) string {
	t.Helper()
	o = testKeep(o)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	os.Stdout = w
	runInstall(o)
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}

// The prune never counts a name the sweep claims, at any age. Measured on a Linux
// VM against f6010b97 with three real CLIs (72 h, 48 h, 24 h old), a ".fetch-o"
// and an "x.zst", and one install of 2.0.0 with -cli-keep 3. With the litter
// fresh, the reference keeps 1.1.0, 1.2.0, 2.0.0 and the litter. With the litter
// 601 s old (the control), the sweep takes it and the same three CLIs stay. The
// litter names here are those that the sweep of 5fd08069 claims.
func TestPruneIgnoresFreshSweepLitter(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		want []string
	}{
		{"fresh litter", 0, []string{".fetch-1", vf("1.1.0"), vf("1.2.0"), "1.9.0.zst", vf("2.0.0")}},
		{"601 s litter (control)", 601 * time.Second, []string{vf("1.1.0"), vf("1.2.0"), vf("2.0.0")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cli")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			put := func(name string, body []byte, age time.Duration) {
				t.Helper()
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, body, 0o755); err != nil {
					t.Fatal(err)
				}
				at := now.Add(-age)
				if err := os.Chtimes(p, at, at); err != nil {
					t.Fatal(err)
				}
			}
			put(vf("1.0.0"), fakeCLI(t, 0), 72*time.Hour)
			put(vf("1.1.0"), fakeCLI(t, 0), 48*time.Hour)
			put(vf("1.2.0"), fakeCLI(t, 0), 24*time.Hour)
			put(".fetch-1", nil, tc.age)
			put("1.9.0.zst", nil, tc.age)
			blob := filepath.Join(t.TempDir(), "cli.zst")
			if err := os.WriteFile(blob, fakeCLIZst(t, 0), 0o600); err != nil {
				t.Fatal(err)
			}
			f := captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: "2.0.0", cliZst: blob, cliKeep: 3})
			if f.CliError != "" {
				t.Fatalf("CliError = %q, want success", f.CliError)
			}
			ents, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var left []string
			for _, e := range ents {
				left = append(left, e.Name())
			}
			sort.Strings(left)
			if strings.Join(left, " ") != strings.Join(tc.want, " ") {
				t.Errorf("cli-dir = %v, want %v", left, tc.want)
			}
		})
	}
}

// The -cli-url path creates the cli-dir (0700, with parents) before any network
// access, and every failure leaves it empty. Measured on a Linux VM against
// f6010b97: a checksum mismatch, a 404 and a refused connection each leave an
// empty 0700 cli-dir, and the cli-dir exists before the server sends headers.
func TestEnsureCLIURLCreatesTheCliDirFirst(t *testing.T) {
	zst := fakeCLIZst(t, 0)
	sum := sha256.Sum256(zst)
	right := hex.EncodeToString(sum[:])

	var mu sync.Mutex
	var cliDir string
	var sawDir bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fi, err := os.Stat(cliDir)
		sawDir = err == nil && fi.IsDir()
		mu.Unlock()
		if r.URL.Path == "/missing.zst" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(zst)
	}))
	defer srv.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL + "/cli.zst"
	dead.Close()

	for _, tc := range []struct {
		name, url, checksum, wantPrefix string
		asksServer                      bool
	}{
		{"wrong checksum", srv.URL + "/cli.zst", "deadbeef", "checksum mismatch:", true},
		{"upper-case checksum", srv.URL + "/cli.zst", strings.ToUpper(right), "checksum mismatch:", true},
		{"404", srv.URL + "/missing.zst", right, "download failed with status 404", true},
		{"unreachable", deadURL, right, "download failed:", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "p1", "cli")
			mu.Lock()
			cliDir, sawDir = dir, false
			mu.Unlock()
			err := ensureCLI(installOpts{cliURL: tc.url, cliChecksum: tc.checksum}, filepath.Join(dir, "1.0.0"))
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Fatalf("ensureCLI = %v, want a %q error", err, tc.wantPrefix)
			}
			mu.Lock()
			saw := sawDir
			mu.Unlock()
			if tc.asksServer && !saw {
				t.Error("the cli-dir did not exist when the request reached the server")
			}
			ents, rerr := os.ReadDir(dir)
			if rerr != nil || len(ents) != 0 {
				t.Fatalf("cli-dir after the failure: entries=%v err=%v, want it created and empty", ents, rerr)
			}
			if runtime.GOOS == "windows" {
				return
			}
			for _, d := range []string{filepath.Join(root, "p1"), dir} {
				fi, serr := os.Stat(d)
				if serr != nil {
					t.Fatal(serr)
				}
				if fi.Mode().Perm() != 0o700 {
					t.Errorf("%s mode = %v, want 0700", d, fi.Mode().Perm())
				}
			}
		})
	}
}

// The final __INSTALL_PROGRESS__ line comes only after the checksum passes, and
// the leading bytes:0 line always comes first. Measured on a Linux VM against
// f6010b97, 5 of 5 runs per row: a good 57-byte download prints bytes:0 then
// bytes:57, and a mismatch or an empty checksum prints bytes:0 only.
func TestInstallProgressLinesFollowTheChecksum(t *testing.T) {
	zst := fakeCLIZst(t, 0)
	sum := sha256.Sum256(zst)
	// Content-Length is set explicitly. Without it a large body (the Windows fake
	// CLI is about 9 MB) goes out chunked, and the lines carry no "total".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	defer srv.Close()
	n := len(zst)
	lead := fmt.Sprintf(`__INSTALL_PROGRESS__{"phase":"download","bytes":0,"total":%d}`, n)
	final := fmt.Sprintf(`__INSTALL_PROGRESS__{"phase":"download","bytes":%d,"total":%d}`, n, n)
	for _, tc := range []struct {
		name, checksum string
		want           []string
	}{
		{"right checksum", hex.EncodeToString(sum[:]), []string{lead, final}},
		{"wrong checksum", "deadbeef", []string{lead}},
		{"no checksum", "", []string{lead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Several runs, because the old ticker-goroutine lead line raced the
			// first read and once printed bytes:57 with no bytes:0 line.
			for i := 0; i < 5; i++ {
				dir := filepath.Join(t.TempDir(), "cli")
				out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "1.0.0",
					cliURL: srv.URL + "/cli.zst", cliChecksum: tc.checksum})
				var got []string
				for _, l := range strings.Split(out, "\n") {
					if strings.HasPrefix(l, "__INSTALL_PROGRESS__") {
						got = append(got, l)
					}
				}
				if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
					t.Fatalf("run %d: progress lines =\n%s\nwant\n%s", i, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
				}
			}
		})
	}
}

// A cache miss with no source flag still creates the cli-dir (0700, with its
// parents) before the source check, and leaves it empty. Measured on a Linux VM
// against f6010b97, two runs: the answer is the same text on both binaries, and
// the reference leaves an empty 0700 cli-dir.
func TestInstallCacheMissWithNoSourceCreatesTheCliDir(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "p1", "cli")
	f := captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: "9.9.9"})
	if want := "cli 9.9.9 missing and no --cli-url or --cli-zst provided"; f.CliError != want {
		t.Fatalf("CliError = %q, want %q", f.CliError, want)
	}
	ents, err := os.ReadDir(cliDir)
	if err != nil || len(ents) != 0 {
		t.Fatalf("cli-dir after a no-source miss: entries=%v err=%v, want it created and empty", ents, err)
	}
	if runtime.GOOS == "windows" {
		return // Windows has no POSIX mode bits to compare
	}
	for _, d := range []string{filepath.Join(root, "p1"), cliDir} {
		fi, serr := os.Stat(d)
		if serr != nil {
			t.Fatal(serr)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", d, fi.Mode().Perm())
		}
	}
}

// A zero or negative bound arms no deadline, so the probe output is used. A
// disabled limiter bypasses its deadline, it never becomes an expired one.
func TestDetectLibcZeroTimeoutIsUnbounded(t *testing.T) {
	run := func(ctx context.Context) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []byte("musl libc (x86_64)\n"), nil
	}
	glob := func(string) ([]string, error) { return nil, nil }
	for _, d := range []time.Duration{0, -time.Second} {
		if got := detectLibcWith(d, run, glob); got != "musl" {
			t.Errorf("detectLibcWith(%v) = %q, want musl (no deadline armed)", d, got)
		}
	}
}

// folderIsCaseSensitive reports whether a new temp folder tells "a" from "A".
func folderIsCaseSensitive(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "A"))
	return os.IsNotExist(err)
}
