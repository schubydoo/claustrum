package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oneFileArchive writes an archive with the file a.txt and gives its path.
func oneFileArchive(t *testing.T) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "a.tgz")
	writeTgz(t, archive, []tgzEntry{{name: "a.txt", body: "x\n"}}, 0)
	return archive
}

// namesIn gives the names of the entries of dir, in the order of the read.
func namesIn(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// TestFilesExtractTarRefusedHomeCreatesAndDeletesNothing: a destDir that the
// home guard refuses reaches no step of the preparation. No folder is made, the
// wipe is not reached, and the archive stays.
//
// The test is safe on a tree with no guard. The home is a temp folder, and the
// wipe is behind its seam. With no guard the request stops at the mkdir of
// destDir, which exists, so no entry is written.
func TestFilesExtractTarRefusedHomeCreatesAndDeletesNothing(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	// The third spelling has a folder that does not exist before a ".." part.
	// A mkdir of the raw path before the guard makes that folder.
	viaMissing := filepath.Join(base, "missing") + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Join("home", "someone")
	for _, destDir := range []string{home, filepath.Dir(home), viaMissing} {
		archive := oneFileArchive(t)
		got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": destDir}))
		if !strings.Contains(got, "destDir must not be or contain the home directory") {
			t.Errorf("extract into %q = %s, want the home refusal", destDir, got)
		}
		if wiped != "" {
			t.Errorf("destDir %q was refused but the wipe was reached for the name %q", destDir, wiped)
		}
		if _, err := os.Lstat(archive); err != nil {
			t.Errorf("the archive is gone after a refusal: %v", err)
		}
	}
	if got := namesIn(t, base); got != "home" {
		t.Errorf("the folder above home holds %s, want home alone", got)
	}
	if got := namesIn(t, filepath.Dir(home)); got != "someone" {
		t.Errorf("the parent of home holds %s, want someone alone", got)
	}
	if got := namesIn(t, home); got != "keep.txt" {
		t.Errorf("home holds %s, want keep.txt alone", got)
	}
}

// TestWipeDestDirCannotTakeTheParent: the wipe removes one name inside the
// handle of the parent. The handle refuses the names "." and "..", so the parent
// and the folder above it stay with their content.
func TestWipeDestDirCannotTakeTheParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.MkdirAll(filepath.Join(parent, "dest"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(parent, "keep.txt"), filepath.Join(filepath.Dir(parent), "above.txt")} {
		if err := os.WriteFile(p, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{".", ".."} {
		if err := wipeDestDir(root, name); err == nil {
			t.Errorf("wipeDestDir(%q) = nil, want a refusal", name)
		}
	}
	if got := namesIn(t, parent); got != "dest,keep.txt" {
		t.Errorf("the parent holds %s, want dest and keep.txt", got)
	}
	if got := namesIn(t, filepath.Dir(parent)); got != "above.txt,parent" {
		t.Errorf("the folder above holds %s, want above.txt and parent", got)
	}
}

// TestFilesExtractTarWipeNeedsOneEntryInBothViews holds the test before the
// wipe and after the mkdir. The path of destDir as the system resolves it and
// the name in the handle of the parent must name one entry. If they differ,
// nothing is deleted and nothing stays that the request made. The seam makes the
// two views differ, as a Windows path with the `\\?\` prefix and a dot after its
// last name does. Both reference builds extract for such a path (Windows VM).
//
// The test is safe on a tree with no such test: each folder is in a temp folder
// of this test.
func TestFilesExtractTarWipeNeedsOneEntryInBothViews(t *testing.T) {
	notExist := func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	for _, tc := range []struct {
		name       string
		destExists bool
		byPath     func(sibling string) func(string) (os.FileInfo, error)
		wantText   string
	}{
		{"the path names no entry and the handle names a folder", true,
			func(string) func(string) (os.FileInfo, error) { return notExist },
			`clean destDir: "dest" is not the entry that the path names`},
		{"the path and the handle name two folders", true,
			func(sibling string) func(string) (os.FileInfo, error) {
				return func(string) (os.FileInfo, error) { return os.Lstat(sibling) }
			},
			`clean destDir: "dest" is not the entry that the path names`},
		{"the mkdir makes a folder that the path does not name", false,
			func(string) func(string) (os.FileInfo, error) { return notExist },
			`mkdir destDir: "dest" is not the entry that the path names`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			sibling := filepath.Join(parent, "sibling")
			keep := []string{filepath.Join(sibling, "keep.txt")}
			if tc.destExists {
				keep = append(keep, filepath.Join(dest, "keep.txt"))
			}
			for _, k := range keep {
				if err := os.MkdirAll(filepath.Dir(k), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(k, []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { lstatDestByPath = os.Lstat })
			lstatDestByPath = tc.byPath(sibling)

			n, err := extractTarGz(oneFileArchive(t), dest)
			if err == nil || err.Error() != tc.wantText {
				t.Errorf("extractTarGz = %v, want %s", err, tc.wantText)
			}
			if n != 0 {
				t.Errorf("fileCount = %d, want 0", n)
			}
			for _, k := range keep {
				if b, err := os.ReadFile(k); err != nil || string(b) != "keep\n" {
					t.Errorf("%s = %q (err %v), want it unchanged", k, b, err)
				}
			}
			want := "sibling"
			if tc.destExists {
				want = "dest,sibling"
			}
			if got := namesIn(t, parent); got != want {
				t.Errorf("the parent holds %q, want %q", got, want)
			}
		})
	}
}

// TestFilesExtractTarParentIsAFile holds the prefix of the answer for a folder
// above destDir that cannot be made, on each system. A file is at the place of
// the parent folder. 89cb6289 and 5fd08069 answer "mkdir parent: mkdir <path of
// the file>: <reason>" there (Linux and Windows VMs). The reason is the text of
// the system, so the test holds the prefix alone. The file keeps its content.
func TestFilesExtractTarParentIsAFile(t *testing.T) {
	for _, rel := range []string{filepath.Join("f.txt", "dest"), filepath.Join("f.txt", "m1", "dest")} {
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			x := t.TempDir()
			file := filepath.Join(x, "f.txt")
			if err := os.WriteFile(file, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			n, err := extractTarGz(oneFileArchive(t), filepath.Join(x, rel))
			if want := "mkdir parent: mkdir " + file + ": "; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("extractTarGz = %v, want a text that starts with %s", err, want)
			}
			if n != 0 {
				t.Errorf("fileCount = %d, want 0", n)
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != "keep\n" {
				t.Errorf("f.txt = %q (err %v), want it unchanged", b, err)
			}
			if got := namesIn(t, x); got != "f.txt" {
				t.Errorf("the folder holds %q, want f.txt alone", got)
			}
		})
	}
}
