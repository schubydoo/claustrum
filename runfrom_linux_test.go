package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// placeFixture is a place that passes the Linux check through the seams: the
// walk starts at a test folder, the owner is this account, and the program
// that runs is the file at the place.
type placeFixture struct {
	top, srvDir, verDir, file string
}

const placeTestVersion = "0123abcd"

func newPlaceFixture(t *testing.T) placeFixture {
	t.Helper()
	top := filepath.Join(t.TempDir(), "top")
	f := placeFixture{top: top, srvDir: filepath.Join(top, "a", "srv")}
	f.verDir = filepath.Join(f.srvDir, placeTestVersion)
	f.file = filepath.Join(f.verDir, "server")
	if err := os.MkdirAll(f.verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mode bits by Chmod, so the umask of the test process does not count.
	for _, p := range []string{top, filepath.Join(top, "a"), f.srvDir, f.verDir, f.file} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldExe, oldUID, oldTop := placeExePath, placeOwnerUID, placeWalkTop
	oldFS, oldCaps := placeOnFUSE, placeHasCaps
	t.Cleanup(func() {
		placeExePath, placeOwnerUID, placeWalkTop = oldExe, oldUID, oldTop
		placeOnFUSE, placeHasCaps = oldFS, oldCaps
	})
	placeExePath = f.file
	placeOwnerUID = uint32(os.Getuid())
	placeWalkTop = top
	placeHasCaps = func(string) (bool, error) { return false, nil }
	// A temp folder on FUSE or virtiofs refuses no test.
	placeOnFUSE = func(string) (bool, error) { return false, nil }
	return f
}

func wantPlaceDenial(t *testing.T, r *placeDenial, kind, path, text string) {
	t.Helper()
	if r == nil {
		t.Fatalf("passed, want %s", kind)
	}
	if r.kind != kind || r.path != path || r.text != text {
		t.Errorf("got (%s) %s: %s\nwant (%s) %s: %s", r.kind, r.path, r.text, kind, path, text)
	}
}

func TestCheckPlaceLinuxPasses(t *testing.T) {
	f := newPlaceFixture(t)
	if r := examinePlace(f.file, placeTestVersion); r != nil {
		t.Fatalf("refused: %s", r.line())
	}
	// The path with a trailing slash passes too, after the Clean.
	if r := examineRunFrom(f.file+"/", placeTestVersion); r != nil {
		t.Fatalf("refused the cleaned path: %s", r.line())
	}
}

func TestCheckPlaceLinuxNotAbsolute(t *testing.T) {
	newPlaceFixture(t)
	wantPlaceDenial(t, examinePlace("srv/v/server", placeTestVersion), "not_absolute", "srv/v/server", notAbsoluteText)
}

func TestCheckPlaceLinuxMissing(t *testing.T) {
	f := newPlaceFixture(t)
	p := filepath.Join(f.srvDir, "v9", "server")
	wantPlaceDenial(t, examinePlace(p, placeTestVersion), "missing", p,
		"`"+filepath.Join(f.srvDir, "v9")+"`, on the way to the helper, does not exist")
}

func TestCheckPlaceLinuxWritable(t *testing.T) {
	f := newPlaceFixture(t)
	if err := os.Chmod(f.srvDir, 0o777); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "writable", f.file,
		"`"+f.srvDir+"` can be changed by accounts other than its owner (mode 0777). Remove that write access.")
	if err := os.Chmod(f.srvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.file, 0o775); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "writable", f.file,
		"`"+f.file+"` can be changed by accounts other than its owner (mode 0775). Remove that write access.")
	// A sticky folder open to all is refused too, with its sticky bit.
	if err := os.Chmod(f.file, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.top, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "writable", f.file,
		"`"+f.top+"` can be changed by accounts other than its owner (mode 1777). Remove that write access.")
}

func TestCheckPlaceLinuxOwner(t *testing.T) {
	f := newPlaceFixture(t)
	placeOwnerUID = uint32(os.Getuid()) + 1
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "owner", f.file,
		"`"+f.top+"` belongs to the account that signs in (uid "+strconv.Itoa(os.Getuid())+"). Root must own the helper and every folder above it.")
}

func TestCheckPlaceLinuxFUSE(t *testing.T) {
	f := newPlaceFixture(t)
	placeOnFUSE = func(p string) (bool, error) { return p == f.srvDir, nil }
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file,
		"`"+f.srvDir+"` is on FUSE or virtiofs, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.")
}

func TestCheckPlaceLinuxAnotherFile(t *testing.T) {
	f := newPlaceFixture(t)
	other := filepath.Join(f.top, "other")
	if err := os.WriteFile(other, []byte("y"), 0o755); err != nil {
		t.Fatal(err)
	}
	placeExePath = other
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "another_file", f.file, anotherFileText)
}

