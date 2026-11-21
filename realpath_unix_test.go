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
// and macOS). The errno comes from the platform.
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
		"dl":           "gone",
		"real/y":       "gone",
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
		// A ".." after a link in the request: the kernel finds the link real/y.
		{"path": root + "/a/L/../y", "missingLeaf": true},
		{"path": root + "/a/L/../new", "missingLeaf": true},
		// A trailing separator does not make the kernel follow the final link.
		{"path": root + "/dl/", "missingLeaf": true},
		{"path": root + "/dl//", "missingLeaf": true},
	})
	for i, want := range []string{"real/gone", "other/gone", "real/gone", "real/new", "gone", "gone"} {
		w := `"wouldBe":"<DIR>/` + want + `"`
		if i != 3 {
			w += `,"dangling":true`
		}
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}

// TestSocketFilesRealpathDanglingTargets covers a dangling link with an absolute
// target whose parent is the root, an absolute target whose parent is missing,
// a target behind a folder with mode 000, and a clean "~/" path (claustrum's own,
// not measured).
func TestSocketFilesRealpathDanglingTargets(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	for name, target := range map[string]string{
		"top":    "/claustrum-realpath-gone",
		"nopar":  filepath.Join(root, "missing", "x"),
		"behind": filepath.Join(root, "np", "sub", "x"),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", root)
	calls := []map[string]any{
		{"path": filepath.Join(root, "top"), "missingLeaf": true},
		{"path": filepath.Join(root, "nopar"), "missingLeaf": true},
		{"path": "~/absent"},
	}
	want := []string{
		`"wouldBe":"/claustrum-realpath-gone","dangling":true`,
		`"result":{"exists":false,"dangling":true}`,
		`"result":{"exists":false}`,
	}
	if os.Geteuid() != 0 {
		np := filepath.Join(root, "np")
		if err := os.Mkdir(np, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(np, 0o700) })
		calls = append(calls, map[string]any{"path": filepath.Join(root, "behind"), "missingLeaf": true})
		want = append(want, `"reason":"no_permission","errno":`+strconv.Itoa(int(syscall.EACCES)))
	}
	for i, g := range realpathCalls(t, root, calls) {
		if !strings.Contains(string(g), want[i]) {
			t.Errorf("reply %d = %s, want %s", i+1, g, want[i])
		}
	}
}

// TestSocketFilesRealpathThroughAFile pins the plain exists:false of a path
// through a regular file, with and without missingLeaf, and the dangling reply
// with no wouldBe of a link to such a path (4c111e86, measured on Linux, and for
// <file>/a with no missingLeaf on macOS). The system answers ENOTDIR there.
// Mutations: resolveRealpath that does not count ENOTDIR as a missing name, or
// wouldBeUnder with no folder test of the parent.
func TestSocketFilesRealpathThroughAFile(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	f := filepath.Join(root, "t.txt")
	writeFile(t, f, "x", 0o644)
	if err := os.Symlink(filepath.Join(f, "x"), filepath.Join(root, "lnk")); err != nil {
		t.Fatal(err)
	}
	plain := `"result":{"exists":false}}`
	cases := []struct {
		path string
		want string
	}{
		{filepath.Join(f, "a"), plain},
		{filepath.Join(f, "a", "b"), plain},
		{f + "/.", plain},
		{f + "/..", plain},
		{filepath.Join(root, "lnk"), plain},
		{filepath.Join(root, "lnk", "a"), plain},
	}
	var params []map[string]any
	for _, c := range cases {
		params = append(params, map[string]any{"path": c.path}, map[string]any{"path": c.path, "missingLeaf": true})
	}
	got := realpathCalls(t, root, params)
	for i, c := range cases {
		wantML := c.want
		if c.path == filepath.Join(root, "lnk") {
			wantML = `"result":{"exists":false,"dangling":true}}`
		}
		if !strings.HasSuffix(string(got[2*i]), c.want) {
			t.Errorf("files.realpath %s = %s, want …%s", c.path, got[2*i], c.want)
		}
		if !strings.HasSuffix(string(got[2*i+1]), wantML) {
			t.Errorf("files.realpath %s with missingLeaf = %s, want …%s", c.path, got[2*i+1], wantML)
		}
	}
}

// TestSocketFilesRealpathTrailingSlash pins a trailing "/" by the kind of the
// last name (4c111e86, measured on Linux; macOS is claustrum's own, not
// measured). After a file, a link to a file or a FIFO it gives the plain
// exists:false, with and without missingLeaf. A folder, a link to a folder, a
// dangling link and a missing name answer as without it. Mutation: resolveRealpath
// with no trailing-slash test (the old code). <file>/ then gives exists:true.
func TestSocketFilesRealpathTrailingSlash(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	f, d := filepath.Join(root, "t.txt"), filepath.Join(root, "d")
	writeFile(t, f, "x", 0o644)
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d, "t.txt"), "y", 0o644)
	for name, target := range map[string]string{"lf": f, "ld": d, "dang": filepath.Join(root, "nothere")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := `"result":{"exists":false}}`
	realOf := func(p string) string { return `"result":{"exists":true,"real":"` + p + `"}}` }
	// realpathCalls writes the root as <DIR>.
	cases := []struct{ path, want, wantML string }{
		{f + "/", plain, plain},
		{f + "//", plain, plain},
		{root + "/lf/", plain, plain},
		{root + "/fifo/", plain, plain},
		{d + "/t.txt/", plain, plain},
		{d + "/", realOf("<DIR>/d"), realOf("<DIR>/d")},
		{root + "/ld/", realOf("<DIR>/d"), realOf("<DIR>/d")},
		{root + "/dang/", plain, `"result":{"exists":false,"wouldBe":"<DIR>/nothere","dangling":true}}`},
		{root + "/missing/", plain, `"result":{"exists":false,"wouldBe":"<DIR>/missing"}}`},
		{f, realOf("<DIR>/t.txt"), realOf("<DIR>/t.txt")},
	}
	var params []map[string]any
	for _, c := range cases {
		params = append(params, map[string]any{"path": c.path}, map[string]any{"path": c.path, "missingLeaf": true})
	}
	got := realpathCalls(t, root, params)
	for i, c := range cases {
		if !strings.HasSuffix(string(got[2*i]), c.want) {
			t.Errorf("files.realpath %s = %s, want …%s", c.path, got[2*i], c.want)
		}
		if !strings.HasSuffix(string(got[2*i+1]), c.wantML) {
			t.Errorf("files.realpath %s with missingLeaf = %s, want …%s", c.path, got[2*i+1], c.wantML)
		}
	}
}
