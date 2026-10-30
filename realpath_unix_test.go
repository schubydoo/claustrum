//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestSocketFilesRealpathErrnoFrames pins the -32603 frames of a link loop, a
// chain over the link limit and a folder with mode 000 (build 683cc6a9, Linux
// and macOS VMs). The errno comes from the platform.
func TestSocketFilesRealpathErrnoFrames(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	if err := os.Symlink("b", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	// A chain of 41 links is over the limit on Linux (40) and on macOS (32).
	prev := "end"
	if err := os.WriteFile(filepath.Join(root, prev), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 41; i++ {
		name := "c" + strconv.Itoa(i)
		if err := os.Symlink(prev, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		prev = name
	}
	paths := []map[string]any{
		{"path": filepath.Join(root, "loop")},
		{"path": filepath.Join(root, "loop", "x")},
		{"path": filepath.Join(root, prev)},
	}
	frame := func(no syscall.Errno, reason string) string {
		return fmt.Sprintf(`"error":{"code":-32603,"message":"files.realpath: error %d: %s","data":{"reason":%q,"errno":%d}}`,
			int(no), no.Error(), reason, int(no))
	}
	for i, g := range realpathCalls(t, root, paths) {
		if want := frame(syscall.ELOOP, "too_many_links"); !strings.Contains(string(g), want) {
			t.Errorf("reply %d = %s, want %s", i+1, g, want)
		}
	}
	if os.Geteuid() == 0 {
		return
	}
	np := filepath.Join(root, "np")
	if err := os.Mkdir(np, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(np, 0o700) })
	g := realpathCalls(t, root, []map[string]any{{"path": filepath.Join(np, "x")}})[0]
	if want := frame(syscall.EACCES, "no_permission"); !strings.Contains(string(g), want) {
		t.Errorf("reply = %s, want %s", g, want)
	}
}

// TestSocketFilesRealpathDanglingRealFolder pins wouldBe of a dangling link whose
// relative target has a ".." (claustrum's own, not measured). The kernel reads
// the target from the real folder of the link, and applies a ".." after a link
// to the link target.
func TestSocketFilesRealpathDanglingRealFolder(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	for _, d := range []string{"a", "real/dir", "other/deep"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"a/L":          filepath.Join(root, "real", "dir"),
		"real/dir/y":   "../gone",
		"real/dir/sub": filepath.Join(root, "other", "deep"),
		"real/dir/z":   "sub/../gone",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(root, "a", "L", "y"), "missingLeaf": true},
		{"path": filepath.Join(root, "real", "dir", "z"), "missingLeaf": true},
	})
	for i, want := range []string{"real/gone", "other/gone"} {
		w := `"wouldBe":"<DIR>/` + want + `","dangling":true`
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}
