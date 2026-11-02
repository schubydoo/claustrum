package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// detectLibc's fallback branch: when `ldd` can't be executed, it probes for the
// musl loader and otherwise reports glibc. Emptying PATH forces that branch.
func TestDetectLibcLddMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		// There is no probe to fall back FROM off linux: detectLibc returns ""
		// without consulting ldd. TestDetectLibc covers that.
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

// zstdDecompress: os.Create fails when the destination parent directory doesn't exist.
func TestZstdDecompressDestError(t *testing.T) {
	err := zstdDecompressBytes(t, zstdOf(t, []byte("payload")), filepath.Join(t.TempDir(), "nonexistent", "out"))
	if err == nil {
		t.Fatal("expected error for missing parent dir, got nil")
	}
}
