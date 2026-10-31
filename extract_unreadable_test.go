package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveUnreadableEmptyDestDirGates runs the gates of the Windows step on
// every system, with the switch on and a permission error given by the test.
// Only an empty real folder goes. Each other case keeps the entry.
func TestRemoveUnreadableEmptyDestDirGates(t *testing.T) {
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
		{"file", func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("f"), 0o600); err != nil {
				t.Fatal(err)
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
			if got := removeUnreadableEmptyDestDir(root, p, tc.destNm, tc.viewErr); got != tc.want {
				t.Fatalf("removeUnreadableEmptyDestDir = %v, want %v", got, tc.want)
			}
			_, after := os.Lstat(p)
			if gone := before == nil && after != nil; gone != tc.want {
				t.Errorf("entry gone = %v, want %v", gone, tc.want)
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
