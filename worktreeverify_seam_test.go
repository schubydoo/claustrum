package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// checkpointCreatedWorktree resolves the leaf's symlinks only AFTER os.Stat of the
// same path succeeded, and on Unix that Stat already traversed the same chain — so a
// fixture that makes the resolve fail also makes the Stat fail, and the run returns
// from the earlier arm instead (on Windows only a deny-List ACL on the parent would
// split the two, which this suite does not stage). The failure is injected through
// the evalSymlinks seam so the later arm is observed rather than assumed.
//
// checkpointCreatedWorktree itself only stats and resolves; it deletes nothing, so
// this test cannot reach a recursive delete. The leaf is a t.TempDir() the test
// never hands to a caller that would remove it.
func TestCheckpointCreatedWorktreeYieldsEmptyCheckpointWhenResolveFails(t *testing.T) {
	sentinel := errors.New("evalSymlinks refused")
	leaf := t.TempDir()

	var gotPath string
	old := evalSymlinks
	evalSymlinks = func(path string) (string, error) {
		gotPath = path
		// A non-empty first return alongside the error: if the arm ever stopped
		// checking err, the checkpoint would carry THIS string and a non-nil info,
		// and both assertions below would fail.
		return "/decoy/resolved/path", sentinel
	}
	t.Cleanup(func() { evalSymlinks = old })

	cp := checkpointCreatedWorktree(leaf)

	if gotPath != leaf {
		t.Fatalf("seam called with %q, want the leaf %q", gotPath, leaf)
	}
	// The documented outcome of the arm is the EMPTY checkpoint — the zero value
	// verifyCreatedWorktree reads as "nothing to compare against" and passes.
	if cp.info != nil {
		t.Errorf("checkpoint info = %v, want nil (empty checkpoint)", cp.info)
	}
	if cp.resolved != "" {
		t.Errorf("checkpoint resolved = %q, want %q (empty checkpoint)", cp.resolved, "")
	}
	// And the empty checkpoint must still make the verifier pass, since a capture
	// failure is never a new way to fail an honest create.
	if msg := verifyCreatedWorktree(leaf, cp); msg != "" {
		t.Errorf("verifyCreatedWorktree on an empty checkpoint = %q, want %q", msg, "")
	}
}

// readAdminRecord compares nothing for a worktree path that is relative or that does
// not resolve. The record itself is readable in both cases, so the answer comes from
// the path arm and not from the read. The second case goes through the evalSymlinks
// seam, for the reason that the test above gives.
func TestReadAdminRecordComparesNoPathItCannotResolve(t *testing.T) {
	admin := t.TempDir()
	leaf := t.TempDir()
	record := filepath.Join(leaf, ".git") + "\n"
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readAdminRecord(admin, "relative/leaf"); got != recordNotCompared {
		t.Errorf("readAdminRecord(relative path) = %d, want recordNotCompared", got)
	}

	old := evalSymlinks
	evalSymlinks = func(string) (string, error) {
		// The leaf comes back beside the error. If the arm stopped testing err, the
		// compare would run and answer recordNamesLeaf.
		return leaf, errors.New("evalSymlinks refused")
	}
	t.Cleanup(func() { evalSymlinks = old })
	if got := readAdminRecord(admin, leaf); got != recordNotCompared {
		t.Errorf("readAdminRecord(path that does not resolve) = %d, want recordNotCompared", got)
	}
}
