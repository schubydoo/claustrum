//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// listIsDirOf lists dir and returns the isDir value of each entry by name.
func listIsDirOf(t *testing.T, dir string, extra map[string]any) map[string]bool {
	t.Helper()
	params := map[string]any{"path": dir}
	for k, v := range extra {
		params[k] = v
	}
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "files.list", params))
	out := map[string]bool{}
	for _, part := range strings.Split(got, `{"name":"`)[1:] {
		name := part[:strings.Index(part, `"`)]
		out[name] = strings.Contains(part, `"isDir":true`)
	}
	return out
}

// A junction is a folder only with junctionsAsDirs. A directory link to a
// folder is a folder either way. A file link to a folder is not (build
// 683cc6a9, Windows).
func TestFilesListWindowsLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	list := filepath.Join(dir, "l")
	if err := os.Mkdir(list, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(list, "junc"), target)
	makeDirSymlink(t, filepath.Join(list, "dlink"), target)
	if out, err := exec.Command("cmd", "/c", "mklink", filepath.Join(list, "flink"), target).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("mklink %s: %v\n%s", target, err, out)
		}
		t.Logf("file link not made: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		junc  bool
	}{
		{"absent", nil, false},
		{"false", map[string]any{"junctionsAsDirs": false}, false},
		{"true", map[string]any{"junctionsAsDirs": true}, true},
	} {
		got := listIsDirOf(t, list, tc.extra)
		if _, ok := got["flink"]; !ok && os.Getenv("CI") != "" {
			t.Fatalf("%s: flink is not in the listing: %v", tc.name, got)
		}
		if got["junc"] != tc.junc || !got["dlink"] || got["flink"] {
			t.Errorf("%s: got %v, want junc %t, dlink true, flink false", tc.name, got, tc.junc)
		}
	}
}

// A chain of k directory links is true for k up to 31 and false for 32. A
// dangling link is false.
func TestFilesListWindowsLinkChain(t *testing.T) {
	for _, tc := range []struct {
		k    int
		want bool
	}{{1, true}, {31, true}, {32, false}} {
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		below := filepath.Join(dir, "below")
		list := filepath.Join(dir, "l")
		for _, d := range []string{real, below, list} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		prev := real
		for i := 1; i < tc.k; i++ {
			link := filepath.Join(below, fmt.Sprintf("c%02d", i))
			makeDirSymlink(t, link, prev)
			prev = link
		}
		makeDirSymlink(t, filepath.Join(list, "head"), prev)
		makeDirSymlink(t, filepath.Join(list, "dangling"), filepath.Join(dir, "missing"))
		got := listIsDirOf(t, list, map[string]any{"junctionsAsDirs": true})
		if got["head"] != tc.want || got["dangling"] {
			t.Errorf("k=%d: got %v, want head %t, dangling false", tc.k, got, tc.want)
		}
	}
}

// A link to a network share is false (the share \\localhost\c$ was measured).
func TestFilesListWindowsShareLink(t *testing.T) {
	list := t.TempDir()
	makeDirSymlink(t, filepath.Join(list, "share"), `\\localhost\c$`)
	if got := listIsDirOf(t, list, nil); got["share"] {
		t.Errorf("share link isDir = true, want false")
	}
}
