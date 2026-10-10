//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// extractFailure is the frame of a failed files.extract_tar with this text.
func extractFailure(text string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"` + text + `"}}`
}

// oldDestDir makes dir with mode 0755 and the old content old.txt and .synced.
func oldDestDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.txt", ".synced"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// chmodForRequest sets the mode of path for the request and gives 0755 back at the end
// of the test, so that the temp folder can go.
func chmodForRequest(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// TestFilesExtractTarParentCannotBeOpened holds the answer for a parent folder
// of destDir that the daemon cannot open for a read. The frames and the disk are
// those of 5fd08069 on Linux and macOS VMs: destDir and its old content stay, and
// no destDir is made where there was none. 89cb6289 extracts in the row with
// mode 0300 and no destDir.
func TestFilesExtractTarParentCannotBeOpened(t *testing.T) {
	skipIfRoot(t)
	for _, mode := range []os.FileMode{0o300, 0o100, 0o000} {
		for _, present := range []bool{true, false} {
			name := mode.String() + " destDir absent"
			if present {
				name = mode.String() + " destDir present"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestServer(t)
				archive := oneFileArchive(t)
				parent := filepath.Join(t.TempDir(), "x")
				dest := filepath.Join(parent, "dest")
				if err := os.Mkdir(parent, 0o755); err != nil {
					t.Fatal(err)
				}
				if present {
					oldDestDir(t, dest)
				}
				chmodForRequest(t, parent, mode)

				got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": dest}))
				if want := extractFailure("open parent: open " + parent + ": permission denied"); got != want {
					t.Errorf("frame = %s\nwant    %s", got, want)
				}
				if err := os.Chmod(parent, 0o755); err != nil {
					t.Fatal(err)
				}
				want := ""
				if present {
					want = "dest"
				}
				if got := namesIn(t, parent); got != want {
					t.Errorf("the parent holds %q, want %q", got, want)
				}
				if present {
					if got := namesIn(t, dest); got != ".synced,old.txt" {
						t.Errorf("destDir holds %q, want its old content", got)
					}
				}
			})
		}
	}
}

// TestFilesExtractTarWipeFailure holds the text of a wipe that fails, and what
// stays on disk. The text names the last name of destDir and no whole path. The
// frames and the disk are those of 5fd08069 on Linux and macOS VMs, in the three
// rows with no separator. The row with a separator after destDir is claustrum's
// own. The entries that can go are gone, and destDir is the folder that it was.
func TestFilesExtractTarWipeFailure(t *testing.T) {
	skipIfRoot(t)
	const text = "clean destDir: RemoveAll dest: permission denied"
	for _, tc := range []struct {
		name     string
		stage    func(t *testing.T, parent, dest string)
		wantLeft string // the entries of destDir afterwards
	}{
		{"a subfolder of mode 0000 with a file", func(t *testing.T, _, dest string) {
			sub := filepath.Join(dest, "s0000")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sub, "in.txt"), []byte("in\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			chmodForRequest(t, sub, 0o000)
		}, "s0000"},
		{"a parent of mode 0500", func(t *testing.T, parent, _ string) {
			chmodForRequest(t, parent, 0o500)
		}, ""},
		{"a parent of mode 0500, with a separator after destDir", func(t *testing.T, parent, _ string) {
			chmodForRequest(t, parent, 0o500)
		}, ""},
		{"a destDir of mode 0000 with content", func(t *testing.T, _, dest string) {
			chmodForRequest(t, dest, 0o000)
		}, ".synced,old.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			archive := oneFileArchive(t)
			parent := filepath.Join(t.TempDir(), "x")
			dest := filepath.Join(parent, "dest")
			oldDestDir(t, dest)
			before, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			tc.stage(t, parent, dest)

			sent := dest
			if strings.HasSuffix(tc.name, "after destDir") {
				sent += string(os.PathSeparator)
			}
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": sent}))
			if want := extractFailure(text); got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			for _, p := range []string{parent, dest} {
				if err := os.Chmod(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			// destDir was never removed, so its inode number cannot be one that
			// the file system gave out again.
			if !os.SameFile(before, after) {
				t.Error("destDir is another folder after a wipe that failed")
			}
			if got := namesIn(t, dest); got != tc.wantLeft {
				t.Errorf("destDir holds %q, want %q", got, tc.wantLeft)
			}
		})
	}
}

// TestFilesExtractTarMkdirFailure holds the text of a destDir that cannot be
// made: the frame of 5fd08069 for a parent of mode 0500 and no destDir (Linux
// and macOS VMs).
func TestFilesExtractTarMkdirFailure(t *testing.T) {
	skipIfRoot(t)
	s := newTestServer(t)
	archive := oneFileArchive(t)
	parent := filepath.Join(t.TempDir(), "x")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	chmodForRequest(t, parent, 0o500)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": filepath.Join(parent, "dest")}))
	if want := extractFailure("mkdir destDir: mkdirat dest: permission denied"); got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if got := namesIn(t, parent); got != "" {
		t.Errorf("the parent holds %q, want no entry", got)
	}
}

// TestFilesExtractTarNewFoldersHaveMode0700 holds the modes of the folders that
// the preparation makes. A destDir over an old one of mode 0777 has mode 0700,
// and each missing folder above destDir has mode 0700. The daemon umask is 0, so
// the umask gives no bit away. The mode 0700 is from 89cb6289 and 5fd08069 on
// Linux and macOS VMs. The rows with a umask of 0 are from a Linux VM, for the
// new destDir alone.
func TestFilesExtractTarNewFoldersHaveMode0700(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "m1", "m2", "dest")
	s := newTestServer(t)

	// The umask is process-wide, so this test must not run in parallel with
	// another test.
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	check := func(step string) {
		t.Helper()
		for _, p := range []string{filepath.Join(base, "m1"), filepath.Join(base, "m1", "m2"), dest} {
			fi, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode() != os.ModeDir|0o700 {
				t.Errorf("%s: %s has mode %v, want drwx------", step, p, fi.Mode())
			}
		}
	}
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`
	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if got != want {
		t.Fatalf("frame = %s, want %s", got, want)
	}
	check("new folders")

	if err := os.Chmod(dest, 0o777); err != nil {
		t.Fatal(err)
	}
	got = dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if got != want {
		t.Fatalf("frame = %s, want %s", got, want)
	}
	check("over an old destDir of mode 0777")
}

