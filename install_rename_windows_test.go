//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Windows side of the rename of the new CLI (cells W1, W2 and W6 of
// 5fd08069, Windows VM). Each test holds the target itself with holdOpen, open
// with no delete sharing. So a rename onto it is denied, as for a running old
// CLI. No test of this file ran on the host of the build.

// setCLIRenameBound sets cliRenameBound until the test ends.
func setCLIRenameBound(t *testing.T, d time.Duration) {
	t.Helper()
	old := cliRenameBound
	cliRenameBound = d
	t.Cleanup(func() { cliRenameBound = old })
}

// renameFixture makes a target with body old and a staging file with body
// staged in one folder.
func renameFixture(t *testing.T, old, staged string) (tmp, cli string) {
	t.Helper()
	dir := t.TempDir()
	tmp, cli = filepath.Join(dir, ".fetch-1"), filepath.Join(dir, "1.2.3.exe")
	if err := os.WriteFile(cli, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	return tmp, cli
}

// A denied rename is tried again inside the bound. The holder lets go after
// 50 ms, and the new CLI is in place.
func TestPlaceStagedCLIRetriesADeniedRename(t *testing.T) {
	setCLIRenameBound(t, 2*time.Second)
	tmp, cli := renameFixture(t, "old", "new")
	release := holdOpen(t, cli, false)
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	if err := placeStagedCLI(tmp, cli); err != nil {
		t.Fatalf("placeStagedCLI = %v, want the retry to place the CLI", err)
	}
	if b, _ := os.ReadFile(cli); string(b) != "new" {
		t.Errorf("the CLI file holds %q, want the new bytes", b)
	}
}

// A rename that stays denied fails after the bound, with the text of cells W1
// and W6: "rename: rename <tmp> <cli>: Access is denied.". The staging file
// goes, and the old file stays.
func TestPlaceStagedCLIFailsAfterTheBound(t *testing.T) {
	tmp, cli := renameFixture(t, "old", "new")
	holdOpen(t, cli, false)
	start := time.Now()
	err := placeStagedCLI(tmp, cli)
	if d := time.Since(start); d < cliRenameBound*3/4 {
		t.Errorf("the rename failed after %v, want about the bound %v", d, cliRenameBound)
	}
	want := "rename: rename " + tmp + " " + cli + ": Access is denied."
	if err == nil || err.Error() != want {
		t.Errorf("placeStagedCLI = %v, want %q", err, want)
	}
	if _, statErr := os.Lstat(tmp); !os.IsNotExist(statErr) {
		t.Errorf("the staging file is still there: %v", statErr)
	}
	if b, _ := os.ReadFile(cli); string(b) != "old" {
		t.Errorf("the CLI file holds %q, want the old bytes", b)
	}
}

// A denied rename onto a file with the same bytes counts as done, and the
// staging file goes (cells W2).
func TestPlaceStagedCLICountsSameBytesAsDone(t *testing.T) {
	setCLIRenameBound(t, 20*time.Millisecond)
	tmp, cli := renameFixture(t, "same", "same")
	holdOpen(t, cli, false)
	if err := placeStagedCLI(tmp, cli); err != nil {
		t.Fatalf("placeStagedCLI = %v, want success for the same bytes", err)
	}
	if _, statErr := os.Lstat(tmp); !os.IsNotExist(statErr) {
		t.Errorf("the staging file is still there: %v", statErr)
	}
}

// A denied rename of an -install keeps the -cli-zst blob (cells W1 and W6).
func TestInstallKeepsTheBlobWhenTheRenameIsDenied(t *testing.T) {
	setCLIRenameBound(t, 20*time.Millisecond)
	f := newStubFixture(t)
	if err := os.WriteFile(f.cli, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, f.cli, false)
	blob := f.stubBlob(t)
	err := ensureCLI(installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob}, f.cli)
	if err == nil || !strings.HasPrefix(err.Error(), "rename: rename ") || !strings.HasSuffix(err.Error(), ": Access is denied.") {
		t.Errorf("ensureCLI = %v, want the denied rename", err)
	}
	if !isRegularFile(blob) {
		t.Error("the blob was consumed by an install that failed at the rename")
	}
}
