package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFilesExtractTarEntryCollisionTexts holds the two texts of an archive whose
// second entry collides with its first. The frames are the ones of 5fd08069 in
// cells 2a2-10-file-then-below and 2a2-11-dir-then-file of the Linux VM. The
// cells 2a-file-then-below and 2a-dir-then-file of the macOS VM and of the
// Windows VM hold them too. The Linux cell names its directory x, and this test
// names it d as the two other cells do. The third entry shows that the
// extraction stops at the failure.
func TestFilesExtractTarEntryCollisionTexts(t *testing.T) {
	createText := "create d: openat d: file exists"
	if runtime.GOOS == "windows" {
		createText = "create d: openat d: is a directory"
	}
	for _, tc := range []struct {
		name     string
		entries  []tgzEntry
		wantText string
		wantLeft string // the one entry of destDir after the request
		leftDir  bool
	}{
		{
			name:     "file then an entry below it",
			entries:  []tgzEntry{{name: "a", body: "x\n"}, {name: "a/b", body: "y\n"}, {name: "c.txt", body: "z\n"}},
			wantText: "mkdir parent a/b: mkdirat a: file exists",
			wantLeft: "a",
		},
		{
			name:     "directory then a file of that name",
			entries:  []tgzEntry{{name: "d", dir: true}, {name: "d", body: "y\n"}, {name: "c.txt", body: "z\n"}},
			wantText: createText,
			wantLeft: "d",
			leftDir:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := filepath.Join(t.TempDir(), "a.tgz")
			writeTgz(t, archive, tc.entries, 0)
			dest := filepath.Join(t.TempDir(), "dest")

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"` + tc.wantText + `"}}`
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			left, err := os.ReadDir(dest)
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 1 || left[0].Name() != tc.wantLeft || left[0].IsDir() != tc.leftDir {
				t.Errorf("destDir holds %v, want only %q (directory: %v)", left, tc.wantLeft, tc.leftDir)
			}
			if _, err := os.Lstat(archive); !os.IsNotExist(err) {
				t.Errorf("archive still exists (err %v), want it gone", err)
			}
		})
	}
}

// TestFilesExtractTarSecondFileEntryReplaces: a second file entry with the name
// of an earlier one replaces the file. The rows are equal on 89cb6289 and
// 5fd08069 in the reply and in the content (Linux, macOS and Windows VMs). The
// file has the name of the last entry: on a Windows VM `a.txt` and then `A.TXT`
// leave `A.TXT` on 5fd08069.
func TestFilesExtractTarSecondFileEntryReplaces(t *testing.T) {
	type row struct {
		name     string
		entries  []tgzEntry
		wantName string
		wantBody string
	}
	rows := []row{
		{"a shorter second entry", []tgzEntry{{name: "a.txt", body: "0123456789"}, {name: "a.txt", body: "abc"}}, "a.txt", "abc"},
		{"a.txt and then ./a.txt", []tgzEntry{{name: "a.txt", body: "one\n"}, {name: "./a.txt", body: "two\n"}}, "a.txt", "two\n"},
	}
	if runtime.GOOS == "windows" {
		rows = append(rows, row{"Windows a.txt and then A.TXT", []tgzEntry{{name: "a.txt", body: "one\n"}, {name: "A.TXT", body: "two\n"}}, "A.TXT", "two\n"})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := filepath.Join(t.TempDir(), "a.tgz")
			writeTgz(t, archive, tc.entries, 0)
			dest := filepath.Join(t.TempDir(), "dest")

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":2}}`; got != want {
				t.Fatalf("frame = %s, want %s", got, want)
			}
			var names []string
			left, err := os.ReadDir(dest)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range left {
				if e.Name() != ".synced" {
					names = append(names, e.Name())
				}
			}
			if len(names) != 1 || names[0] != tc.wantName {
				t.Errorf("destDir holds %v, want only %s", names, tc.wantName)
			}
			if b, err := os.ReadFile(filepath.Join(dest, tc.wantName)); err != nil || string(b) != tc.wantBody {
				t.Errorf("%s = %q (err %v), want %q", tc.wantName, b, err, tc.wantBody)
			}
		})
	}
}

