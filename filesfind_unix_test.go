//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findFixture makes the trees of the files.find rows of 4c111e86: a hit below
// a .claude folder, a file root, a folder with .git, a link to a file outside,
// and a chain of 14 folders.
func findFixture(t *testing.T) (root, outside string) {
	t.Helper()
	root = resolveTestRoot(t, t.TempDir())
	outside = resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(outside, "t.txt"), "x", 0o644)
	for _, f := range []string{"r1/a/.claude/settings.json", "r1/top.txt", "git/.git/t.txt", "git/ok/t.txt", "link/b/t.txt"} {
		writeFile(t, filepath.Join(root, f), "x", 0o644)
	}
	if err := os.Symlink(filepath.Join(outside, "t.txt"), filepath.Join(root, "link", "t.txt")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "chain")
	for k := 1; k <= 14; k++ {
		p = filepath.Join(p, fmt.Sprintf("c%d", k))
		writeFile(t, filepath.Join(p, fmt.Sprintf("t%d.txt", k)), "x", 0o644)
	}
	return root, outside
}

// findTreeList lists every name under root, so a test sees that a walk changed
// nothing.
func findTreeList(t *testing.T, root string) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(names, "\n")
}

// TestSocketFilesFind pins the frames of files.find, and shows that the calls
// leave the trees as they were. The inputs come from the rows of 4c111e86 (Linux
// and macOS). Some frames join the inputs of more than one row.
func TestSocketFilesFind(t *testing.T) {
	root, outside := findFixture(t)
	before := findTreeList(t, root) + findTreeList(t, outside)
	r1 := filepath.Join(root, "r1")
	params := []map[string]any{
		{"roots": []string{r1}, "suffix": "settings.json"},
		{"roots": []string{r1}, "suffix": ".claude/settings.json", "bogus": 1},
		{"roots": []string{}, "suffix": "settings.json"},
		{"suffix": "settings.json"},
		{"roots": []string{"ft/r1"}, "suffix": "settings.json"},
		{"roots": []string{r1 + "/"}, "suffix": "settings.json"},
		{"roots": []string{r1}, "suffix": "../settings.json"},
		{"roots": []string{r1}, "suffix": "a/b/c/d/e/f/g/h/i"},
		{"roots": []string{r1}, "suffix": "a/b/c/d/e/f/g/h"},
		{"roots": []string{r1}, "suffix": "settings.json", "limit": -1},
		{"roots": []string{r1}, "suffix": "settings.json", "limit": 1.5},
		{"roots": []string{r1}, "suffix": "set\u202etings.json"},
		{"roots": []string{filepath.Join(root, "nonesuch")}, "suffix": "settings.json"},
		{"roots": []string{filepath.Join(r1, "top.txt")}, "suffix": "settings.json"},
		{"roots": []string{filepath.Join(root, "git")}, "suffix": "t.txt"},
		{"roots": []string{filepath.Join(root, "link")}, "suffix": "t.txt"},
		{"roots": []string{filepath.Join(root, "chain")}, "suffix": "t12.txt"},
		{"roots": []string{filepath.Join(root, "chain")}, "suffix": "t13.txt"},
	}
	cl := dial(t, startSocketServer(t))
	var got []json.RawMessage
	for i, pm := range params {
		got = append(got, normPath(cl.call(req(i+1, "files.find", pm)), root))
	}
	n := len(params)
	for i, raw := range []string{
		`"params":[1],`,
		`"params":null,`,
		``,
		`"params":{"roots":["x"],"roots":["` + r1 + `"],"suffix":"top.txt"},`,
	} {
		line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"files.find",%s"auth":%q}`, n+i+1, raw, testToken)
		got = append(got, normPath(cl.call(line), root))
	}
	assertGolden(t, "socket_files_find.golden.json", encodeGolden(t, got))
	if after := findTreeList(t, root) + findTreeList(t, outside); after != before {
		t.Errorf("the walk changed the trees:\n%s\nwant\n%s", after, before)
	}
}
