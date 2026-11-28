package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	// A deeper top folder passes too.
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
