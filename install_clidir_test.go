package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Tests for a cli-dir path that names something that is not a folder. The cells
// are E12x-a to E12x-n of the 89cb6289 measurement (f6010b97 and 89cb6289, one
// run each, on Linux, macOS and Windows). This file holds the cells with regular
// files and folders, which run on every system. install_clidir_unix_test.go
// holds the symlink, FIFO and permission cells. Windows has no FIFO cell and no
// cell with a parent that is not writable.

// cliDirCell is one cell: a parent folder with the entries of the row.
type cliDirCell struct {
	*stubFixture
	parent string
}

// newCLIDirCell makes an empty parent folder. The cli folder of the stub fixture
// is not used.
func newCLIDirCell(t *testing.T) *cliDirCell {
	t.Helper()
	f := newStubFixture(t)
	c := &cliDirCell{stubFixture: f, parent: filepath.Join(f.root, "cell")}
	if err := os.Mkdir(c.parent, 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// file writes a regular file in the parent folder and returns its path.
func (c *cliDirCell) file(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(c.parent, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// names is the sorted list of the parent folder.
func (c *cliDirCell) names(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(c.parent)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

// wantParent compares the parent folder with a list of names.
func (c *cliDirCell) wantParent(t *testing.T, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := c.names(t); !slices.Equal(got, want) {
		t.Errorf("parent folder = %v, want %v", got, want)
	}
}

// wantFolder0700 checks that dir is a real folder, with mode 0700 off Windows.
func wantFolder0700(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("%s: err %v, want a folder in place of the old entry", dir, err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("%s has mode %04o, want 0700", dir, fi.Mode().Perm())
	}
}

// wantContent checks that a kept file still has its content.
func wantContent(t *testing.T, p, want string) {
	t.Helper()
	if b, err := os.ReadFile(p); err != nil || string(b) != want {
		t.Errorf("%s: err %v, content %q, want it kept with %q", p, err, b, want)
	}
}

// Cells a, b, g, h and l. A regular file at the cli-dir path is removed, and so
// is the file named exactly ccd-cli-version beside it. The cli folder is made
// and the install goes on. Every other file beside it stays: the name of the
// version file is literal, and it does not follow the name of the cli-dir.
func TestInstallReplacesARegularFileAtTheCLIDir(t *testing.T) {
	for _, tc := range []struct {
		cell, dirName string
		marker        bool
		keep          []string
	}{
		{"a", "cli", true, nil},
		{"b: no version file", "cli", false, nil},
		{"g: another cli-dir name", "other", true, []string{"other-version"}},
		{"h: cli-dir named ccd-cli", "ccd-cli", true, nil},
		{"l: files with near names", "cli", true, []string{"ccd-cli-version.bak", "zzz-version"}},
	} {
		t.Run(tc.cell, func(t *testing.T) {
			c := newCLIDirCell(t)
			dir := c.file(t, tc.dirName, "a regular file\n")
			if tc.marker {
				c.file(t, cliVersionMarker, "beside\n")
			}
			for _, k := range tc.keep {
				c.file(t, k, "keep "+k)
			}
			blob := c.stubBlob(t)
			cli := installCLIPath(dir, "9.9.9")
			out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: blob})
			if want := factsHead(t, cli) + "false}\n"; out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			wantFolder0700(t, dir)
			if !isRegularFile(cli) {
				t.Error("the CLI was not installed in the new cli folder")
			}
			c.wantParent(t, append([]string{tc.dirName}, tc.keep...)...)
			for _, k := range tc.keep {
				wantContent(t, filepath.Join(c.parent, k), "keep "+k)
			}
			if _, err := os.Lstat(blob); !os.IsNotExist(err) {
				t.Errorf("the blob must be consumed, Lstat err = %v", err)
			}
		})
	}
}

// Cell j. The replace happens with no source flag too, before the "missing"
// answer. The file and the version file are gone, and an empty cli folder is
// there. cliPath is not empty.
func TestInstallReplacesTheCLIDirFileWithNoSource(t *testing.T) {
	c := newCLIDirCell(t)
	dir := c.file(t, "cli", "a regular file\n")
	c.file(t, cliVersionMarker, "beside\n")
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9"})
	want := factsHead(t, installCLIPath(dir, "9.9.9")) + `false,"cliError":"cli 9.9.9 missing and no --cli-url or --cli-zst provided"}` + "\n"
	if out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, dir)
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Errorf("the new cli folder holds %v (err %v), want it empty", ents, err)
	}
	c.wantParent(t, "cli")
}

// Cell m. With no -cli-dir the same rule holds for the default folder: a file at
// <home>/.claude/remote/ccd-cli and the version file beside it are removed, and
// the install lands in the new folder. The two levels above keep their modes.
func TestInstallReplacesAFileAtTheDefaultCLIDir(t *testing.T) {
	f := newStubFixture(t)
	home := filepath.Join(f.root, "home")
	remote := filepath.Join(home, ".claude", "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	// MkdirAll passes its mode through the umask. The assertion below wants 0755.
	for _, d := range []string{filepath.Join(home, ".claude"), remote} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(remote, "ccd-cli")
	marker := filepath.Join(remote, cliVersionMarker)
	for _, p := range []string{dir, marker} {
		if err := os.WriteFile(p, []byte("a regular file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cli := installCLIPath(dir, "9.9.9")
	out := captureInstallOutput(t, installOpts{home: home, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	if want := factsHead(t, cli) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, dir)
	if !isRegularFile(cli) {
		t.Error("the CLI was not installed in the default folder")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the version file beside the default folder stays, Lstat err = %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Join(home, ".claude"), remote} {
			if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("%s: err %v, want it kept with mode 0755", d, err)
			}
		}
	}
}

// Cells c and n. A cli-dir that is a folder, or that does not exist, triggers
// nothing: the version file beside it stays.
func TestInstallKeepsTheVersionFileBesideAFolderOrAnAbsentCLIDir(t *testing.T) {
	for _, cell := range []string{"c: a folder", "n: absent"} {
		t.Run(cell, func(t *testing.T) {
			c := newCLIDirCell(t)
			dir := filepath.Join(c.parent, "cli")
			if strings.HasPrefix(cell, "c") {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			marker := c.file(t, cliVersionMarker, "beside\n")
			out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: c.stubBlob(t)})
			if want := factsHead(t, installCLIPath(dir, "9.9.9")) + "false}\n"; out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			wantContent(t, marker, "beside\n")
			c.wantParent(t, "cli", cliVersionMarker)
		})
	}
}

// Not measured on the reference: a ccd-cli-version that is not a regular file.
// claustrum removes the version file only when it is a regular file. A folder
// with that name stays, with its content, and the replace of the cli-dir file
// goes on.
func TestInstallLeavesAVersionMarkerThatIsAFolder(t *testing.T) {
	c := newCLIDirCell(t)
	dir := c.file(t, "cli", "a regular file\n")
	marker := filepath.Join(c.parent, cliVersionMarker)
	if err := os.Mkdir(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(marker, "inner")
	if err := os.WriteFile(inner, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: c.stubBlob(t)})
	if want := factsHead(t, installCLIPath(dir, "9.9.9")) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, dir)
	wantContent(t, inner, "keep")
}

// The D6 rule runs BEFORE the replace. This test discriminates that order only:
// with the replace moved in front of the rule, the file at the cli-dir path and
// the version file beside it are removed for a refused -cli-version.
func TestInstallVersionGuardRunsBeforeTheCLIDirReplace(t *testing.T) {
	c := newCLIDirCell(t)
	dir := c.file(t, "cli", "a regular file\n")
	marker := c.file(t, cliVersionMarker, "beside\n")
	facts := captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: "../victim", cliZst: c.stubBlob(t)})
	if !strings.Contains(facts.CliError, "single path component") {
		t.Errorf("CliError = %q, want the D6 refusal", facts.CliError)
	}
	wantContent(t, dir, "a regular file\n")
	wantContent(t, marker, "beside\n")
}
