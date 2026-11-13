package files

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestFindLinuxWalksHome: on Linux a root equal to the HOME of the daemon, and
// its parent, are walked with no cut "home" (4c111e86, measured on Linux).
func TestFindLinuxWalksHome(t *testing.T) {
	parent := resolveTestRoot(t, t.TempDir())
	home := filepath.Join(parent, "home")
	findTree(t, home, "t.txt", "sub/t.txt")
	t.Setenv("HOME", home)
	for _, root := range []string{home, parent} {
		res := findResult(t, map[string]any{"roots": []string{root}, "suffix": "t.txt"})
		if !res.Whole || len(res.Paths) != 2 || res.Paths[0] != home+"/t.txt" {
			t.Errorf("root %s: %+v, want a whole walk with 2 paths", root, res)
		}
	}
}

// TestFindLinuxSlashRoot: the root "/" is walked with no cut "home", and each
// path has one "/" at its start (4c111e86, measured on Linux).
func TestFindLinuxSlashRoot(t *testing.T) {
	res := findResult(t, map[string]any{"roots": []string{"/"}, "suffix": "hostname", "limit": 20, "timeoutMs": 5000})
	if res.Cut == "home" || len(res.Paths) == 0 {
		t.Errorf("root /: %+v, want paths and no cut home", res)
	}
	for _, p := range res.Paths {
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
			t.Errorf("root /: path %q", p)
		}
	}
}
