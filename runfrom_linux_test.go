package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	oldFS, oldCaps, oldLogin := placeFileSystem, placeHasCaps, placeLoginUID
	t.Cleanup(func() {
		placeExePath, placeOwnerUID, placeWalkTop = oldExe, oldUID, oldTop
		placeFileSystem, placeHasCaps, placeLoginUID = oldFS, oldCaps, oldLogin
	})
	placeExePath = f.file
	placeOwnerUID = uint32(os.Getuid())
	placeWalkTop = top
	placeHasCaps = func(string) (bool, error) { return false, nil }
	// A temp folder on FUSE, virtiofs or NFS refuses no test.
	placeFileSystem = func(string) (string, error) { return "", nil }
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
	placeFileSystem = func(p string) (string, error) {
		if p == f.srvDir {
			return "fuse", nil
		}
		return "", nil
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file,
		"`"+f.srvDir+"` is on FUSE or virtiofs, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.")
}

// TestCheckPlaceLinuxNFS pins the NFS text of 13047a51. It comes before the
// writable and another_file tests of the entries below it.
func TestCheckPlaceLinuxNFS(t *testing.T) {
	f := newPlaceFixture(t)
	placeFileSystem = func(p string) (string, error) {
		if strings.HasPrefix(p, f.srvDir) {
			return "nfs", nil
		}
		return "", nil
	}
	want := "`" + f.srvDir + "` is on NFS, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image."
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file, want)
	if err := os.Chmod(f.file, 0o775); err != nil {
		t.Fatal(err)
	}
	placeExePath = f.top
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file, want)
}

// TestCheckPlaceLinuxOwnerKinds pins the three owner texts of 13047a51: uid
// 65534 is cannot_tell, the account that signs in and a third account are owner.
func TestCheckPlaceLinuxOwnerKinds(t *testing.T) {
	f := newPlaceFixture(t)
	placeOwnerUID = 0
	placeLoginUID = 1000
	fi, err := os.Stat(f.file)
	if err != nil {
		t.Fatal(err)
	}
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 65534}), "cannot_tell", f.file,
		"`"+f.verDir+"` shows as uid 65534 (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the helper and every folder above it.")
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 1}), "owner", f.file,
		"`"+f.verDir+"` belongs to uid 1. Root must own the helper and every folder above it.")
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 1000}), "owner", f.file,
		"`"+f.verDir+"` belongs to the account that signs in (uid 1000). Root must own the helper and every folder above it.")
	if r := testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 0}); r != nil {
		t.Errorf("root owner refused: %s", r.line())
	}
	// The file system test of an entry comes before its owner test. The mode
	// 0775 of the file pins that it comes before the write test too.
	if err := os.Chmod(f.verDir, 0o775); err != nil {
		t.Fatal(err)
	}
	if fi, err = os.Stat(f.verDir); err != nil {
		t.Fatal(err)
	}
	placeFileSystem = func(string) (string, error) { return "nfs", nil }
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 65534}), "cannot_tell", f.file,
		"`"+f.verDir+"` is on NFS, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.")
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 0}), "cannot_tell", f.file,
		"`"+f.verDir+"` is on NFS, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.")
	placeFileSystem = func(string) (string, error) { return "fuse", nil }
	wantPlaceDenial(t, testPlaceEntry(f.file, f.verDir, ownerInfo{fi, 65534}), "cannot_tell", f.file,
		"`"+f.verDir+"` is on FUSE or virtiofs, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.")
}

// ownerInfo is a FileInfo whose Sys gives the owner uid.
type ownerInfo struct {
	os.FileInfo
	uid uint32
}

func (o ownerInfo) Sys() any {
	st := *o.FileInfo.Sys().(*syscall.Stat_t)
	st.Uid = o.uid
	return &st
}

