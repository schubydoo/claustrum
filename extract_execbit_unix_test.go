//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// modeEntry is one member of an archive whose members carry their own modes.
type modeEntry struct {
	name string
	mode int64
	dir  bool
}

// writeModeTgz writes the members to a .tar.gz at path. Each file has the body "x\n".
func writeModeTgz(t *testing.T, path string, entries []modeEntry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: tar.TypeReg, Size: 2}
		if e.dir {
			hdr = &tar.Header{Name: e.name, Mode: e.mode, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte("x\n")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFilesExtractTarKeepsExecuteBit holds the modes of the extracted entries. A
// file whose archive mode has the execute bit of the owner arrives 0700, and each
// other file arrives 0600. The rows are those of 5fd08069 on Linux and macOS
// VMs. The row deep/er/f is from the Linux VM alone. The rows with the names g
// and d0755/g show that a group or other execute bit alone gives 0600.
func TestFilesExtractTarKeepsExecuteBit(t *testing.T) {
	// The daemon of the cell ran with umask 022. The umask is process-wide, so
	// this test must not run in parallel with another test.
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	rows := []struct {
		entry modeEntry
		want  os.FileMode
	}{
		{modeEntry{name: "f0644", mode: 0o644}, 0o600},
		{modeEntry{name: "f0600", mode: 0o600}, 0o600},
		{modeEntry{name: "f0444", mode: 0o444}, 0o600},
		{modeEntry{name: "f0755", mode: 0o755}, 0o700},
		{modeEntry{name: "f0700", mode: 0o700}, 0o700},
		{modeEntry{name: "f0111", mode: 0o111}, 0o700},
		{modeEntry{name: "f4755", mode: 0o4755}, 0o700},
		{modeEntry{name: "f2755", mode: 0o2755}, 0o700},
		{modeEntry{name: "f1777", mode: 0o1777}, 0o700},
		{modeEntry{name: "d0755", mode: 0o755, dir: true}, os.ModeDir | 0o700},
		{modeEntry{name: "d0700", mode: 0o700, dir: true}, os.ModeDir | 0o700},
		{modeEntry{name: "d0555", mode: 0o555, dir: true}, os.ModeDir | 0o700},
		{modeEntry{name: "d0755/in", mode: 0o644}, 0o600},
		// No directory entry names deep or deep/er.
		{modeEntry{name: "deep/er/f", mode: 0o755}, 0o700},
	}
	for _, m := range []int64{0o010, 0o001, 0o011, 0o654, 0o645, 0o610, 0o601, 0o674} {
		rows = append(rows,
			struct {
				entry modeEntry
				want  os.FileMode
			}{modeEntry{name: fmt.Sprintf("g%04o", m), mode: m}, 0o600},
			struct {
				entry modeEntry
				want  os.FileMode
			}{modeEntry{name: fmt.Sprintf("d0755/g%04o", m), mode: m}, 0o600})
	}
	for _, m := range []int64{0o100, 0o744} {
		rows = append(rows,
			struct {
				entry modeEntry
				want  os.FileMode
			}{modeEntry{name: fmt.Sprintf("g%04o", m), mode: m}, 0o700},
			struct {
				entry modeEntry
				want  os.FileMode
			}{modeEntry{name: fmt.Sprintf("d0755/g%04o", m), mode: m}, 0o700})
	}
	entries := make([]modeEntry, 0, len(rows))
	files := 0
	for _, r := range rows {
		entries = append(entries, r.entry)
		if !r.entry.dir {
			files++
		}
	}
	archive := filepath.Join(t.TempDir(), "modes.tgz")
	writeModeTgz(t, archive, entries)
	dest := filepath.Join(t.TempDir(), "dest")

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if want := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":%d}}`, files); got != want {
		t.Fatalf("frame = %s, want %s", got, want)
	}

	// The whole mode is compared, so a setuid, setgid or sticky bit fails a row.
	wantMode := map[string]os.FileMode{
		".":       os.ModeDir | 0o700,
		".synced": 0o600,
		"deep":    os.ModeDir | 0o700,
		"deep/er": os.ModeDir | 0o700,
	}
	for _, r := range rows {
		wantMode[r.entry.name] = r.want
	}
	for name, want := range wantMode {
		fi, err := os.Lstat(filepath.Join(dest, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if fi.Mode() != want {
			t.Errorf("%s: mode %v, want %v", name, fi.Mode(), want)
		}
	}
	if fi, err := os.Lstat(filepath.Join(dest, ".synced")); err == nil && fi.Size() != 0 {
		t.Errorf(".synced has %d bytes, want 0", fi.Size())
	}
}

// TestFilesExtractTarDestDirNotSearchable holds the answer for a destDir that
// the daemon cannot search. With a umask of 0100 the new destDir gets mode 0600.
// The frame is the one of 5fd08069 in cell 2a1-modes-u0100 (Linux VM) and cell
// 2a-modes-u100 (macOS VM). The disk is the one of those cells too.
func TestFilesExtractTarDestDirNotSearchable(t *testing.T) {
	skipIfRoot(t)
	s := newTestServer(t)
	archive := filepath.Join(t.TempDir(), "modes.tgz")
	writeModeTgz(t, archive, []modeEntry{{name: "f0644", mode: 0o644}})
	dest := filepath.Join(t.TempDir(), "dest")

	// The umask is process-wide, so this test must not run in parallel with
	// another test. It changes back right after the request.
	old := syscall.Umask(0o100)
	restore := func() { syscall.Umask(old) }
	t.Cleanup(restore)
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	restore()

	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"open destDir: \"dest\" could not be examined (statat .: permission denied)"}}`
	if got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != os.ModeDir|0o600 {
		t.Errorf("destDir mode = %v, want drw-------", fi.Mode())
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if names, err := os.ReadDir(dest); err != nil || len(names) != 0 {
		t.Errorf("destDir holds %v (err %v), want no entry", names, err)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Errorf("archive still exists (err %v), want it gone", err)
	}
}

// TestFilesExtractTarLastEntryGivesTheMode holds the mode of a file that the
// archive names more than once. The file has the mode of the last entry, and its
// content. The rows are those of 5fd08069 on Linux and macOS VMs. `a.txt` and
// `./a.txt` are one name.
func TestFilesExtractTarLastEntryGivesTheMode(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	for _, tc := range []struct {
		name    string
		entries []modeEntry
		want    os.FileMode
	}{
		{"0644 then 0755", []modeEntry{{name: "a.txt", mode: 0o644}, {name: "a.txt", mode: 0o755}}, 0o700},
		{"0755 then 0644", []modeEntry{{name: "a.txt", mode: 0o755}, {name: "a.txt", mode: 0o644}}, 0o600},
		{"0644, 0755, 0644", []modeEntry{{name: "a.txt", mode: 0o644}, {name: "a.txt", mode: 0o755}, {name: "a.txt", mode: 0o644}}, 0o600},
		{"a.txt 0644 then ./a.txt 0755", []modeEntry{{name: "a.txt", mode: 0o644}, {name: "./a.txt", mode: 0o755}}, 0o700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := filepath.Join(t.TempDir(), "a.tgz")
			writeModeTgz(t, archive, tc.entries)
			dest := filepath.Join(t.TempDir(), "dest")

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
			want := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":%d}}`, len(tc.entries))
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			fi, err := os.Lstat(filepath.Join(dest, "a.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode() != tc.want {
				t.Errorf("a.txt: mode %v, want %v", fi.Mode(), tc.want)
			}
		})
	}
}

