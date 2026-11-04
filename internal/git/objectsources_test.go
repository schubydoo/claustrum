package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// checksOn turns the object source checks on for one test, on every system.
func checksOn(t *testing.T) {
	t.Helper()
	old := objectSourceChecks
	objectSourceChecks = true
	t.Cleanup(func() { objectSourceChecks = old })
}

// objectsDir makes <dir>/objects/info and returns <dir>/objects.
func objectsDir(t *testing.T, dir string) string {
	t.Helper()
	objects := filepath.Join(dir, "objects")
	if err := os.MkdirAll(filepath.Join(objects, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	return objects
}

func alternatesOf(objects string) string { return filepath.Join(objects, "info", "alternates") }

// The rows of b3081b51 on Windows, as the shapes of the alternates file of the
// repository's own git directory. A valid line passes.
func TestAlternatesRefusal(t *testing.T) {
	checksOn(t)
	cases := []struct {
		name string
		// write makes the file in objects and returns the text that git.status
		// gets, and the one that the other methods get.
		write func(t *testing.T, root, objects string) (all, part string)
	}{
		{"a valid line", func(t *testing.T, root, objects string) (string, string) {
			writeFile(t, alternatesOf(objects), filepath.ToSlash(objectsDir(t, filepath.Join(root, "O")))+"\n", 0o644)
			return "", ""
		}},
		{"Windows line endings", func(t *testing.T, root, objects string) (string, string) {
			writeFile(t, alternatesOf(objects), filepath.ToSlash(objectsDir(t, filepath.Join(root, "O")))+"\r\n", 0o644)
			text := alternatesCRLFText(alternatesOf(objects))
			return text, text
		}},
		{"a quoted line", func(t *testing.T, root, objects string) (string, string) {
			writeFile(t, alternatesOf(objects), `"`+filepath.ToSlash(objectsDir(t, filepath.Join(root, "O")))+"\"\n", 0o644)
			return alternatesQuoteText(alternatesOf(objects)), ""
		}},
		{"a tab", func(t *testing.T, root, objects string) (string, string) {
			writeFile(t, alternatesOf(objects), filepath.ToSlash(objectsDir(t, filepath.Join(root, "O")))+"\tx\n", 0o644)
			return alternatesControlText(alternatesOf(objects)), ""
		}},
		{"1048577 bytes", func(t *testing.T, _, objects string) (string, string) {
			writeFile(t, alternatesOf(objects), strings.Repeat("a", alternatesMaxBytes)+"\n", 0o644)
			return alternatesUnreadableText(alternatesOf(objects)), ""
		}},
		{"a folder", func(t *testing.T, _, objects string) (string, string) {
			if err := os.Mkdir(alternatesOf(objects), 0o755); err != nil {
				t.Fatal(err)
			}
			return alternatesUnreadableText(alternatesOf(objects)), ""
		}},
		{"1000 lines", func(t *testing.T, root, objects string) (string, string) {
			line := filepath.ToSlash(objectsDir(t, filepath.Join(root, "O"))) + "\n"
			writeFile(t, alternatesOf(objects), strings.Repeat(line, 1000), 0o644)
			text := alternatesTooManyText(alternatesOf(objects))
			return text, text
		}},
		{"a chain of 20", func(t *testing.T, root, objects string) (string, string) {
			level := func(n int) string { return objectsDir(t, filepath.Join(root, "ch", strconv.Itoa(n))) }
			writeFile(t, alternatesOf(objects), filepath.ToSlash(level(1))+"\n", 0o644)
			for n := 1; n < 20; n++ {
				writeFile(t, alternatesOf(level(n)), filepath.ToSlash(level(n+1))+"\n", 0o644)
			}
			// The named file is the one of level 6.
			text := alternatesTooManyText(alternatesOf(level(6)))
			return text, text
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(root, "R", ".git")
			all, part := c.write(t, root, objectsDir(t, common))
			if got := alternatesRefusal(common, true); got != all {
				t.Errorf("git.status check:\n got %q\nwant %q", got, all)
			}
			if got := alternatesRefusal(common, false); got != part {
				t.Errorf("check of the other methods:\n got %q\nwant %q", got, part)
			}
		})
	}
}

// A chain of 6 levels below the repository's own file passes, and so do 64
// object directories. One more of either is refused. The two bounds are not
// measured: the rows send 1000 lines and chains of 20 and 22.
func TestAlternatesBounds(t *testing.T) {
	checksOn(t)
	chain := func(t *testing.T, levels int) (common, last string) {
		root := t.TempDir()
		common = filepath.Join(root, "R", ".git")
		prev := objectsDir(t, common)
		for n := 1; n <= levels; n++ {
			next := objectsDir(t, filepath.Join(root, "ch", strconv.Itoa(n)))
			writeFile(t, alternatesOf(prev), filepath.ToSlash(next)+"\n", 0o644)
			last, prev = prev, next
		}
		return common, last
	}
	if common, _ := chain(t, 6); alternatesRefusal(common, true) != "" {
		t.Error("a chain of 6 levels was refused")
	}
	if common, last := chain(t, 7); alternatesRefusal(common, true) != alternatesTooManyText(alternatesOf(last)) {
		t.Errorf("a chain of 7 levels: got %q", alternatesRefusal(common, true))
	}
	lines := func(t *testing.T, n int) string {
		root := t.TempDir()
		common := filepath.Join(root, "R", ".git")
		line := filepath.ToSlash(objectsDir(t, filepath.Join(root, "O"))) + "\n"
		writeFile(t, alternatesOf(objectsDir(t, common)), strings.Repeat(line, n), 0o644)
		return common
	}
	if got := alternatesRefusal(lines(t, alternatesMaxDirs), true); got != "" {
		t.Errorf("64 lines: got %q", got)
	}
	if got := alternatesRefusal(lines(t, alternatesMaxDirs+1), true); got == "" {
		t.Error("65 lines passed")
	}
}

// With the checks off, as on Linux and macOS, no shape is refused (measured on
// Linux and macOS).
func TestAlternatesChecksOff(t *testing.T) {
	old := objectSourceChecks
	objectSourceChecks = false
	t.Cleanup(func() { objectSourceChecks = old })
	root := t.TempDir()
	common := filepath.Join(root, "R", ".git")
	writeFile(t, alternatesOf(objectsDir(t, common)), "x\r\n", 0o644)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", root)
	if got := statusSourceRefusal(common); got != "" {
		t.Errorf("checks off: got %q", got)
	}
	for _, all := range []bool{true, false} {
		if got := alternatesRefusal(common, all); got != "" {
			t.Errorf("checks off, all=%v: got %q", all, got)
		}
	}
}

// GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment refuses
// git.status (measured on Windows). GIT_OBJECT_DIRECTORY logs one line and
// refuses nothing (measured on Windows).
func TestStatusSourceEnvironment(t *testing.T) {
	checksOn(t)
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(root, "O"))
	if got := statusSourceRefusal(common); got != alternateEnvText {
		t.Errorf("GIT_ALTERNATE_OBJECT_DIRECTORIES: got %q", got)
	}
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "")
	if got := statusSourceRefusal(common); got != "" {
		t.Errorf("an empty GIT_ALTERNATE_OBJECT_DIRECTORIES: got %q", got)
	}

	objectDirLogged.Store(false)
	t.Cleanup(func() { objectDirLogged.Store(false) })
	logs := captureLogBuf(t)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(root, "O"))
	for range 2 {
		if got := statusSourceRefusal(common); got != "" {
			t.Errorf("GIT_OBJECT_DIRECTORY: got %q", got)
		}
	}
	if n := strings.Count(logs.String(), objectDirLine); n != 1 {
		t.Errorf("%d lines of GIT_OBJECT_DIRECTORY after two requests, want 1", n)
	}
}

func quotedJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The frames of the four methods with a CRLF alternates file (measured on
// Windows). A create makes nothing and a remove deletes nothing. This test
// turns the Windows checks on, so it runs on every system.
func TestObjectSourceFrames(t *testing.T) {
	checksOn(t)
	f := newStatusFixture(t)
	w0 := filepath.Join(f.T, ".claude", "worktrees", "w0")
	runGit(t, f.T, "worktree", "add", "-q", "-b", "w0", w0)
	alt := alternatesOf(filepath.Join(f.T, ".git", "objects"))
	writeFile(t, alt, filepath.ToSlash(filepath.Join(f.root, "O", "objects"))+"\r\n", 0o644)
	text := alternatesCRLFText(alt)
	h := newTestGitDeps()
	call := func(method string, params map[string]any) string {
		t.Helper()
		return gitDispatch(t, h, rpcLine(t, method, params))
	}

	if raw, want := call("git.status", map[string]any{"path": f.W, "baseRepo": f.T}), errFrame(t, text); raw != want {
		t.Errorf("git.status = %s\nwant %s", raw, want)
	}
	if raw, want := call("git.info", map[string]any{"path": f.T}), errFrame(t, text); raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	w1 := filepath.Join(f.T, ".claude", "worktrees", "w1")
	raw := call("git.worktree_create", map[string]any{"baseRepo": f.T, "branchName": "w1", "worktreePath": w1})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + quotedJSON(t, text) + `,"errorCode":"worktree_add_failed"}}`; raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if _, err := os.Lstat(w1); !os.IsNotExist(err) {
		t.Errorf("the create made %s: %v", w1, err)
	}
	raw = call("git.worktree_remove", map[string]any{"baseRepo": f.T, "worktreePath": w0, "branchName": "w0"})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		quotedJSON(t, "failed to remove worktree: could not check whether "+w0+" is locked: "+text) + `}}`; raw != want {
		t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
	}
	if _, err := os.Stat(w0); err != nil {
		t.Errorf("the remove touched %s: %v", w0, err)
	}
}
