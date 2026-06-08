package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// runnableScript is a stand-in CLI: exits 0 for any args, so `<cli> --version`
// (the isRunnable check) succeeds.
const runnableScript = "#!/bin/sh\nexit 0\n"

func TestZstdDecompress(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("hello zstd payload")
	dest := filepath.Join(dir, "out")
	if err := zstdDecompress(zstdOf(t, payload), dest); err != nil {
		t.Fatalf("decompress: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("decompressed = %q, want %q", got, payload)
	}
	if err := zstdDecompress([]byte("not a zstd stream"), filepath.Join(dir, "x")); err == nil {
		t.Error("expected error decompressing non-zstd input")
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
	ok := filepath.Join(dir, "ok")
	if err := os.WriteFile(ok, []byte(runnableScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isRunnable(ok) {
		t.Error("an exit-0 script should be runnable")
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isRunnable(bad) {
		t.Error("an exit-3 script should not be runnable")
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

	b, err := httpGet(srv.URL + "/ok")
	if err != nil || string(b) != "body-bytes" {
		t.Fatalf("httpGet ok = %q, %v", b, err)
	}
	if _, err := httpGet(srv.URL + "/missing"); err == nil {
		t.Error("a non-200 response should error")
	}
}

func TestDetectLibc(t *testing.T) {
	if got := detectLibc(); got != "glibc" && got != "musl" {
		t.Errorf("detectLibc() = %q, want glibc or musl", got)
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

// runInstall's prune step is gated on `o.cliKeep > 0`. With keep=0 it must NOT
// prune (a boundary mutant `>= 0` would wipe every version); with keep>0 it must
// prune to the newest keep (a negated guard `<= 0` would skip pruning). Driving
// runInstall with a pre-populated, already-runnable CLI exercises the guard
// without a download.
func TestRunInstallHonorsCliKeepGuard(t *testing.T) {
	mk := func(dir, name string, ageSec int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(runnableScript), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(int64(1000+ageSec), 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	count := func(dir string) int {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}

	t.Run("keep0_does_not_prune", func(t *testing.T) {
		dir := t.TempDir()
		mk(dir, "cur", 30)
		mk(dir, "old1", 20)
		mk(dir, "old2", 10)
		_ = captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: "cur", cliKeep: 0})
		if n := count(dir); n != 3 {
			t.Errorf("keep=0 left %d files, want 3 (cliKeep>0 guard regressed to >=0, wiping versions)", n)
		}
	})

	t.Run("keep2_prunes_to_newest", func(t *testing.T) {
		dir := t.TempDir()
		mk(dir, "cur", 40) // newest → kept, and it's the present CLI
		mk(dir, "old1", 30)
		mk(dir, "old2", 20)
		mk(dir, "old3", 10)
		_ = captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: "cur", cliKeep: 2})
		if n := count(dir); n != 2 {
			t.Errorf("keep=2 left %d files, want 2 (cliKeep>0 guard regressed, skipping prune)", n)
		}
	})
}

func TestEnsureCLIFromZst(t *testing.T) {
	dir := t.TempDir()
	zst := zstdOf(t, []byte(runnableScript))
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
}

func TestEnsureCLIFromURL(t *testing.T) {
	zst := zstdOf(t, []byte(runnableScript))
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

func TestEnsureCLIChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	zstFile := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstFile, zstdOf(t, []byte(runnableScript)), 0o644); err != nil {
		t.Fatal(err)
	}
	o := installOpts{cliZst: zstFile, cliChecksum: "deadbeef"}
	if err := ensureCLI(o, filepath.Join(dir, "out")); err == nil {
		t.Error("a checksum mismatch should error")
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
	zst := zstdOf(t, []byte(runnableScript))
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
	if f.OS == "" || f.Arch == "" || f.Libc == "" {
		t.Errorf("facts missing os/arch/libc: %+v", f)
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
