//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// mkdirFixture makes the parent folder P with a folder, a file, three links and a
// folder of mode 0555, and a link linkP to P (the fixture of c9125eb8).
func mkdirFixture(t *testing.T) (root, p string) {
	t.Helper()
	root = resolveTestRoot(t, t.TempDir())
	p = filepath.Join(root, "P")
	for _, d := range []string{"dirA", "sub", "ro"} {
		if err := os.MkdirAll(filepath.Join(p, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(p, "fileA"), "x", 0o644)
	for name, target := range map[string]string{"linkD": "dirA", "linkX": "gone", "linkF": "fileA"} {
		if err := os.Symlink(target, filepath.Join(p, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("P", filepath.Join(root, "linkP")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(p, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(p, "ro"), 0o755) })
	return root, p
}

// TestSocketFilesMkdir pins the frames of each files.mkdir code with the inputs of
// c9125eb8 (measured on Linux, most of them on macOS too), the one log line of each
// answered call, and the folders that the calls leave.
func TestSocketFilesMkdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder of mode 0555")
	}
	root, p := mkdirFixture(t)
	params := []map[string]any{
		{"parent": p, "name": "new1"},
		{"parent": p, "name": "dirA"},
		{"parent": p, "name": "fileA"},
		{"parent": p, "name": "linkD"},
		{"parent": p, "name": "linkX"},
		{"parent": p, "name": "linkF"},
		{"parent": "rel/dir", "name": "n1"},
		{"parent": ".", "name": "n1"},
		{"parent": p + "/sub/..", "name": "viadd"},
		{"parent": p + "/sub/../", "name": "viadd2"},
		{"parent": p + "/", "name": "trail"},
		{"parent": p + "//sub", "name": "dbl"},
		{"parent": p, "name": "a/b"},
		{"parent": p, "name": "tr/"},
		{"parent": p, "name": ".."},
		{"parent": p, "name": "."},
		{"parent": p, "name": ""},
		{"parent": p, "name": "a\x00b"},
		{"parent": p, "name": strings.Repeat("n", 300)},
		{"parent": p, "name": " sp ace "},
		{"parent": filepath.Join(root, "linkP"), "name": "viaLink"},
		{"parent": filepath.Join(root, "linkP") + "/", "name": "viaLink2"},
		{"parent": filepath.Join(p, "nonexist"), "name": "n"},
		{"parent": filepath.Join(p, "fileA"), "name": "n"},
		{"parent": filepath.Join(p, "ro"), "name": "n"},
		{"parent": "", "name": "n1"},
		{"parent": p + "\x00/x", "name": "n"},
		{"parent": p},
		{"name": "x"},
		{"parent": 1, "name": "x"},
		{"parent": p, "name": "x", "mode": 511},
		{"parent": p, "name": "shape_extra", "extra": 1},
		nil,
	}
	// Umask 0 lets the mode 0777 show: under umask 022 it reads as 0755. The
	// fixture above already made its folders.
	oldMask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldMask) })
	logs := captureLogBuf(t)
	cl := dial(t, startSocketServer(t))
	got := make([]json.RawMessage, len(params))
	for i, pm := range params {
		got[i] = cl.call(req(i+1, "files.mkdir", pm))
	}
	// ENAMETOOLONG is 36 on Linux (measured) and 63 on macOS. The golden holds the
	// Linux frame, so the macOS frame maps back to it. The long name is params[18].
	if syscall.ENAMETOOLONG != 36 {
		got[18] = json.RawMessage(strings.Replace(string(got[18]), fmt.Sprintf(`"errno":%d`, int(syscall.ENAMETOOLONG)), `"errno":36`, 1))
	}
	assertGolden(t, "socket_files_mkdir.golden.json", encodeGolden(t, got))

	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if i := strings.Index(l, "[Files] mkdir: "); i >= 0 {
			lines = append(lines, l[i:])
		}
	}
	errLine := func(code string, no syscall.Errno) string {
		return fmt.Sprintf("%s (error %d: %s)", code, int(no), no.Error())
	}
	want := []string{"created", "exists_folder", "exists_other", "exists_link", "exists_link", "exists_link"}
	want = append(want, repeat("parent_refused", 6)...)
	want = append(want, repeat("name_refused", 6)...)
	want = append(want, errLine("name_too_long", syscall.ENAMETOOLONG), "name_refused", "parent_elsewhere",
		"parent_refused", errLine("parent_gone", syscall.ENOENT), errLine("parent_gone", syscall.ENOTDIR),
		errLine("denied", syscall.EACCES), "parent_refused", "parent_refused", "name_refused", "parent_refused",
		"created", "created")
	for i := range want {
		want[i] = "[Files] mkdir: " + want[i]
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("log lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	var made []string
	ents, err := os.ReadDir(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		made = append(made, e.Name())
	}
	if w := "dirA fileA linkD linkF linkX new1 ro shape_extra sub x"; strings.Join(made, " ") != w {
		t.Errorf("P holds %v, want %s", made, w)
	}
	for _, name := range []string{"new1", "x", "shape_extra"} {
		fi, err := os.Stat(filepath.Join(p, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o777 {
			t.Errorf("mode of %s = %v, want 0777 under umask 0", name, fi.Mode().Perm())
		}
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
