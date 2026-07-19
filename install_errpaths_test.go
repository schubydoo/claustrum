package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// refusedURL returns an http URL on a port that was just listening and no
// longer is, so a connection attempt fails fast and deterministically.
func refusedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr + "/cli.zst"
}

// runInstall reports an ensureCLI failure through the CliError fact (never a
// crash or a bare error print) so the caller's JSON parse always succeeds.
func TestRunInstallReportsEnsureError(t *testing.T) {
	f := captureInstallFacts(t, installOpts{
		cliDir:     t.TempDir(),
		cliVersion: "v9.9.9",
		// no -cli-url and no -cli-zst: the CLI is missing with no way to get it
	})
	if f.CliWasPresent {
		t.Error("cliWasPresent = true for a missing CLI")
	}
	if !strings.Contains(f.CliError, "missing and no --cli-url or --cli-zst") {
		t.Errorf("CliError = %q, want missing-source error", f.CliError)
	}
}

// A -cli-url whose server is unreachable surfaces as "download failed", not a
// bare transport error.
func TestEnsureCLIDownloadRefused(t *testing.T) {
	dir := t.TempDir()
	err := ensureCLI(installOpts{cliURL: refusedURL(t)}, filepath.Join(dir, "v1"))
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Errorf("ensureCLI refused download = %v, want download failed", err)
	}
}

// A cliPath whose parent chain runs through a regular file fails at the
// MkdirAll step, before any decompression work.
func TestEnsureCLIMkdirDenied(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: zstPath}, filepath.Join(blocker, "sub", "v1")); err == nil {
		t.Error("ensureCLI under a file parent succeeded, want mkdir error")
	}
}

// The final atomic rename fails when something undeletable already sits at
// cliPath (here: a non-empty directory); the temp file must not be left as the
// installed CLI.
func TestEnsureCLIRenameBlocked(t *testing.T) {
	dir := t.TempDir()
	cliPath := filepath.Join(dir, "v1")
	if err := os.MkdirAll(filepath.Join(cliPath, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(zstPath, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLI(installOpts{cliZst: zstPath}, cliPath); err == nil {
		t.Error("ensureCLI onto a non-empty directory succeeded, want rename error")
	}
}

// httpGet propagates a dial failure from client.Get.
func TestHTTPGetConnectionError(t *testing.T) {
	if _, err := httpGet(refusedURL(t)); err == nil {
		t.Error("httpGet to a refused port succeeded, want error")
	}
}

// httpGet propagates a body read that dies mid-stream: the server advertises
// more bytes than it sends, so ReadAll hits an unexpected EOF.
func TestHTTPGetTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(1024))
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	if _, err := httpGet(srv.URL); err == nil {
		t.Error("httpGet with truncated body succeeded, want error")
	}
}

// pruneCLI is best-effort: an unlistable cliDir is a silent no-op, and
// subdirectories inside cliDir are never candidates for pruning.
func TestPruneCLIEdges(t *testing.T) {
	pruneCLI(filepath.Join(t.TempDir(), "absent"), 1) // must not panic

	dir := t.TempDir()
	sub := filepath.Join(dir, "not-a-cli")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pruning is newest-mtime-first; pin distinct mtimes so the order can't
	// collapse on a coarse-resolution filesystem.
	now := time.Now()
	for i, name := range []string{"1.0.0", "1.0.1"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("cli"), 0o755); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-1) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	pruneCLI(dir, 1)
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("pruneCLI removed the subdirectory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.1")); err != nil {
		t.Errorf("pruneCLI removed the newest version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("pruneCLI kept the older version (err=%v), want pruned", err)
	}
}
