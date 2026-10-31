package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveUnreadableDestEntryGates runs the gates of the Windows step on
// every system, with the switch on and a permission error given by the test.
// Only an empty real folder and a regular file go. Each other case keeps the
// entry.
func TestRemoveUnreadableDestEntryGates(t *testing.T) {
	denied := &fs.PathError{Op: "lstat", Path: "d", Err: fs.ErrPermission}
	for _, tc := range []struct {
		name    string
		make    func(t *testing.T, p string)
		destNm  string
		viewErr error
		on      bool
		want    bool
	}{
		{"empty folder goes", mkdirOK, "d", denied, true, true},
		{"switch off", mkdirOK, "d", denied, false, false},
		{"not a permission error", mkdirOK, "d", errors.New("other"), true, false},
		{"name ends in a dot", mkdirOK, "d.", denied, true, false},
		{"folder with content", func(t *testing.T, p string) {
			mkdirOK(t, p)
			if err := os.WriteFile(filepath.Join(p, "keep"), []byte("k"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "d", denied, true, false},
		{"file goes", func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("f"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "d", denied, true, true},
		{"symbolic link to a file", func(t *testing.T, p string) {
			target := filepath.Join(filepath.Dir(p), "target")
			if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, p); err != nil {
				t.Skipf("no symbolic link here: %v", err)
			}
		}, "d", denied, true, false},
		{"gone", func(*testing.T, string) {}, "d", denied, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, tc.destNm)
			tc.make(t, p)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			goes := unreadableDestDirGoes
			t.Cleanup(func() { unreadableDestDirGoes = goes })
			unreadableDestDirGoes = tc.on

			_, before := os.Lstat(p)
			if got := removeUnreadableDestEntry(root, p, tc.destNm, tc.viewErr); got != tc.want {
				t.Fatalf("removeUnreadableDestEntry = %v, want %v", got, tc.want)
			}
			_, after := os.Lstat(p)
			if gone := before == nil && after != nil; gone != tc.want {
				t.Errorf("entry gone = %v, want %v", gone, tc.want)
			}
		})
	}
}

// irregularInfo reports the mode that it holds for an entry. An entry that is
// neither a plain folder nor a regular file stays.
type irregularInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i irregularInfo) Mode() fs.FileMode { return i.mode }
func (i irregularInfo) IsDir() bool       { return i.mode.IsDir() }

// TestRemoveUnreadableDestEntryKeepsAnIrregularEntry pins that only a plain
// folder or a regular file goes. An entry that is neither a plain folder nor a
// regular file stays, also an empty folder whose mode has the irregular bit
// (claustrum's own, not measured).
func TestRemoveUnreadableDestEntryKeepsAnIrregularEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, p string)
		mode fs.FileMode
	}{
		{"irregular file", func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("f"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, fs.ModeIrregular},
		{"empty folder with the irregular bit", mkdirOK, fs.ModeDir | fs.ModeIrregular},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "d")
			tc.make(t, p)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			goes, lstat := unreadableDestDirGoes, lstatDestByPath
			t.Cleanup(func() { unreadableDestDirGoes, lstatDestByPath = goes, lstat })
			unreadableDestDirGoes = true
			lstatDestByPath = func(name string) (fs.FileInfo, error) {
				fi, err := os.Lstat(name)
				if err != nil {
					return nil, err
				}
				return irregularInfo{fi, tc.mode}, nil
			}

			denied := &fs.PathError{Op: "lstat", Path: "d", Err: fs.ErrPermission}
			if removeUnreadableDestEntry(root, p, "d", denied) {
				t.Fatal("removeUnreadableDestEntry removed an irregular entry")
			}
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("the entry is gone: %v", err)
			}
		})
	}
}

func mkdirOK(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
}