// TestFilesExtractTarMarkerModeIsFixed: a file entry .synced of mode 0755 ends as
// the marker of mode 0600, as on 89cb6289 and 5fd08069 (Linux and macOS VMs).
func TestFilesExtractTarMarkerModeIsFixed(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	s := newTestServer(t)
	archive := filepath.Join(t.TempDir(), "a.tgz")
	writeModeTgz(t, archive, []modeEntry{{name: ".synced", mode: 0o755}})
	dest := filepath.Join(t.TempDir(), "dest")

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	fi, err := os.Lstat(filepath.Join(dest, ".synced"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != 0o600 || fi.Size() != 0 {
		t.Errorf(".synced: mode %v with %d bytes, want -rw------- with 0", fi.Mode(), fi.Size())
	}
}

// TestSyncedMarkerRemoveStaysInDestDir is the guard test of the marker remove.
// A link named .synced that leads out of destDir goes as a link. Its target
// stays, with its content. Each target is in a temp folder of this test, so a
// wrong remove or a write through the link can reach nothing else.
func TestSyncedMarkerRemoveStaysInDestDir(t *testing.T) {
	for _, tc := range []struct {
		name      string
		targetDir bool
	}{
		{"link to a file outside", false},
		{"link to a folder outside", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := t.TempDir()
			keep := filepath.Join(outside, "keep.txt")
			if err := os.WriteFile(keep, []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "dest")
			if err := os.Mkdir(dest, 0o700); err != nil {
				t.Fatal(err)
			}
			target := keep
			if tc.targetDir {
				target = outside
			}
			if err := os.Symlink(target, filepath.Join(dest, ".synced")); err != nil {
				t.Fatal(err)
			}
			destRoot, err := os.OpenRoot(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer destRoot.Close()

			if err := writeSyncedMarker(destRoot); err != nil {
				t.Errorf("writeSyncedMarker: %v", err)
			}
			if b, err := os.ReadFile(keep); err != nil || string(b) != "keep\n" {
				t.Errorf("the file outside destDir = %q (err %v), want it unchanged", b, err)
			}
			fi, err := os.Lstat(filepath.Join(dest, ".synced"))
			if err != nil {
				t.Fatal(err)
			}
			if !fi.Mode().IsRegular() || fi.Size() != 0 {
				t.Errorf(".synced is %v, want an empty regular file in place of the link", fi.Mode())
			}
		})
	}
}

// TestSyncedMarkerWriteGoesThroughTheHandle: the marker write is a create through
// the handle of destDir, so its failure text names .synced and no whole path. A
// destDir of mode 0500 refuses the create. No reference build is measured there.
func TestSyncedMarkerWriteGoesThroughTheHandle(t *testing.T) {
	skipIfRoot(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.Mkdir(dest, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o700) })
	destRoot, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer destRoot.Close()

	err = writeSyncedMarker(destRoot)
	if err == nil || err.Error() != "openat .synced: permission denied" {
		t.Errorf("writeSyncedMarker = %v, want openat .synced: permission denied", err)
	}
}

// TestFilesExtractTarRefusesSwappedDestDir is the guard test of the handle of
// destDir. Another process puts a link at destDir between the mkdir and the open.
// The request then fails before an entry is written and before the marker
// remove, so the folder behind the link keeps its content. Both folders are temp
// folders of this test, so a missing guard can reach nothing else.
func TestFilesExtractTarRefusesSwappedDestDir(t *testing.T) {
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, ".synced"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(other, ".synced", "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	archive := filepath.Join(t.TempDir(), "a.tgz")
	writeTgz(t, archive, []tgzEntry{{name: "a.txt", body: "x\n"}}, 0)

	t.Cleanup(func() { openDestRoot = os.OpenRoot })
	openDestRoot = func(name string) (*os.Root, error) {
		// The swap: the new empty destDir goes, and a link to the other folder
		// takes its place.
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, name); err != nil {
			t.Fatal(err)
		}
		return os.OpenRoot(name)
	}

	n, err := extractTarGz(archive, dest)
	if want := `open destDir: "dest" changed while it was opened`; err == nil || err.Error() != want {
		t.Errorf("extractTarGz = %v, want %s", err, want)
	}
	if n != 0 {
		t.Errorf("fileCount = %d, want 0", n)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "keep\n" {
		t.Errorf("the file in the other folder = %q (err %v), want it unchanged", b, err)
	}
	left, err := os.ReadDir(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name() != ".synced" || !left[0].IsDir() {
		t.Errorf("the other folder holds %v, want only its .synced folder", left)
	}
}
