//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestFilesExtractTarNotAlreadyCompleteForOtherKinds: a plugin folder whose
// .synced is a link or a FIFO gets the normal wipe and unpack on 683cc6a9, and
// the target of the link keeps its content (Linux VM).
func TestFilesExtractTarNotAlreadyCompleteForOtherKinds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, synced, outside string)
	}{
		{"a link to a file", func(t *testing.T, synced, outside string) {
			if err := os.Symlink(outside, synced); err != nil {
				t.Fatal(err)
			}
		}},
		{"a FIFO", func(t *testing.T, synced, _ string) {
			if err := syscall.Mkfifo(synced, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			base := t.TempDir()
			outside := filepath.Join(base, "out.txt")
			writeFileForTest(t, outside, "out\n")
			dest := filepath.Join(base, "plugins", pluginDir)
			writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
			tc.stage(t, filepath.Join(dest, ".synced"), outside)

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if got := namesIn(t, dest); got != ".synced,new" {
				t.Errorf("destDir holds %q, want .synced and new", got)
			}
			if b, err := os.ReadFile(outside); err != nil || string(b) != "out\n" {
				t.Errorf("out.txt = %q (err %v), want it unchanged", b, err)
			}
		})
	}
}

// TestFilesExtractTarOldSyncedCannotBeRemoved holds the answer of 683cc6a9 for an
// old .synced that cannot go: a folder of mode 0555 with a file (Linux and macOS
// VMs). The rest of destDir stays.
func TestFilesExtractTarOldSyncedCannotBeRemoved(t *testing.T) {
	skipIfRoot(t)
	s := newTestServer(t)
	dest := filepath.Join(t.TempDir(), "plugins", pluginDir)
	writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
	writeFileForTest(t, filepath.Join(dest, ".synced", "in"), "in\n")
	chmodForRequest(t, filepath.Join(dest, ".synced"), 0o555)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
	if want := extractFailure("remove destDir's old .synced: RemoveAll .synced: permission denied"); got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if got := namesIn(t, dest); got != ".synced,keep" {
		t.Errorf("destDir holds %q, want .synced and keep", got)
	}
	if got := namesIn(t, filepath.Join(dest, ".synced")); got != "in" {
		t.Errorf(".synced holds %q, want in", got)
	}
}

// TestFilesExtractTarOldDestDirCannotBeOpened holds the answers of 683cc6a9 for
// an old destDir of mode 0000 (Linux and macOS VMs). With content the answer is
// the open failure and the content stays. An empty one goes, and the extract
// succeeds.
func TestFilesExtractTarOldDestDirCannotBeOpened(t *testing.T) {
	skipIfRoot(t)
	for _, withContent := range []bool{true, false} {
		name := "empty"
		if withContent {
			name = "with content"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t)
			dest := filepath.Join(t.TempDir(), "plugins", pluginDir)
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if withContent {
				writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
			}
			chmodForRequest(t, dest, 0o000)

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`
			if withContent {
				want = extractFailure("open the old destDir: openat " + pluginDir + ": permission denied")
			}
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if err := os.Chmod(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			wantNames := ".synced,new"
			if withContent {
				wantNames = "keep"
			}
			if got := namesIn(t, dest); got != wantNames {
				t.Errorf("destDir holds %q, want %q", got, wantNames)
			}
		})
	}
}

// TestFilesExtractTarDestDirKindsGetAPlainRemove: a destDir that is a link to a
// folder, a file or a FIFO is removed as that entry, and a new real folder gets
// the content. Nothing is wiped through the link, and the folder that it names
// keeps its content (683cc6a9 and 52d82f0a alike, Linux VM). The relative link
// names a folder in the same parent, which the handle of the parent follows.
func TestFilesExtractTarDestDirKindsGetAPlainRemove(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, dest, target string)
	}{
		{"a relative link to a folder", func(t *testing.T, dest, target string) {
			if err := os.Symlink(filepath.Base(target), dest); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file", func(t *testing.T, dest, _ string) { writeFileForTest(t, dest, "f\n") }},
		{"a FIFO", func(t *testing.T, dest, _ string) {
			if err := syscall.Mkfifo(dest, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			plugins := filepath.Join(t.TempDir(), "plugins")
			target := filepath.Join(plugins, "target")
			writeFileForTest(t, filepath.Join(target, "t.txt"), "t\n")
			writeFileForTest(t, filepath.Join(target, ".synced"), "")
			writeFileForTest(t, filepath.Join(plugins, "keep", "k"), "k\n")
			dest := filepath.Join(plugins, pluginDir)
			tc.stage(t, dest, target)

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if fi, err := os.Lstat(dest); err != nil || fi.Mode() != os.ModeDir|0o700 {
				t.Errorf("destDir is %v (err %v), want a real folder of mode 0700", fi, err)
			}
			if got := namesIn(t, dest); got != ".synced,new" {
				t.Errorf("destDir holds %q, want .synced and new", got)
			}
			if got := namesIn(t, target); got != ".synced,t.txt" {
				t.Errorf("the target holds %q, want .synced and t.txt", got)
			}
			if b, err := os.ReadFile(filepath.Join(plugins, "keep", "k")); err != nil || string(b) != "k\n" {
				t.Errorf("keep/k = %q (err %v), want it unchanged", b, err)
			}
		})
	}
}

// TestFilesExtractTarHomeGuardComesBeforeTheOldSynced: the home guard by
// identity refuses home before the step that removes an old .synced. The home
// is a temp folder, and the wipe is behind its seam.
func TestFilesExtractTarHomeGuardComesBeforeTheOldSynced(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "someone")
	writeKeepFile(t, filepath.Join(home, ".synced", "in"))
	t.Setenv(homeEnvVar(), home)
	if err := os.Symlink(filepath.Join(base, "home"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	destDir := filepath.Join(base, "alias", "someone")
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": destDir}))
	if want := extractFailure(`destDir must not be or contain the home directory: \"` + destDir + `\"`); got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if wiped != "" {
		t.Errorf("the wipe was reached for the name %q", wiped)
	}
	if got := namesIn(t, filepath.Join(home, ".synced")); got != "in" {
		t.Errorf("home/.synced holds %q, want in", got)
	}
}

// TestFilesExtractTarOldDestDirSwappedForALink: a destDir name that becomes a
// link to another folder in the parent, after the view test and before the open,
// is refused by the handle test. The folder behind the link keeps its .synced.
// The test and its text are claustrum's own, not measured. Every folder is in a
// temp folder of this test.
func TestFilesExtractTarOldDestDirSwappedForALink(t *testing.T) {
	s := newTestServer(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	sibling := filepath.Join(parent, "sibling")
	writeFileForTest(t, filepath.Join(dest, "keep"), "keep\n")
	writeFileForTest(t, filepath.Join(sibling, ".synced"), "s\n")
	t.Cleanup(func() { beforeOldDestDirOpen = func() {} })
	beforeOldDestDirOpen = func() {
		if err := os.RemoveAll(dest); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("sibling", dest); err != nil {
			t.Fatal(err)
		}
	}

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": newFileArchive(t), "destDir": dest}))
	if want := extractFailure(`open the old destDir: \"dest\" changed while it was opened`); got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(sibling, ".synced")); err != nil || string(b) != "s\n" {
		t.Errorf("sibling/.synced = %q (err %v), want it unchanged", b, err)
	}
}
