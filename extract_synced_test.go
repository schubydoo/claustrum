package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginDir is a last name of 16 lower-case hex characters, as in the rows of
// 683cc6a9.
const pluginDir = "0123456789abcdef"

// writeFileForTest writes body at path and makes the folders above it.
func writeFileForTest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFileArchive writes an archive with the one file new and gives its path.
func newFileArchive(t *testing.T) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "n.tgz")
	writeTgz(t, archive, []tgzEntry{{name: "new", body: "n\n"}}, 0)
	return archive
}

// TestFilesExtractTarAlreadyComplete holds the answer of 683cc6a9 for a plugin
// folder with a regular file .synced (Linux, macOS and Windows VMs). Nothing is
// unpacked and nothing is deleted, and the archive is gone after the reply. The
// wipe is behind its seam, so a wrong arm deletes nothing.
func TestFilesExtractTarAlreadyComplete(t *testing.T) {
	logs := captureLogBuf(t)
	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	dest := filepath.Join(t.TempDir(), "plugins", pluginDir)
	writeFileForTest(t, filepath.Join(dest, ".synced"), "")
	writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
	archive := newFileArchive(t)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":0,"alreadyComplete":true}}`; got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if wiped != "" {
		t.Errorf("the wipe was reached for the name %q", wiped)
	}
	if got := namesIn(t, dest); got != ".synced,keep" {
		t.Errorf("destDir holds %q, want .synced and keep", got)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Errorf("the archive still exists (err %v), want it gone", err)
	}
	if want := "[Files] extract_tar: plugin dir " + pluginDir + " already has its .synced marker, so the archive was not unpacked"; !strings.Contains(logs.String(), want) {
		t.Errorf("the log has no line %q", want)
	}
}

// TestFilesExtractTarNotAlreadyComplete holds the kinds that get the normal wipe
// and unpack on 683cc6a9 (Linux VM, and the folder and abc rows on macOS and
// Windows VMs): a last name that is not 16 lower-case hex characters, and a
// .synced that is a folder.
func TestFilesExtractTarNotAlreadyComplete(t *testing.T) {
	for _, tc := range []struct {
		name, last string
		synced     func(t *testing.T, path string)
	}{
		{"upper-case hex", strings.ToUpper(pluginDir), nil},
		{"15 characters", pluginDir[:15], nil},
		{"17 characters", pluginDir + "0", nil},
		{"abc", "abc", nil},
		{"an empty folder at .synced", pluginDir, func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a folder with a file at .synced", pluginDir, func(t *testing.T, path string) {
			writeFileForTest(t, filepath.Join(path, "in"), "in\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			dest := filepath.Join(t.TempDir(), "plugins", tc.last)
			writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
			if tc.synced == nil {
				writeFileForTest(t, filepath.Join(dest, ".synced"), "")
			} else {
				tc.synced(t, filepath.Join(dest, ".synced"))
			}
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if got := namesIn(t, dest); got != ".synced,new" {
				t.Errorf("destDir holds %q, want .synced and new", got)
			}
		})
	}
}

// TestFilesExtractTarSkipsEntriesThatReadAsSynced holds the shape of the shared
// archive of 683cc6a9: 11 entries, 8 with a top-level name that reads as
// .synced, 7 regular files, 5 of them skipped. Each system answers fileCount 7
// and logs a skip count of 8, and x/.synced stays (Linux, macOS and Windows VMs).
func TestFilesExtractTarSkipsEntriesThatReadAsSynced(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	dest := filepath.Join(t.TempDir(), "plugins", pluginDir)
	archive := filepath.Join(t.TempDir(), "b.tgz")
	writeTgz(t, archive, []tgzEntry{
		{name: ".synced", body: "s\n"},
		{name: ".SYNCED/", dir: true},
		{name: ".SYNCED/a", body: "a\n"},
		{name: ".SYNCED/q/", dir: true},
		{name: ".SYNCED/q/b", body: "b\n"},
		{name: ".synced./", dir: true},
		{name: ".synced./b", body: "b\n"},
		{name: ".synced\u200d", body: "z\n"},
		{name: "x/", dir: true},
		{name: "x/.synced", body: "x\n"},
		{name: "f", body: "f\n"},
	}, 0)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":7}}`; got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if got := namesIn(t, dest); got != ".synced,f,x" {
		t.Errorf("destDir holds %q, want .synced, f and x", got)
	}
	if b, err := os.ReadFile(filepath.Join(dest, ".synced")); err != nil || len(b) != 0 {
		t.Errorf(".synced = %q (err %v), want the empty marker", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "x", ".synced")); err != nil || string(b) != "x\n" {
		t.Errorf("x/.synced = %q (err %v), want its 2 bytes", b, err)
	}
	if want := `extract_tar: 8 entries in the archive for "` + pluginDir + `" were not unpacked: their top-level name reads as .synced, which only the helper may write; rename them in the plugin to have them copied`; !strings.Contains(logs.String(), want) {
		t.Errorf("the log has no line %q", want)
	}
}

// TestReadsAsSynced holds the name rule. The rows with a trailing space and with
// another format character are claustrum's own, not measured.
func TestReadsAsSynced(t *testing.T) {
	sep := string(filepath.Separator)
	for name, want := range map[string]bool{
		".synced":               true,
		".SYNCED" + sep + "a":   true,
		".synced." + sep + "b":  true,
		".synced\u200d":         true,
		".synced ":              true,
		".syn\u200bced":         true,
		"x" + sep + ".synced":   false,
		".":                     false,
		"a.synced":              false,
		".synce":                false,
		".\u017fynced":          false,
		".synced2":              false,
		"synced":                false,
		".synced" + sep + "..x": true,
	} {
		if got := readsAsSynced(name); got != want {
			t.Errorf("readsAsSynced(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestFilesExtractTarRemovesSyncedAfterAFailure: a .synced that appears in the
// new destDir during the request is gone after a failed request. 683cc6a9 does
// that after an unsafe path (Linux, macOS and Windows VMs) and a cut archive
// (Linux VM). The seam after the mkdir makes the .synced, and every file is in a
// temp folder of this test.
func TestFilesExtractTarRemovesSyncedAfterAFailure(t *testing.T) {
	big := strings.Repeat("b", 4096)
	for _, tc := range []struct {
		name    string
		entries []tgzEntry
		cut     int
		want    string
	}{
		{"an unsafe path", []tgzEntry{{name: "big", body: big}, {name: "../e", body: "e\n"}}, 0, "unsafe path in archive: ../e"},
		{"a cut archive", []tgzEntry{{name: "big", body: big}}, 512 + 1024, "write big: unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			dest := filepath.Join(t.TempDir(), "plugins", pluginDir)
			archive := filepath.Join(t.TempDir(), "f.tgz")
			writeTgz(t, archive, tc.entries, tc.cut)
			t.Cleanup(func() { afterDestDirMkdir = func() {} })
			afterDestDirMkdir = func() { writeFileForTest(t, filepath.Join(dest, ".synced"), "") }

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"` + tc.want + `"}}`; got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if got := namesIn(t, dest); got != "big" {
				t.Errorf("destDir holds %q, want big alone", got)
			}
		})
	}
}
