//go:build unix

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// These tests cover a worktreeRoot that is the file system root. The cells are R1n,
// R1u and R1r of a Linux VM, and on a macOS VM R1a (both methods) and R1b (the
// remove), each against 89cb6289.
// Every request names a leaf under "/" that does not exist. No test writes under "/",
// with or without the refusal: before, claustrum refused the create with another text
// and answered success to the remove of a missing folder. worktreeRoot is unix-only.

const fsRootTail = ` is a filesystem root; choose the worktree location by its absolute path, without "..", beneath the filesystem root`

// fsRootLeaf is a path under "/" that does not exist.
func fsRootLeaf(t *testing.T) string {
	t.Helper()
	leaf := fmt.Sprintf("/claustrum-test-no-such-leaf-%d", os.Getpid())
	if _, err := os.Lstat(leaf); err == nil {
		t.Skipf("%s exists", leaf)
	}
	return leaf
}

// The text of both verbs for a root of "/". Rows R2 and R3 are the cells of "//" and
// "/." on Linux and macOS VMs: the same refusal, with the root as sent.
func TestWorktreeRootIsFileSystemRootText(t *testing.T) {
	leaf := fsRootLeaf(t)
	for _, c := range []struct{ name, root string }{
		{"R1", "/"},
		{"R2_double_slash", "//"},
		{"R3_slash_dot", "/."},
	} {
		for _, verb := range []string{"create", "remove"} {
			t.Run(c.name+"_"+verb, func(t *testing.T) {
				want := "refusing to " + verb + " worktree: " + c.root + fsRootTail
				if got := worktreeExternalSpellingRefusal(c.root, leaf, verb); got != want {
					t.Errorf("worktreeExternalSpellingRefusal(%q) =\n  %q\nwant\n  %q", c.root, got, want)
				}
			})
		}
	}
	// A root one level below "/" passes this test.
	if got := worktreeExternalSpellingRefusal("/tmp", "/tmp/cp/w1", "create"); got != "" {
		t.Errorf("worktreeExternalSpellingRefusal(/tmp) = %q, want none", got)
	}
}

// The create frame of cells R1n, R1u, R1r and R1a, byte for byte. It comes after the repo
// test, which is 3 git calls with the excludes read, and nothing is created.
func TestWorktreeCreateRootIsFileSystemRoot(t *testing.T) {
	leaf := fsRootLeaf(t)
	f := newAncFixture(t)
	before := r0cState(t, &r0Fixture{T: f.T})
	calls := logGitArgv(t)
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": f.T, "branchName": "w1", "worktreePath": leaf, "worktreeRoot": "/"}))
	wantFrame(t, got, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"refusing to create worktree: / is a filesystem root; choose the worktree location by its absolute path, without \"..\", beneath the filesystem root","errorCode":"unsafe_path"}}`)
	if got, want := r0cCalls(calls()), r0cCreateCalls[:2]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("git calls = %q, want %q", got, want)
	}
	if _, err := os.Lstat(leaf); err == nil {
		t.Errorf("%s exists, and a refusal creates nothing", leaf)
	}
	if after := r0cState(t, &r0Fixture{T: f.T}); after != before {
		t.Errorf("entries and branches = %s, want %s", after, before)
	}
}

// The remove frame of cells R1n, R1r, R1a and R1b, byte for byte. No git call runs.
// Before, claustrum answered {"success":true} here after 12 git calls.
func TestWorktreeRemoveRootIsFileSystemRoot(t *testing.T) {
	leaf := fsRootLeaf(t)
	f := newAncFixture(t)
	before := r0cState(t, &r0Fixture{T: f.T})
	calls := logGitArgv(t)
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": f.T, "branchName": "w1", "worktreePath": leaf, "worktreeRoot": "/"}))
	wantFrame(t, got, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"refusing to remove worktree: / is a filesystem root; choose the worktree location by its absolute path, without \"..\", beneath the filesystem root"}}`)
	if got := strings.Join(r0cCalls(calls()), "\n"); got != "" {
		t.Errorf("git calls = %q, want none", got)
	}
	if after := r0cState(t, &r0Fixture{T: f.T}); after != before {
		t.Errorf("entries and branches = %s, want %s", after, before)
	}
}