// TestCheckPlaceLinuxNotRegular pins that a folder and a FIFO at the place
// answer another_file: the program that runs is a regular file.
func TestCheckPlaceLinuxNotRegular(t *testing.T) {
	f := newPlaceFixture(t)
	dir := filepath.Join(f.top, "d", "srv", placeTestVersion, "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(f.top, "d"), filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wantPlaceDenial(t, examinePlace(dir, placeTestVersion), "another_file", dir, anotherFileText)
	fifo := filepath.Join(f.verDir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(fifo, placeTestVersion), "another_file", fifo, anotherFileText)
}

func TestCheckPlaceLinuxMode(t *testing.T) {
	f := newPlaceFixture(t)
	if err := os.Chmod(f.file, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(f.file); err != nil || fi.Mode()&os.ModeSetuid == 0 {
		t.Skipf("the temp folder keeps no setuid bit (%v)", err)
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "mode", f.file, modeText)
	if err := os.Chmod(f.file, 0o755); err != nil {
		t.Fatal(err)
	}
	placeHasCaps = func(string) (bool, error) { return true, nil }
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "mode", f.file, modeText)
	placeHasCaps = func(string) (bool, error) { return false, errors.New("boom") }
	r := examinePlace(f.file, placeTestVersion)
	if r == nil || r.kind != "cannot_tell" {
		t.Errorf("a failed capability read passed: %+v", r)
	}
}

func TestCheckPlaceLinuxShape(t *testing.T) {
	f := newPlaceFixture(t)
	shapeText := "The path must end in `srv/" + placeTestVersion + "/server`, this helper's own version, with no link among those three parts."
	// The version of another build.
	wantPlaceDenial(t, examinePlace(f.file, "ffff"), "shape", f.file,
		"The path must end in `srv/ffff/server`, this helper's own version, with no link among those three parts.")
	// A link at the file: the program that runs is the file it names.
	link := filepath.Join(f.top, "l", "srv", placeTestVersion, "server")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.file, link); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(link, placeTestVersion), "shape", link, shapeText)
	// A link at the version folder.
	dlink := filepath.Join(f.top, "d", "srv", placeTestVersion)
	if err := os.MkdirAll(filepath.Dir(dlink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.verDir, dlink); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dlink, "server")
	wantPlaceDenial(t, examinePlace(p, placeTestVersion), "shape", p, shapeText)
	// A wrong last name.
	other := filepath.Join(f.verDir, "notserver")
	if err := os.Link(f.file, other); err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, examinePlace(other, placeTestVersion), "shape", other, shapeText)
}

// TestPlaceOnFUSEReal runs the real statfs test: a temp folder is no FUSE,
// and a missing path gives an error.
func TestPlaceOnFUSEReal(t *testing.T) {
	dir := t.TempDir()
	on, err := placeOnFUSE(dir)
	if err != nil {
		t.Fatalf("statfs of a temp folder: %v", err)
	}
	if on {
		t.Skip("the temp folder is on FUSE or virtiofs")
	}
	if _, err := placeOnFUSE(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path gave no error")
	}
}

// TestPlaceHasCapsReal runs the real capability read: a plain file has none,
// also where the file system has no xattrs, and a missing path gives an error.
func TestPlaceHasCapsReal(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	caps, err := placeHasCaps(plain)
	if err != nil || caps {
		t.Fatalf("plain file: caps %v, err %v, want false and no error", caps, err)
	}
	if _, err := placeHasCaps(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path gave no error")
	}
}

// TestCheckPlaceLinuxUnreadable pins the cannot_tell text of claustrum's own
// for each entry that a test fails to read.
func TestCheckPlaceLinuxUnreadable(t *testing.T) {
	f := newPlaceFixture(t)
	// A failed stat of the program that runs.
	placeExePath = filepath.Join(f.top, "gone")
	r := examinePlace(f.file, placeTestVersion)
	if r == nil || r.kind != "cannot_tell" || !strings.HasPrefix(r.text, "`"+placeExePath+"` cannot be checked: ") {
		t.Errorf("missing exe: %+v", r)
	}
	placeExePath = f.file
	// A failed statfs of an entry.
	placeOnFUSE = func(p string) (bool, error) {
		if p == f.srvDir {
			return false, errors.New("boom")
		}
		return false, nil
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file, "`"+f.srvDir+"` cannot be checked: boom")
	placeOnFUSE = func(string) (bool, error) { return false, nil }
	// An entry with no owner.
	wantPlaceDenial(t, testPlaceEntry(f.file, f.file, noOwnerInfo{}), "cannot_tell", f.file, "`"+f.file+"` cannot be checked: no owner")
	// A failed Lstat in the shape test.
	gone := filepath.Join(f.top, "gone", "srv", placeTestVersion, "server")
	r = examineShape(gone, placeTestVersion)
	if r == nil || r.kind != "cannot_tell" || !strings.HasPrefix(r.text, "`"+gone+"` cannot be checked: ") {
		t.Errorf("shape Lstat: %+v", r)
	}
	// A failed stat of a part of the path, in a folder of mode 0.
	skipIfRoot(t)
	a := filepath.Join(f.top, "a")
	if err := os.Chmod(a, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a, 0o755) })
	r = examinePlace(f.file, placeTestVersion)
	if r == nil || r.kind != "cannot_tell" || !strings.HasPrefix(r.text, "`"+f.srvDir+"` cannot be checked: ") {
		t.Errorf("folder of mode 0: %+v", r)
	}
}

// noOwnerInfo is a FileInfo whose Sys gives no owner.
type noOwnerInfo struct{ os.FileInfo }

func (noOwnerInfo) Sys() any { return nil }

// TestCheckPlaceLinuxGoodPlaceRuns pins that a good place passes the gate
// and -check-place: no exit and no line.
func TestCheckPlaceLinuxGoodPlaceRuns(t *testing.T) {
	f := newPlaceFixture(t)
	stubOsExit(t)
	tok := filepath.Join(f.top, "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	if code, exited := catchExit(func() {
		gateRunFrom(func(s string) { got += s }, f.file, placeTestVersion, true, tok)
	}); exited || got != "" {
		t.Fatalf("gate: exit %d, %v, line %q", code, exited, got)
	}
	if _, err := os.Lstat(tok); err != nil {
		t.Errorf("token file gone for a good place: %v", err)
	}
	Version = placeTestVersion // runMainOut puts the old value back
	t.Setenv(runFromEnv, f.file)
	code, exited, out, errText := runMainOut(t, "-check-place")
	if exited || out != "" || errText != "" {
		t.Errorf("-check-place: exit %d, %v, stdout %q, stderr %q, want a return and no output", code, exited, out, errText)
	}
}
