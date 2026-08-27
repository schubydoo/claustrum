package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// isSocketDead classifies a dial error: a missing socket file or a refused
// connection means nothing is listening (stale socket), anything else is treated as
// "possibly live" so the launcher keeps waiting for a genuine handoff.
func TestIsSocketDead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"not exist", os.ErrNotExist, true},
		{"refused", &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, true},
		{"timeout", context.DeadlineExceeded, false},
		{"opaque", errors.New("something else"), false},
	}
	for _, c := range cases {
		if got := isSocketDead(c.err); got != c.want {
			t.Errorf("%s: isSocketDead(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// The process-control params carry no filesystem paths; their expandPaths is an
// explicit no-op. Pin that nothing — not even a "~"-shaped id — is rewritten.
func TestExpandPathsProcessControlParamsAreNoOps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &stdinParams{ID: "~id", Data: "~/data"}
	st.expandPaths()
	if st.ID != "~id" || st.Data != "~/data" {
		t.Errorf("stdinParams mutated: %+v", st)
	}
	k := &killParams{ID: "~id", Signal: "~/TERM"}
	k.expandPaths()
	if k.ID != "~id" || k.Signal != "~/TERM" {
		t.Errorf("killParams mutated: %+v", k)
	}
	kw := &killAndWaitParams{ID: "~id", Signal: "~/TERM"}
	kw.expandPaths()
	if kw.ID != "~id" || kw.Signal != "~/TERM" {
		t.Errorf("killAndWaitParams mutated: %+v", kw)
	}
	r := &reattachParams{ID: "~id", FromSeq: 7}
	r.expandPaths()
	if r.ID != "~id" || r.FromSeq != 7 {
		t.Errorf("reattachParams mutated: %+v", r)
	}
}

// filepath.Rel cannot relate a relative path to an absolute root; that error must
// read as "not inside" rather than panic or accept.
func TestPathStrictlyUnderRelError(t *testing.T) {
	root := t.TempDir()
	if pathStrictlyUnder(filepath.Join("rel", "x"), root) {
		t.Error("a relative path must not be reported as strictly under an absolute root")
	}
	if pathStrictlyUnder(root, root) {
		t.Error("the root itself must not be strictly under itself")
	}
}

// sha256File surfaces the open error for a missing blob instead of hashing nothing.
func TestSha256FileMissing(t *testing.T) {
	if _, err := sha256File(filepath.Join(t.TempDir(), "absent.zst")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// A bare leaf with no directory component has nothing to resolve; it comes back
// unchanged instead of being joined onto ".".
func TestResolveAsFarAsExistsBareLeaf(t *testing.T) {
	const leaf = "claustrum-no-such-leaf"
	if got := resolveAsFarAsExists(leaf); got != leaf {
		t.Errorf("resolveAsFarAsExists(%q) = %q, want it unchanged", leaf, got)
	}
}

// removePipeNameFileIfOwned with an identity to protect but no file on disk is a
// silent no-op (the successor, or a crash, already took it).
func TestRemovePipeNameFileIfOwnedAbsent(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	removePipeNameFileIfOwned(filepath.Join(dir, "rpc.sock"), owned)
	if _, err := os.Stat(other); err != nil {
		t.Errorf("an unrelated file was touched: %v", err)
	}
}

// The stale-registration scan skips non-directory entries and directories with no
// gitdir file rather than failing; unrelated registrations survive untouched.
func TestDropStaleWorktreeRegistrationSkipsMalformedEntries(t *testing.T) {
	repo := t.TempDir()
	base := filepath.Join(repo, ".git", "worktrees")
	if err := os.MkdirAll(filepath.Join(base, "nogitdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(base, "junk")
	if err := os.WriteFile(junk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dropStaleWorktreeRegistration(repo, filepath.Join(t.TempDir(), "w"))
	for _, p := range []string{junk, filepath.Join(base, "nogitdir")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
