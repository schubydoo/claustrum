//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeDriveTypes makes driveType answer from types, by drive root, for the
// rest of the test. A root that types does not name gets DRIVE_FIXED.
func fakeDriveTypes(t *testing.T, types map[string]uint32) {
	t.Helper()
	old := driveType
	driveType = func(root string) uint32 {
		if d, ok := types[root]; ok {
			return d
		}
		return windows.DRIVE_FIXED
	}
	t.Cleanup(func() { driveType = old })
}

// A UNC path and a path on a drive that GetDriveType calls DRIVE_REMOTE are
// network places. A fixed drive, a removable drive, a drive with no answer and
// a relative path are not.
func TestOnNetwork(t *testing.T) {
	fakeDriveTypes(t, map[string]uint32{
		`Z:\`: windows.DRIVE_REMOTE,
		`Y:\`: windows.DRIVE_REMOTE,
		`C:\`: windows.DRIVE_FIXED,
		`S:\`: windows.DRIVE_REMOVABLE,
		`Q:\`: windows.DRIVE_NO_ROOT_DIR,
	})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{`Z:\o\real`, true},
		{`z:\o\real`, true},
		{`Y:\x`, true},
		{`\\?\Z:\o`, true},
		{`\\localhost\c$\o\real`, true},
		{`//srv/s/x`, true},
		{`\\?\UNC\localhost\c$\o\real`, true},
		{`\\?\unc\localhost\c$\o`, true},
		{`C:\x`, false},
		{`\\?\C:\x`, false},
		{`S:\x`, false},
		{`Q:\x`, false},
		{`\\.\pipe\x`, false},
		{`real\sub`, false},
		{`\gone`, false},
	} {
		if got := onNetwork(tc.path); got != tc.want {
			t.Errorf("onNetwork(%q) = %t, want %t", tc.path, got, tc.want)
		}
	}
}

// networkLinkRoot makes root\real\f and the directory link root\lk\lz to
// root\real, and makes the drive of root a mapped network drive through the
// seam. It returns root.
func networkLinkRoot(t *testing.T) string {
	t.Helper()
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "real", "f"), "x", 0o644)
	if err := os.Mkdir(filepath.Join(root, "lk"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeDirSymlink(t, filepath.Join(root, "lk", "lz"), filepath.Join(root, "real"))
	fakeDriveTypes(t, map[string]uint32{
		strings.ToUpper(filepath.VolumeName(root)) + `\`: windows.DRIVE_REMOTE,
	})
	return root
}

// A directory link to a folder of a mapped network drive is not a directory,
// with or without junctionsAsDirs (build 683cc6a9, Windows).
func TestFilesListWindowsMappedDriveLink(t *testing.T) {
	root := networkLinkRoot(t)
	for _, extra := range []map[string]any{nil, {"junctionsAsDirs": true}} {
		if got := listIsDirOf(t, filepath.Join(root, "lk"), extra, "lz"); got["lz"] {
			t.Errorf("%v: mapped drive link isDir = true, want false", extra)
		}
	}
}

// files.realpath through a link to a mapped network drive, and of the link
// itself, gives the no_usable_name frame (build 683cc6a9, Windows).
func TestFilesRealpathWindowsMappedDriveLink(t *testing.T) {
	root := networkLinkRoot(t)
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"files.realpath: no verified path for the target (it may be deleted or moved, or on Windows on a network share)","data":{"reason":"no_usable_name"}}}`
	for _, p := range []string{filepath.Join(root, "lk", "lz", "f"), filepath.Join(root, "lk", "lz")} {
		got := dispatchRaw(t, newTestServer(t), rpcLine(t, "files.realpath", map[string]any{"path": p}))
		if got != want {
			t.Errorf("files.realpath %s = %s, want %s", p, got, want)
		}
	}
}

// files.realpath through a link to a `\\?\UNC\` path gives the no_usable_name
// frame (build 683cc6a9, Windows). It needs the administrative share of the
// drive of the temp folder.
func TestFilesRealpathWindowsUNCLink(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(root, "real", "f"), "x", 0o644)
	vol := filepath.VolumeName(root)
	if len(vol) != 2 {
		t.Skipf("the temp folder %s is not on a drive letter", root)
	}
	unc := `\\?\UNC\localhost\` + vol[:1] + `$` + root[2:] + `\real`
	makeDirSymlink(t, filepath.Join(root, "lu"), unc)
	if _, err := os.Stat(filepath.Join(root, "lu", "f")); err != nil {
		t.Skipf("the administrative share is not reachable: %v", err)
	}
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "files.realpath", map[string]any{"path": filepath.Join(root, "lu", "f")}))
	if !strings.Contains(got, `"data":{"reason":"no_usable_name"}`) {
		t.Errorf("files.realpath through %s = %s, want the no_usable_name frame", unc, got)
	}
}

// A ".." inside a link target is removed by text before the next link is
// followed: x -> L\..\dir, L -> real\sub. d\x\f1 is d\dir\f1, and d\x\f2 does
// not exist although d\real\dir\f2 does (build 683cc6a9, Windows).
func TestSocketFilesRealpathWindowsDotDotInTarget(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	d := filepath.Join(root, "d")
	writeFile(t, filepath.Join(d, "dir", "f1"), "1", 0o644)
	writeFile(t, filepath.Join(d, "real", "dir", "f2"), "2", 0o644)
	if err := os.Mkdir(filepath.Join(d, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeDirSymlink(t, filepath.Join(d, "L"), `real\sub`)
	makeDirSymlink(t, filepath.Join(d, "x"), `L\..\dir`)
	got := realpathCalls(t, root, []map[string]any{
		{"path": filepath.Join(d, "x", "f1")},
		{"path": filepath.Join(d, "x", "f2")},
	})
	for i, w := range []string{`"result":{"exists":true,"real":"<DIR>/d/dir/f1"}`, `"result":{"exists":false}`} {
		if !strings.Contains(string(got[i]), w) {
			t.Errorf("reply %d = %s, want %s", i+1, got[i], w)
		}
	}
}
