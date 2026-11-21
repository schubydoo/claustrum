//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSocketFilesRealpathWindowsCleansDotDot pins the Windows rule (claustrum's
// own, not measured): Win32 removes a ".." by text before it opens a path, so a
// ".." after a link removes the link name and not a folder of its target. The
// rule holds for the request path and for the relative target of a dangling link.
func TestSocketFilesRealpathWindowsCleansDotDot(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	for _, d := range []string{"a", filepath.Join("real", "dir")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "real", "dir"), filepath.Join(root, "a", "L")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if err := os.Symlink(`L\..\gone`, filepath.Join(root, "a", "d")); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	got := realpathCalls(t, root, []map[string]any{
		{"path": root + `\a\L\..\new`, "missingLeaf": true},
		{"path": filepath.Join(root, "a", "d"), "missingLeaf": true},
	})
	for i, w := range []string{`"wouldBe":"<DIR>/a/new"`, `"wouldBe":"<DIR>/a/gone","dangling":true`} {
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}

// TestSocketFilesRealpathWindowsJunction pins the Windows rows on 4c111e86 and
// c9125eb8: files.realpath follows a junction, both for a name below it and for
// the junction itself.
func TestSocketFilesRealpathWindowsJunction(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	makeJunction(t, filepath.Join(root, "junc"), filepath.Join(root, "dir"))
	writeFile(t, filepath.Join(root, "dir", "f.txt"), "hi\n", 0o644)
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "junc", "f.txt")},
		{"path": filepath.Join(root, "junc")},
	})
	for i, w := range []string{
		`"result":{"exists":true,"real":"<DIR>/dir/f.txt"}`,
		`"result":{"exists":true,"real":"<DIR>/dir"}`,
	} {
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}

// TestSocketFilesRealpathWindowsStream pins the -32602 frame of a path with a
// colon after the drive letter, with and without missingLeaf, for a stream name
// and for a stream name with its type (4c111e86, Windows).
func TestSocketFilesRealpathWindowsStream(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "f.txt"), "x", 0o644)
	want := `"error":{"code":-32602,"message":"files.realpath: the path has a colon after the drive letter's (alternate data streams are not supported)"}`
	var params []map[string]any
	for _, p := range []string{filepath.Join(root, "f.txt") + ":stream", filepath.Join(root, "f.txt") + ":nope:$DATA"} {
		params = append(params, map[string]any{"path": p}, map[string]any{"path": p, "missingLeaf": true})
	}
	for i, got := range realpathCalls(t, root, params) {
		if !strings.Contains(string(got), want) {
			t.Errorf("reply %d = %s, want %s", i+1, got, want)
		}
	}
}

// junctionChain makes dir\j1 to dir\j<n>, each a junction to the next, and
// dir\j<n> a junction to real: a path below dir\j1 follows n links.
func junctionChain(t *testing.T, dir, real string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := real
	for k := n; k >= 1; k-- {
		link := filepath.Join(dir, "j"+strconv.Itoa(k))
		makeJunction(t, link, prev)
		prev = link
	}
}

// TestSocketFilesRealpathWindowsJunctionChain pins the link limit: a chain of
// 31 junctions resolves, and a chain of 32 gives error 1921 for a file below
// it, for the chain itself and for a missing name below it, with and without
// missingLeaf (4c111e86, Windows).
func TestSocketFilesRealpathWindowsJunctionChain(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "real", "inner", "x.txt"), "x", 0o644)
	for _, n := range []int{31, 32} {
		junctionChain(t, filepath.Join(root, "ch"+strconv.Itoa(n)), filepath.Join(root, "real"), n)
	}
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "ch31", "j1", "inner", "x.txt")},
		{"path": filepath.Join(root, "ch32", "j1", "inner", "x.txt")},
		{"path": filepath.Join(root, "ch32", "j1")},
		{"path": filepath.Join(root, "ch32", "j1", "inner", "nope.txt")},
		{"path": filepath.Join(root, "ch32", "j1", "inner", "nope.txt"), "missingLeaf": true},
	})
	tooMany := `"error":{"code":-32603,"message":"files.realpath: error 1921: The name of the file cannot be resolved by the system.","data":{"reason":"too_many_links","errno":1921,"stoppedShort":true}}`
	for i, w := range []string{`"result":{"exists":true,"real":"<DIR>/real/inner/x.txt"}`, tooMany, tooMany, tooMany, tooMany} {
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}