// TestFilesExtractTarDirectoryEntryTexts holds the answer for a directory entry at
// or below a file that the archive wrote before. The frames are the ones of
// 5fd08069 on Linux, macOS and Windows VMs.
// 89cb6289 has the prefix and the fileCount 0 too, with another text after the
// prefix.
func TestFilesExtractTarDirectoryEntryTexts(t *testing.T) {
	below := "mkdir a/b/: openat a: not a directory"
	if runtime.GOOS == "windows" {
		below = "mkdir a/b/: openat a: The system cannot find the path specified."
	}
	for _, tc := range []struct {
		name     string
		entries  []tgzEntry
		wantText string
	}{
		{"directory entry at a file", []tgzEntry{{name: "a", body: "x\n"}, {name: "a/", dir: true}, {name: "c.txt", body: "z\n"}}, "mkdir a/: mkdirat a: file exists"},
		{"directory entry below a file", []tgzEntry{{name: "a", body: "x\n"}, {name: "a/b/", dir: true}, {name: "c.txt", body: "z\n"}}, below},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := filepath.Join(t.TempDir(), "a.tgz")
			writeTgz(t, archive, tc.entries, 0)
			dest := filepath.Join(t.TempDir(), "dest")

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"` + tc.wantText + `"}}`
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			left, err := os.ReadDir(dest)
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 1 || left[0].Name() != "a" || left[0].IsDir() {
				t.Errorf("destDir holds %v, want only the file a", left)
			}
		})
	}
}

// TestFilesExtractTarMarkerReplacesArchiveEntry holds the marker rule. After the
// entries, .synced at the top of destDir is an empty file, whatever the archive
// put there. The rows are equal on 89cb6289 and 5fd08069 (Linux, macOS and
// Windows VMs).
func TestFilesExtractTarMarkerReplacesArchiveEntry(t *testing.T) {
	file := func(name, body string) tgzEntry { return tgzEntry{name: name, body: body} }
	dir := func(name string) tgzEntry { return tgzEntry{name: name, dir: true} }
	for _, tc := range []struct {
		name     string
		entries  []tgzEntry
		count    int
		wantStay string // a path below destDir that stays, or ""
	}{
		{"directory entry alone", []tgzEntry{dir(".synced/")}, 0, ""},
		{"file and then the directory entry", []tgzEntry{file("a.txt", "a\n"), dir(".synced/")}, 1, "a.txt"},
		{"directory entry with a file", []tgzEntry{dir(".synced/"), file(".synced/in.txt", "in\n")}, 1, ""},
		{"directory entry with a folder and a file", []tgzEntry{dir(".synced/"), dir(".synced/deep/"), file(".synced/deep/f", "f\n")}, 1, ""},
		{"file entry with content", []tgzEntry{file(".synced", "thirteen byte")}, 1, ""},
		// A .synced below another folder is not the marker.
		{"directory entry below a folder", []tgzEntry{dir("sub/.synced/"), file("sub/.synced/f", "sf\n")}, 1, "sub/.synced/f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := filepath.Join(t.TempDir(), "a.tgz")
			writeTgz(t, archive, tc.entries, 0)
			dest := filepath.Join(t.TempDir(), "dest")

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			want := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":%d}}`, tc.count)
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			fi, err := os.Lstat(filepath.Join(dest, ".synced"))
			if err != nil {
				t.Fatal(err)
			}
			if !fi.Mode().IsRegular() || fi.Size() != 0 {
				t.Errorf(".synced is %v with %d bytes, want an empty regular file", fi.Mode(), fi.Size())
			}
			if tc.wantStay != "" {
				if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(tc.wantStay))); err != nil {
					t.Errorf("%s does not stay: %v", tc.wantStay, err)
				}
			}
		})
	}
}