// TestCheckPlaceLinuxStalled pins the bound of the whole check: a check past
// it is refused with the stalled text, also when it passes later, and the
// gate exits only after the check returned.
func TestCheckPlaceLinuxStalled(t *testing.T) {
	f := newPlaceFixture(t)
	oldBound := placeCheckBound
	t.Cleanup(func() { placeCheckBound = oldBound })
	placeCheckBound = 50 * time.Millisecond
	release, _, returned := stallPlaceCheck(t, f.srvDir)
	stubOsExit(t)
	lines := make(chan string, 1)
	exitDone := make(chan int, 1)
	go func() {
		code, _ := catchExit(func() {
			gateRunFrom(func(s string) { lines <- s }, f.file, placeTestVersion, false, "")
		})
		exitDone <- code
	}()
	var got string
	select {
	case got = <-lines:
	case <-time.After(10 * time.Second):
		t.Fatal("no line within 10 s of a 50 ms bound")
	}
	if want := "claustrum: refusing to run (cannot_tell) from " + f.file + ": The file could not be checked within 8 seconds. If a folder on the way to it is on a network share, check that its server is reachable.\n"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	select {
	case code := <-exitDone:
		t.Fatalf("exit %d before the stalled check returned", code)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case code := <-exitDone:
		if code != placeRefusedExit || !returned.Load() {
			t.Errorf("exit %d, check returned %v, want %d after the return", code, returned.Load(), placeRefusedExit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit after the stalled check returned")
	}
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

// TestPlaceFileSystemReal runs the real statfs test: a temp folder is no FUSE
// and no NFS, and a missing path gives an error.
func TestPlaceFileSystemReal(t *testing.T) {
	dir := t.TempDir()
	kind, err := placeFileSystem(dir)
	if err != nil {
		t.Fatalf("statfs of a temp folder: %v", err)
	}
	if kind != "" {
		t.Skipf("the temp folder is on %s", kind)
	}
	if _, err := placeFileSystem(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path gave no error")
	}
}

// TestFileSystemName pins the statfs types that the place check names.
func TestFileSystemName(t *testing.T) {
	for magic, want := range map[int64]string{unix.FUSE_SUPER_MAGIC: "fuse", unix.NFS_SUPER_MAGIC: "nfs", unix.EXT4_SUPER_MAGIC: ""} {
		if got := fileSystemName(magic); got != want {
			t.Errorf("type %#x: %q, want %q", magic, got, want)
		}
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
	if r == nil || r.kind != "cannot_tell" || !strings.HasPrefix(r.text, "`"+placeExePath+"`, on the way to the helper, cannot be checked: ") {
		t.Errorf("missing exe: %+v", r)
	}
	placeExePath = f.file
	// A failed statfs of an entry.
	placeFileSystem = func(p string) (string, error) {
		if p == f.srvDir {
			return "", errors.New("boom")
		}
		return "", nil
	}
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file, "`"+f.srvDir+"`, on the way to the helper, cannot be checked: boom")
	placeFileSystem = func(string) (string, error) { return "", nil }
	// An entry with no owner.
	wantPlaceDenial(t, testPlaceEntry(f.file, f.file, noOwnerInfo{}), "cannot_tell", f.file, "`"+f.file+"`, on the way to the helper, cannot be checked: no owner")
	// A failed Lstat in the shape test.
	gone := filepath.Join(f.top, "gone", "srv", placeTestVersion, "server")
	r = examineShape(gone, placeTestVersion)
	if r == nil || r.kind != "cannot_tell" || !strings.HasPrefix(r.text, "`"+gone+"`, on the way to the helper, cannot be checked: ") {
		t.Errorf("shape Lstat: %+v", r)
	}
	// A failed stat of a part of the path, in a folder of mode 0.
	skipIfRoot(t)
	a := filepath.Join(f.top, "a")
	if err := os.Chmod(a, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a, 0o755) })
	wantPlaceDenial(t, examinePlace(f.file, placeTestVersion), "cannot_tell", f.file,
		"`"+f.srvDir+"`, on the way to the helper, cannot be checked: permission denied")
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

// stallPlaceCheck makes the statfs of stalled block until the test calls
// release. entered closes when that call begins to wait, and returned turns
// true when it returns.
func stallPlaceCheck(t *testing.T, stalled string) (release func(), entered <-chan struct{}, returned *atomic.Bool) {
	t.Helper()
	gate := make(chan struct{})
	in := make(chan struct{})
	returned = new(atomic.Bool)
	var once, inOnce sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	placeFileSystem = func(p string) (string, error) {
		if p == stalled {
			inOnce.Do(func() { close(in) })
			<-gate
			returned.Store(true)
		}
		return "", nil
	}
	return release, in, returned
}

// TestCheckPlaceLinuxStalledModes pins that -check-place and -probe-cli print
// the stalled line and end only after the stalled check returned.
func TestCheckPlaceLinuxStalledModes(t *testing.T) {
	f := newPlaceFixture(t)
	oldBound := placeCheckBound
	t.Cleanup(func() { placeCheckBound = oldBound })
	placeCheckBound = 20 * time.Millisecond
	Version = placeTestVersion // runMainOut puts the old value back
	t.Setenv(runFromEnv, f.file)
	line := "claustrum: refusing to run (cannot_tell) from " + f.file + ": " + stalledText + "\n"
	for _, args := range [][]string{{"-check-place"}, {"-probe-cli", filepath.Join(f.top, "none")}} {
		release, entered, returned := stallPlaceCheck(t, f.srvDir)
		// The release comes well after the stall began and the bound passed,
		// so an end before the return of the check shows as returned false.
		go func() {
			<-entered
			time.Sleep(300 * time.Millisecond)
			release()
		}()
		code, exited, out, errText := runMainOut(t, args...)
		if !returned.Load() {
			t.Errorf("%v ended before the stalled check returned", args)
		}
		if errText != line {
			t.Errorf("%v: stderr %q, want %q", args, errText, line)
		}
		if args[0] == "-check-place" && (!exited || code != placeRefusedExit || out != "") {
			t.Errorf("-check-place: exit %d, %v, stdout %q, want 78 and no output", code, exited, out)
		}
		if args[0] == "-probe-cli" && (exited || out != "__CLI_BAD__\n") {
			t.Errorf("-probe-cli: exit %d, %v, stdout %q, want a return and __CLI_BAD__", code, exited, out)
		}
	}
}
