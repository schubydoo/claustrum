//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilesExtractTarWindowsSpellingsOfDestDir holds three spellings of destDir
// that name the folder dest. 5fd08069 removes the old content, makes a new
// folder named dest and extracts (Windows VM). 89cb6289 keeps the old content
// for a dot after the name, and it fails for a space after the name.
func TestFilesExtractTarWindowsSpellingsOfDestDir(t *testing.T) {
	for _, suffix := range []string{".", " ", `\.`} {
		t.Run("dest"+suffix, func(t *testing.T) {
			s := newTestServer(t)
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest + suffix}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
				t.Errorf("frame = %s, want %s", got, want)
			}
			if got := namesIn(t, parent); got != "dest" {
				t.Errorf("the parent holds %q, want dest alone", got)
			}
			if got := namesIn(t, dest); got != ".synced,a.txt" {
				t.Errorf("destDir holds %q, want the marker and a.txt", got)
			}
		})
	}
}

// TestFilesExtractTarWipeLeavesJunctionTarget is the Windows must-not-escape
// test of the wipe. A junction in the old content goes as a link, and its
// target keeps its content, as on 89cb6289 and 5fd08069 (Windows VM). The target
// is a temp folder of this test, so a wrong wipe can reach nothing else.
func TestFilesExtractTarWipeLeavesJunctionTarget(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	dest := filepath.Join(base, "x", "dest")
	for _, d := range []string{filepath.Join(outside, "sub"), dest} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"keep.txt", filepath.Join("sub", "k2.txt")} {
		if err := os.WriteFile(filepath.Join(outside, f), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	makeJunction(t, filepath.Join(dest, "j"), outside)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	for _, f := range []string{"keep.txt", filepath.Join("sub", "k2.txt")} {
		if b, err := os.ReadFile(filepath.Join(outside, f)); err != nil || string(b) != "keep\n" {
			t.Errorf("outside\\%s = %q (err %v), want it unchanged", f, b, err)
		}
	}
	if got := namesIn(t, dest); got != ".synced,a.txt" {
		t.Errorf("destDir holds %q, want the marker and a.txt", got)
	}
}

// TestFilesExtractTarWipeLeavesSiblingOfPrefixedName holds the test before the
// wipe with real Windows paths. A destDir with the `\\?\` prefix and a dot or a
// space after its last name names an entry that does not exist. The handle of
// the parent reads that name as the sibling folder dest. The request fails, and
// the sibling keeps its content. With no sibling the request fails too, and no
// folder stays. 89cb6289 extracts into a folder with the exact name there, and
// 5fd08069 into the folder dest (Windows VM). The sibling is a temp
// folder of this test, so a wrong wipe can reach nothing else.
func TestFilesExtractTarWipeLeavesSiblingOfPrefixedName(t *testing.T) {
	for _, suffix := range []string{".", " "} {
		for _, sibling := range []bool{true, false} {
			name := "dest" + suffix + " with no sibling"
			if sibling {
				name = "dest" + suffix + " with a sibling dest"
			}
			t.Run(name, func(t *testing.T) {
				parent := t.TempDir()
				keep := filepath.Join(parent, "dest", "keep.txt")
				if sibling {
					if err := os.Mkdir(filepath.Dir(keep), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				destDir := `\\?\` + filepath.Join(parent, "dest") + suffix

				n, err := extractTarGz(oneFileArchive(t), destDir)
				step := "mkdir destDir"
				if sibling {
					step = "clean destDir"
				}
				want := step + `: "dest` + suffix + `" is not the entry that the path names`
				if err == nil || err.Error() != want {
					t.Errorf("extractTarGz = %v, want %s", err, want)
				}
				if n != 0 {
					t.Errorf("fileCount = %d, want 0", n)
				}
				if !sibling {
					if got := namesIn(t, parent); got != "" {
						t.Errorf("the parent holds %q, want no entry", got)
					}
					return
				}
				if b, err := os.ReadFile(keep); err != nil || string(b) != "keep\n" {
					t.Errorf("the file in the sibling = %q (err %v), want it unchanged", b, err)
				}
				if got := namesIn(t, parent); got != "dest" {
					t.Errorf("the parent holds %q, want dest alone", got)
				}
			})
		}
	}
}

// TestFilesExtractTarRefusesWindowsSpellingsOfHome sends Windows spellings that
// name a temp home folder. Each row gets the home refusal, the home keeps its
// content and the wipe is not reached. Two guards answer, and the archive shows
// which one. The text compare of wipesHomeDir answers a dot or a space after
// home or after the folder above it, and the archive stays. The test of the
// entry by identity answers a path with the `\\?\` prefix and the short name of
// home, after the archive open, so the archive is gone. A Windows VM ran these
// rows with this test binary.
//
// The test is safe on a tree with no guard. The home is a temp folder, and the
// wipe is behind its seam.
func TestFilesExtractTarRefusesWindowsSpellingsOfHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "users", "a long home folder name")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(home, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	rows := []struct {
		name, destDir string
		archiveStays  bool
	}{
		{"a dot after home", home + ".", true},
		{"a space after home", home + " ", true},
		{"the prefix before home", `\\?\` + home, false},
		{"the prefix and a dot after home", `\\?\` + home + ".", false},
		{"the prefix and a space after home", `\\?\` + home + " ", false},
		{"a dot after the folder above home", filepath.Join(base, "users") + `.\` + filepath.Base(home), true},
	}
	s := newTestServer(t)
	check := func(t *testing.T, destDir string, archiveStays bool) {
		wiped = ""
		archive := oneFileArchive(t)
		got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": destDir}))
		_, statErr := os.Lstat(archive)
		guard := "the text compare (the archive stays)"
		if statErr != nil {
			guard = "after the archive open (the archive is gone)"
		}
		t.Logf("destDir=%q guard=%s frame=%s", destDir, guard, got)
		if !strings.Contains(got, `"success":false,"fileCount":0,"error":"destDir must not be or contain the home directory: `) {
			t.Errorf("frame = %s, want the home refusal", got)
		}
		if stays := statErr == nil; stays != archiveStays {
			t.Errorf("the archive stays = %v, want %v", stays, archiveStays)
		}
		if wiped != "" {
			t.Errorf("the wipe was reached for the name %q", wiped)
		}
		if b, err := os.ReadFile(keep); err != nil || string(b) != "keep\n" {
			t.Errorf("keep.txt in home = %q (err %v), want it unchanged", b, err)
		}
		if got := namesIn(t, filepath.Dir(home)); got != filepath.Base(home) {
			t.Errorf("the folder above home holds %q, want the home folder alone", got)
		}
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) { check(t, tc.destDir, tc.archiveStays) })
	}
	// This row skips if the volume makes no short name for the home folder.
	t.Run("the short name of home", func(t *testing.T) { check(t, shortPathName(t, home), false) })
}

// TestWipesHomeDirWindowsSpellings holds what the text compare answers for
// Windows spellings of a temp home folder. It refuses a dot or a space after
// home, because the path is resolved first. It lets a path with the `\\?\` prefix
// pass. A Windows VM ran these rows with this test binary.
func TestWipesHomeDirWindowsSpellings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "users", "bob")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{home, true},
		{home + "x", false},
		{home + ".", true},
		{home + " ", true},
		{`\\?\` + home, false},
		{`\\?\` + home + ".", false},
	} {
		got := wipesHomeDir(tc.path)
		t.Logf("wipesHomeDir(%q) = %v", tc.path, got)
		if got != tc.want {
			t.Errorf("wipesHomeDir(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestFilesExtractTarLastNameOfDotsAndSpaces sends a destDir whose last name is
// dots and spaces alone. The handle of the parent refuses such a name, so the
// request fails and the parent folder keeps its content. The frames are those
// of 5fd08069 (Windows VM). 89cb6289 fails at the create of the first file
// there.
//
// The wipe is the real one. The parent is a temp folder of this test, so a
// wipe that took it can reach nothing else.
func TestFilesExtractTarLastNameOfDotsAndSpaces(t *testing.T) {
	for _, name := range []string{"...", " ", ". .", "...."} {
		t.Run("the name "+name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "p")
			if err := os.Mkdir(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			n, err := extractTarGz(oneFileArchive(t), parent+`\`+name)
			if want := "clean destDir: RemoveAll " + name + ": invalid argument"; err == nil || err.Error() != want {
				t.Errorf("extractTarGz = %v, want %s", err, want)
			}
			if n != 0 {
				t.Errorf("fileCount = %d, want 0", n)
			}
			if got := namesIn(t, parent); got != "keep.txt" {
				t.Errorf("the parent holds %q, want keep.txt alone", got)
			}
		})
	}
}