// TestFilesExtractTarWipeStaysInDestDir is the must-not-escape test of the wipe.
// A link in the old content goes as a link, a hard link loses one name, and a
// destDir that is a link is replaced by a real folder. The folder outside keeps
// its content in each row, as on 89cb6289 and 5fd08069. The two rows with no
// separator are from Linux and macOS VMs. The row with a separator is from a
// Linux VM for both builds, and from a macOS VM for 5fd08069.
// The folder outside is a temp folder of this test, so a wrong wipe can reach
// nothing else.
func TestFilesExtractTarWipeStaysInDestDir(t *testing.T) {
	asLink := func(t *testing.T, dest, outside string) {
		if err := os.Symlink(outside, dest); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, dest, outside string)
	}{
		{"links in the old content", func(t *testing.T, dest, outside string) {
			oldDestDir(t, dest)
			if err := os.Symlink(outside, filepath.Join(dest, "dirlink")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(dest, "filelink")); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(filepath.Join(outside, "keep.txt"), filepath.Join(dest, "hard.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"destDir is a link to the folder outside", asLink},
		// With a separator after the name, a call that resolves the raw path
		// follows the link.
		{"destDir is a link, with a separator after destDir", asLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			base := t.TempDir()
			outside := filepath.Join(base, "outside")
			dest := filepath.Join(base, "x", "dest")
			for _, d := range []string{filepath.Join(outside, "sub"), filepath.Dir(dest)} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range []string{"keep.txt", filepath.Join("sub", "deep.txt")} {
				if err := os.WriteFile(filepath.Join(outside, f), []byte("keep\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tc.stage(t, dest, outside)

			sent := dest
			if strings.HasSuffix(tc.name, "after destDir") {
				sent += string(os.PathSeparator)
			}
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": sent}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
				t.Errorf("frame = %s, want %s", got, want)
			}
			for _, f := range []string{"keep.txt", filepath.Join("sub", "deep.txt")} {
				if b, err := os.ReadFile(filepath.Join(outside, f)); err != nil || string(b) != "keep\n" {
					t.Errorf("outside/%s = %q (err %v), want it unchanged", f, b, err)
				}
			}
			if got := namesIn(t, outside); got != "keep.txt,sub" {
				t.Errorf("the folder outside holds %q, want keep.txt and sub", got)
			}
			if fi, err := os.Lstat(dest); err != nil || fi.Mode() != os.ModeDir|0o700 {
				t.Errorf("destDir is %v (err %v), want a real folder of mode 0700", fi, err)
			}
			if got := namesIn(t, dest); got != ".synced,a.txt" {
				t.Errorf("destDir holds %q, want the marker and a.txt", got)
			}
		})
	}
}

// TestFilesExtractTarParentCannotBeMade holds the answer for a folder above
// destDir that cannot be made. The frames are those of 89cb6289 and 5fd08069 on
// a Linux VM: the prefix is "mkdir parent: ", and the text has the whole path of
// the folder that fails. Nothing changes on disk, and an old destDir keeps its
// content.
func TestFilesExtractTarParentCannotBeMade(t *testing.T) {
	skipIfRoot(t)
	for _, tc := range []struct {
		name   string
		rel    string // destDir below the folder x
		stage  func(t *testing.T, x string)
		failAt string // the folder of the text, below x
		reason string
		keep   string // a file below x that must stay, or ""
	}{
		{"the parent is a regular file", "f.txt/dest", func(t *testing.T, x string) {
			writeKeepFile(t, filepath.Join(x, "f.txt"))
		}, "f.txt", "not a directory", "f.txt"},
		{"a folder above the parent has no search bit, destDir present", "g/p/dest", func(t *testing.T, x string) {
			writeKeepFile(t, filepath.Join(x, "g", "p", "dest", "old.txt"))
			chmodForRequest(t, filepath.Join(x, "g"), 0o600)
		}, "g/p", "permission denied", "g/p/dest/old.txt"},
		{"a folder above the parent has no search bit, destDir absent", "g/p/dest", func(t *testing.T, x string) {
			if err := os.MkdirAll(filepath.Join(x, "g", "p"), 0o755); err != nil {
				t.Fatal(err)
			}
			chmodForRequest(t, filepath.Join(x, "g"), 0o600)
		}, "g/p", "permission denied", ""},
		{"a missing parent in a folder of mode 0500", "ro/m1/dest", func(t *testing.T, x string) {
			if err := os.Mkdir(filepath.Join(x, "ro"), 0o755); err != nil {
				t.Fatal(err)
			}
			chmodForRequest(t, filepath.Join(x, "ro"), 0o500)
		}, "ro/m1", "permission denied", ""},
		{"the parent is a dangling link", "dlink/dest", func(t *testing.T, x string) {
			if err := os.Symlink("missing-target", filepath.Join(x, "dlink")); err != nil {
				t.Fatal(err)
			}
		}, "dlink", "file exists", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			x := t.TempDir()
			tc.stage(t, x)
			before := namesBelow(t, x)

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": filepath.Join(x, filepath.FromSlash(tc.rel))}))
			want := extractFailure("mkdir parent: mkdir " + filepath.Join(x, filepath.FromSlash(tc.failAt)) + ": " + tc.reason)
			if got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			for _, d := range []string{"g", "ro"} {
				_ = os.Chmod(filepath.Join(x, d), 0o755)
			}
			if after := namesBelow(t, x); after != before {
				t.Errorf("the tree changed:\nbefore %s\nafter  %s", before, after)
			}
			if tc.keep != "" {
				if b, err := os.ReadFile(filepath.Join(x, filepath.FromSlash(tc.keep))); err != nil || string(b) != "keep\n" {
					t.Errorf("%s = %q (err %v), want it unchanged", tc.keep, b, err)
				}
			}
		})
	}
}

// writeKeepFile writes a file with the content "keep" and makes its folders.
func writeKeepFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// namesBelow gives the names below dir, one per entry. It gives a folder that it
// cannot read the mode 0755 for the walk and puts the old mode back.
func namesBelow(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	var walk func(d string)
	walk = func(d string) {
		fi, err := os.Lstat(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(d, fi.Mode().Perm()) }()
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			names = append(names, p)
			if e.IsDir() {
				walk(p)
			}
		}
	}
	walk(dir)
	return strings.Join(names, ",")
}

// TestFilesExtractTarParentCannotBeSearched: a parent folder that the daemon
// can open and cannot search gives no view of destDir. The frame is the one of
// 5fd08069 for a parent of mode 0400 or 0600, with destDir present and absent
// (Linux and macOS VMs). Nothing changes on disk. 89cb6289 answers a
// "clean destDir:" text there.
func TestFilesExtractTarParentCannotBeSearched(t *testing.T) {
	skipIfRoot(t)
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		present bool
	}{
		{"mode 0400, destDir present", 0o400, true},
		{"mode 0400, destDir absent", 0o400, false},
		{"mode 0600, destDir present", 0o600, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			parent := filepath.Join(t.TempDir(), "x")
			dest := filepath.Join(parent, "dest")
			if err := os.Mkdir(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.present {
				oldDestDir(t, dest)
			}
			chmodForRequest(t, parent, tc.mode)

			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
			if want := extractFailure("open parent: open " + parent + ": permission denied"); got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.present {
				want = "dest"
				if got := namesIn(t, dest); got != ".synced,old.txt" {
					t.Errorf("destDir holds %q, want its old content", got)
				}
			}
			if got := namesIn(t, parent); got != want {
				t.Errorf("the parent holds %q, want %q", got, want)
			}
		})
	}
}

// TestFilesExtractTarRefusesHomeByIdentity holds the home guard of the entry
// that the wipe acts on. The destDir reaches the home folder, or its parent
// folder, by a spelling that the text compare of wipesHomeDir does not see: a
// link stands for a folder above. The entry at the last name is the home folder
// by identity, so the request gets the home refusal and the wipe is not reached.
// The archive is gone, because this refusal comes after its open.
//
// The test is safe on a tree with no such guard. The home is a temp folder, and
// the wipe is behind its seam.
func TestFilesExtractTarRefusesHomeByIdentity(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "someone")
	writeKeepFile(t, filepath.Join(home, "keep.txt"))
	t.Setenv(homeEnvVar(), home)
	for link, target := range map[string]string{"alias": filepath.Join(base, "home"), "top": base} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}

	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	for _, tc := range []struct{ name, destDir string }{
		{"the home folder through a linked parent", filepath.Join(base, "alias", "someone")},
		{"the parent of home through a linked folder above", filepath.Join(base, "top", "home")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wiped = ""
			archive := oneFileArchive(t)
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": archive, "destDir": tc.destDir}))
			if want := extractFailure(`destDir must not be or contain the home directory: \"` + tc.destDir + `\"`); got != want {
				t.Errorf("frame = %s\nwant    %s", got, want)
			}
			if wiped != "" {
				t.Errorf("the wipe was reached for the name %q", wiped)
			}
			if _, err := os.Lstat(archive); !os.IsNotExist(err) {
				t.Errorf("the archive still exists (err %v), want it gone", err)
			}
			if b, err := os.ReadFile(filepath.Join(home, "keep.txt")); err != nil || string(b) != "keep\n" {
				t.Errorf("keep.txt in home = %q (err %v), want it unchanged", b, err)
			}
			if got := namesIn(t, filepath.Dir(home)); got != "someone" {
				t.Errorf("the parent of home holds %q, want someone alone", got)
			}
		})
	}
}

// TestFilesExtractTarLastNameTooLong: a last name of destDir that the handle
// cannot read gets the text of a remove of that name through the handle, and
// nothing is made. The frame is the one of 5fd08069 for a name of 256 bytes
// (Linux VM). 89cb6289 answers a text with the whole path there.
func TestFilesExtractTarLastNameTooLong(t *testing.T) {
	s := newTestServer(t)
	parent := t.TempDir()
	name := strings.Repeat("n", 256)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": filepath.Join(parent, name)}))
	if want := extractFailure("clean destDir: RemoveAll " + name + ": file name too long"); got != want {
		t.Errorf("frame = %s\nwant    %s", got, want)
	}
	if got := namesIn(t, parent); got != "" {
		t.Errorf("the parent holds %q, want no entry", got)
	}
}
