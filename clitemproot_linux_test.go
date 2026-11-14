package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/rpc"
)

func runTempRoot(t *testing.T, x, runDir string, releases *[]func()) ([]string, *rpc.CLITempRoot) {
	t.Helper()
	env, res, release, _ := privateTempRoot([]string{"TMPDIR=" + x}, runDir)
	*releases = append(*releases, release)
	return env, res
}

// The new folder ccd-<10 hex> has mode 0500 and holds t of mode 0700, and the child
// gets t (4c111e86, measured on Linux and macOS).
func TestCLITempRootGivesAFolderBesideAnUnfitEntry(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	logs := captureLogBuf(t)
	env, res := runTempRoot(t, x, runDir, &rel)
	if res == nil || res.Refused != unfitKind || !tempRootNameRE.MatchString(res.Name) || len(res.Name) != len("ccd-")+10 || res.WhyNot != "" {
		t.Fatalf("answer %+v, want refused %s and a name of 10 hex", res, unfitKind)
	}
	if v := tempEnvValue(env, "CLAUDE_CODE_TMPDIR"); v != filepath.Join(x, res.Name, "t") {
		t.Errorf("CLAUDE_CODE_TMPDIR %q, want %q", v, filepath.Join(x, res.Name, "t"))
	}
	if v := tempEnvValue(env, "TMPDIR"); v != x {
		t.Errorf("TMPDIR %q, want %q", v, x)
	}
	fi, err := os.Lstat(filepath.Join(x, res.Name))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o500 {
		t.Fatalf("new folder %v %v, want a 0500 folder", fi, err)
	}
	fi, err = os.Lstat(filepath.Join(x, res.Name, "t"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("inner folder %v %v, want a 0700 folder", fi, err)
	}
	reg := filepath.Join(runDir, tempRootRegistryName(x))
	b, err := os.ReadFile(reg)
	if err != nil || string(b) != res.Name {
		t.Fatalf("registry %q %v, want %q with no newline", b, err, res.Name)
	}
	if fi, _ := os.Stat(reg); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("registry mode %v, want 0600", fi)
	}
	if fi, err := os.Lstat(filepath.Join(x, tempEntryName())); err != nil || !fi.IsDir() {
		t.Errorf("the entry: %v %v, want it to stay", fi, err)
	}
	want := "[process.Manager] Claude Code's temp folder is unfit (it is " + unfitKind + "): Claude Code gets " + res.Name + "/t beside it\n"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want the line %q", logs.String(), want)
	}
}

// The registry name is the first 8 bytes of the SHA-256 of the boot id, "/", the
// pid namespace, a NUL and the temp dir as given.
func TestCLITempRootRegistryName(t *testing.T) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Skip(err)
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Skip(err)
	}
	got := tempRootRegistryName("/tmp/r/X")
	if !strings.HasPrefix(got, "cli-temp-root-") || len(got) != len("cli-temp-root-")+16 {
		t.Fatalf("name %q", got)
	}
	// An independent form of the same rule.
	in := strings.TrimSpace(string(boot)) + "/" + ns + string(rune(0)) + "/tmp/r/X"
	if want := "cli-temp-root-" + sha256Hex8(in); got != want {
		t.Errorf("name %q, want %q", got, want)
	}
}

func TestCLITempRootReusesItsName(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	_, a := runTempRoot(t, x, runDir, &rel)
	// A folder that this daemon holds is taken again, even with an old write time,
	// and its write time is set to now.
	old := time.Now().Add(-30 * time.Hour)
	if err := os.Chtimes(filepath.Join(x, a.Name), old, old); err != nil {
		t.Fatal(err)
	}
	_, b := runTempRoot(t, x, runDir, &rel)
	if b == nil || b.Name != a.Name {
		t.Fatalf("second answer %+v, want the name %s", b, a.Name)
	}
	if fi, _ := os.Stat(filepath.Join(x, a.Name)); fi == nil || time.Since(fi.ModTime()) > time.Hour {
		t.Errorf("write time %v, want now", fi)
	}
	// With nothing held, as in a new daemon, the registry gives the name again.
	for _, r := range rel {
		r()
	}
	rel = nil
	_, c := runTempRoot(t, x, runDir, &rel)
	if c == nil || c.Name != a.Name {
		t.Fatalf("answer after the hold %+v, want the name %s", c, a.Name)
	}
	if got := ccdFolders(t, x); len(got) != 1 {
		t.Errorf("folders %v, want one", got)
	}
}

func TestCLITempRootNewNameForAnOldFolderOrABadRegistry(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	_, a := runTempRoot(t, x, runDir, &rel)
	rel[0]()
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(x, a.Name), old, old); err != nil {
		t.Fatal(err)
	}
	_, b := runTempRoot(t, x, runDir, &rel)
	if b == nil || b.Name == a.Name || !tempRootNameRE.MatchString(b.Name) {
		t.Fatalf("answer %+v, want a new name after %s", b, a.Name)
	}
	reg := filepath.Join(runDir, tempRootRegistryName(x))
	if got, _ := os.ReadFile(reg); string(got) != b.Name {
		t.Errorf("registry %q, want %q", got, b.Name)
	}
	if _, err := os.Stat(filepath.Join(x, a.Name)); err != nil {
		t.Errorf("the old folder: %v, want it to stay", err)
	}
	rel[1]()
	if err := os.WriteFile(reg, []byte("zzz"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, c := runTempRoot(t, x, runDir, &rel)
	if c == nil || c.Name == b.Name || c.Name == a.Name {
		t.Fatalf("answer %+v after a bad registry, want a new name", c)
	}
	if got := ccdFolders(t, x); len(got) != 3 {
		t.Errorf("folders %v, want the three", got)
	}
}

// A folder that another process holds by flock is not taken. An own open file
// description stands in for that process.
func TestCLITempRootSkipsAFolderThatAnotherHolds(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	_, a := runTempRoot(t, x, runDir, &rel)
	rel[0]()
	f, err := os.Open(filepath.Join(x, a.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, b := runTempRoot(t, x, runDir, &rel)
	if b == nil || b.Name == a.Name {
		t.Fatalf("answer %+v, want a new name beside the held %s", b, a.Name)
	}
}

func TestCLITempRootCapOfEightLiveSpawns(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	logs := captureLogBuf(t)
	for i := range tempRootLiveCap {
		x, runDir := tempRootDir(t, true)
		if _, res := runTempRoot(t, x, runDir, &rel); res == nil || res.Name == "" {
			t.Fatalf("spawn %d: %+v, want a name", i+1, res)
		}
	}
	x, runDir := tempRootDir(t, true)
	env, res := runTempRoot(t, x, runDir, &rel)
	b, _ := json.Marshal(res)
	if want := `{"refused":"not readable by its owner","whyNot":"too many sessions of yours on this host have a private temp folder of their own; end one, or try again in a minute"}`; string(b) != want {
		t.Fatalf("ninth answer %s, want %s", b, want)
	}
	if tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" || len(ccdFolders(t, x)) != 0 {
		t.Errorf("the ninth got a folder")
	}
	want := "[process.Manager] Claude Code's temp folder is unfit (it is " + unfitKind + "), but no other was made: " + tempRootTooMany + "\n"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want %q", logs.String(), want)
	}
	rel[0]()
	if _, res := runTempRoot(t, x, runDir, &rel); res == nil || res.Name == "" {
		t.Fatalf("after one ended: %+v, want a name", res)
	}
}

// Spawns that share a held folder take one place of the cap: a ninth spawn in the
// same temp dir gets the folder (c9125eb8 and 4c111e86, measured on Linux). A spawn
// in another temp dir then still gets a folder of its own (claustrum's own).
func TestCLITempRootCapCountsEachFolderOnce(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	_, first := runTempRoot(t, x, runDir, &rel)
	for i := range tempRootLiveCap {
		if _, res := runTempRoot(t, x, runDir, &rel); res == nil || res.Name != first.Name {
			t.Fatalf("spawn %d: %+v, want the name %s", i+2, res, first.Name)
		}
	}
	x2, runDir2 := tempRootDir(t, true)
	if _, res := runTempRoot(t, x2, runDir2, &rel); res == nil || res.Name == "" {
		t.Fatalf("a spawn in another temp dir: %+v, want a name", res)
	}
}

func TestCLITempRootEntryKinds(t *testing.T) {
	uid := 1000
	dir := func(mode uint32) *unix.Stat_t {
		return &unix.Stat_t{Mode: unix.S_IFDIR | mode, Uid: uint32(uid), Dev: 7}
	}
	other := dir(0o700)
	other.Uid = 0
	otherFS := dir(0o700)
	otherFS.Dev = 8
	for _, c := range []struct {
		st   *unix.Stat_t
		want string
	}{
		{&unix.Stat_t{Mode: unix.S_IFLNK | 0o777, Uid: uint32(uid), Dev: 7}, "a link"},
		{&unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(uid), Dev: 7}, "not a folder"},
		{other, "another account's"},
		{otherFS, "on another file system"},
		{dir(0o300), "not readable by its owner"},
		{dir(0o700), ""},
		{dir(0o755), ""},
	} {
		if got := tempEntryUnfit(c.st, 7, uid); got != c.want {
			t.Errorf("mode %o uid %d dev %d: %q, want %q", c.st.Mode, c.st.Uid, c.st.Dev, got, c.want)
		}
	}
}

func TestCLITempRootFitOrMissingEntryAddsNothing(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	for _, mode := range []os.FileMode{0, 0o700, 0o755} {
		x, runDir := tempRootDir(t, false)
		if mode != 0 {
			if err := os.Mkdir(filepath.Join(x, tempEntryName()), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(x, tempEntryName()), mode); err != nil {
				t.Fatal(err)
			}
		}
		env, res := runTempRoot(t, x, runDir, &rel)
		if res != nil || tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" || len(ccdFolders(t, x)) != 0 {
			t.Errorf("mode %o: answer %+v, want nothing", mode, res)
		}
	}
}

// A link or a file at the entry gets refused with the stepped-around text and no
// folder (4c111e86, measured on Linux and macOS).
func TestCLITempRootLinkOrFileGetsNoFolder(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	for _, c := range []struct {
		kind string
		make func(x, e string) error
	}{
		{"a link", func(x, e string) error { return os.Symlink(x, e) }},
		{"not a folder", func(x, e string) error { return os.WriteFile(e, nil, 0o600) }},
	} {
		x, runDir := tempRootDir(t, false)
		if err := c.make(x, filepath.Join(x, tempEntryName())); err != nil {
			t.Fatal(err)
		}
		logs := captureLogBuf(t)
		env, res := runTempRoot(t, x, runDir, &rel)
		b, _ := json.Marshal(res)
		if want := `{"refused":"` + c.kind + `","whyNot":"only a folder of root's or of this account's own that others cannot write is stepped around: another account may have laid this"}`; string(b) != want {
			t.Errorf("answer %s, want %s", b, want)
		}
		if tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" || len(ccdFolders(t, x)) != 0 {
			t.Errorf("%s: a folder was given", c.kind)
		}
		if want := "[process.Manager] Claude Code's temp folder is unfit (it is " + c.kind + "), but no other was made: " + tempRootSteppedAround + "\n"; !strings.Contains(logs.String(), want) {
			t.Errorf("log %q, want %q", logs.String(), want)
		}
	}
}

func TestCLITempRootUnreadableEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0300 folder")
	}
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, false)
	e := filepath.Join(x, tempEntryName())
	if err := os.Mkdir(e, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e, 0o700) })
	if _, res := runTempRoot(t, x, runDir, &rel); res == nil || res.Refused != "not readable by its owner" || res.Name == "" {
		t.Errorf("a 0300 folder: %+v", res)
	}
}

func TestCLITempRootTempDirOthersCanWrite(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	if err := os.Chmod(x, 0o777); err != nil {
		t.Fatal(err)
	}
	logs := captureLogBuf(t)
	env, res := runTempRoot(t, x, runDir, &rel)
	b, _ := json.Marshal(res)
	if want := `{"refused":"not readable by its owner","whyNot":"the temp dir or a folder above it belongs to, or can be written by, another account"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
	if tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" || len(ccdFolders(t, x)) != 0 {
		t.Errorf("a folder was given")
	}
	if want := "[process.Manager] Claude Code's temp folder is unfit (it is " + unfitKind + "), but no other was made: " + tempRootOther + "\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want %q", logs.String(), want)
	}
	// The sticky bit makes a folder that others can write pass, as /tmp does.
	if err := os.Chmod(x, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if _, res := runTempRoot(t, x, runDir, &rel); res == nil || res.Name == "" {
		t.Errorf("sticky temp dir: %+v, want a name", res)
	}
}

func TestCLITempRootUnreadableTempDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0333 folder")
	}
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, false)
	if err := os.Chmod(x, 0o333); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(x, 0o700) })
	logs := captureLogBuf(t)
	_, res := runTempRoot(t, x, runDir, &rel)
	b, _ := json.Marshal(res)
	if want := `{"whyNot":"the temp dir or a folder above it could not be opened for reading"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
	if strings.Contains(logs.String(), "temp folder") {
		t.Errorf("log %q, want no line", logs.String())
	}
}

// setAfterMkdir runs fn between the mkdir and the test of the new folder.
func setAfterMkdir(t *testing.T, fn func(dir, name string)) {
	t.Helper()
	old := afterTempRootMkdir
	afterTempRootMkdir = fn
	t.Cleanup(func() { afterTempRootMkdir = old })
}

func wantRemovedAnswer(t *testing.T, res *rpc.CLITempRoot) {
	t.Helper()
	b, _ := json.Marshal(res)
	if want := `{"refused":"not readable by its owner","whyNot":"the new folder was not this account's own with mode 0700, and was removed"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
}

func TestCLITempRootNewFolderWithAnotherModeIsRemoved(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	setAfterMkdir(t, func(dir, name string) { _ = os.Chmod(filepath.Join(dir, name), 0o755) })
	env, res := runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x); len(got) != 0 || tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" {
		t.Errorf("folders %v, want the new folder gone and no env", got)
	}
	if _, err := os.Stat(filepath.Join(runDir, tempRootRegistryName(x))); !os.IsNotExist(err) {
		t.Errorf("registry: %v, want none", err)
	}
}

// A racer that moved the new folder and put a link at its name: the rmdir fails on
// the link, and the moved folder and the link stay.
func TestCLITempRootMovedFolderStays(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	var made string
	setAfterMkdir(t, func(dir, name string) {
		made = name
		_ = os.Rename(filepath.Join(dir, name), filepath.Join(dir, "moved-"+name))
		_ = os.Symlink(filepath.Join(dir, "t"), filepath.Join(dir, name))
	})
	_, res := runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if _, err := os.Stat(filepath.Join(x, "moved-"+made)); err != nil {
		t.Errorf("moved folder: %v, want it to stay", err)
	}
	if fi, err := os.Lstat(filepath.Join(x, made)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link at the name: %v %v, want it to stay", fi, err)
	}
}

// The remove is one rmdir: a new folder that got content stays with it, and the
// home guard keeps a new folder that is home.
func TestCLITempRootRemoveIsOneRmdirWithTheHomeGuard(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	setAfterMkdir(t, func(dir, name string) {
		_ = os.Chmod(filepath.Join(dir, name), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name, "keep"), nil, 0o600)
	})
	_, res := runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x); len(got) != 1 {
		t.Fatalf("folders %v, want the one with content", got)
	}
	x2, runDir2 := tempRootDir(t, true)
	setAfterMkdir(t, func(dir, name string) {
		_ = os.Chmod(filepath.Join(dir, name), 0o755)
		t.Setenv("HOME", filepath.Join(dir, name))
	})
	_, res = runTempRoot(t, x2, runDir2, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x2); len(got) != 1 {
		t.Errorf("folders %v, want the folder that is home to stay", got)
	}
}

func TestCLITempRootRemoveTakesOnlyItsOwnShape(t *testing.T) {
	x, _ := tempRootDir(t, false)
	if err := os.Mkdir(filepath.Join(x, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(x)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	removeMadeTempRoot(int(f.Fd()), -1, x, "other")
	if _, err := os.Stat(filepath.Join(x, "other")); err != nil {
		t.Errorf("a name of another shape: %v, want it to stay", err)
	}
}

// setAfterChildMkdir runs fn between the mkdir of t and its test.
func setAfterChildMkdir(t *testing.T, fn func(dir, name string)) {
	t.Helper()
	old := afterTempRootChildMkdir
	afterTempRootChildMkdir = fn
	t.Cleanup(func() { afterTempRootChildMkdir = old })
}

// A t that fails its test goes by one rmdir, and then the new name by one rmdir. A t
// with content stays, and so does the name then. The home guard keeps a t that is home.
func TestCLITempRootRemovesTheInnerFolderThenTheName(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, true)
	setAfterChildMkdir(t, func(dir, name string) { _ = os.Chmod(filepath.Join(dir, name, "t"), 0o755) })
	env, res := runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x); len(got) != 0 || tempEnvValue(env, "CLAUDE_CODE_TMPDIR") != "" {
		t.Errorf("folders %v, want the new folder and its t gone", got)
	}
	x, runDir = tempRootDir(t, true)
	setAfterChildMkdir(t, func(dir, name string) {
		_ = os.Chmod(filepath.Join(dir, name, "t"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name, "t", "keep"), nil, 0o600)
	})
	_, res = runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x); len(got) != 1 {
		t.Fatalf("folders %v, want the one whose t has content", got)
	} else if _, err := os.Stat(filepath.Join(x, got[0], "t", "keep")); err != nil {
		t.Errorf("the content of t: %v, want it to stay", err)
	}
	x, runDir = tempRootDir(t, true)
	setAfterChildMkdir(t, func(dir, name string) {
		_ = os.Chmod(filepath.Join(dir, name, "t"), 0o755)
		t.Setenv("HOME", filepath.Join(dir, name, "t"))
	})
	_, res = runTempRoot(t, x, runDir, &rel)
	wantRemovedAnswer(t, res)
	if got := ccdFolders(t, x); len(got) != 1 {
		t.Fatalf("folders %v, want the folder whose t is home", got)
	} else if _, err := os.Stat(filepath.Join(x, got[0], "t")); err != nil {
		t.Errorf("the t that is home: %v, want it to stay", err)
	}
}

func TestCLITempRootRefreshEveryInterval(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	old := tempRootRefresh
	tempRootRefresh = 20 * time.Millisecond
	t.Cleanup(func() { tempRootRefresh = old })
	x, runDir := tempRootDir(t, true)
	_, res := runTempRoot(t, x, runDir, &rel)
	p := filepath.Join(x, res.Name)
	back := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, back, back); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		fi, err := os.Stat(p)
		if err == nil && time.Since(fi.ModTime()) < time.Minute {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("write time %v, want a refresh", fi.ModTime())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A temp dir that does not exist gives no member.
func TestCLITempRootMissingTempDir(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, runDir := tempRootDir(t, false)
	if _, res := runTempRoot(t, filepath.Join(x, "gone"), runDir, &rel); res != nil {
		t.Fatalf("answer %+v, want none", res)
	}
}

// The temp dir is the first value that is not empty of CLAUDE_CODE_TMPDIR, TMPDIR,
// TMP and TEMP. The child gets the new folder in CLAUDE_CODE_TMPDIR.
func TestCLITempRootTempDirOrder(t *testing.T) {
	for _, c := range []struct {
		name   string
		env    func(x, gone string) []string
		folder bool
	}{
		{"CLAUDE_CODE_TMPDIR missing, TMPDIR fit", func(x, gone string) []string {
			return []string{"CLAUDE_CODE_TMPDIR=" + gone, "TMPDIR=" + x}
		}, false},
		{"CLAUDE_CODE_TMPDIR fit, TMPDIR missing", func(x, gone string) []string {
			return []string{"TMPDIR=" + gone, "CLAUDE_CODE_TMPDIR=" + x}
		}, true},
		{"TMP only", func(x, gone string) []string { return []string{"TMP=" + x} }, true},
		{"TEMP only", func(x, gone string) []string { return []string{"TEMP=" + x} }, true},
		{"TMPDIR empty, TMP fit", func(x, gone string) []string { return []string{"TMPDIR=", "TMP=" + x} }, true},
		{"TMP fit, TEMP missing", func(x, gone string) []string { return []string{"TEMP=" + gone, "TMP=" + x} }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var rel []func()
			cleanTempRoots(t, &rel)
			x, runDir := tempRootDir(t, true)
			in := c.env(x, filepath.Join(x, "gone"))
			env, res, release, _ := privateTempRoot(in, runDir)
			rel = append(rel, release)
			got := ccdFolders(t, x)
			if !c.folder {
				if res != nil || len(got) != 0 || !slices.Equal(env, in) {
					t.Fatalf("answer %+v, folders %v, env %q, want none and the env as it was", res, got, env)
				}
				return
			}
			if res == nil || res.Refused != unfitKind || len(got) != 1 || res.Name != got[0] {
				t.Fatalf("answer %+v, folders %v, want one folder and its name", res, got)
			}
			if v := tempEnvValue(env, "CLAUDE_CODE_TMPDIR"); v != filepath.Join(x, res.Name, "t") {
				t.Errorf("CLAUDE_CODE_TMPDIR %q, want %q", v, filepath.Join(x, res.Name, "t"))
			}
		})
	}
}

// After the time limit the reply says so and the child gets the env as it was. A
// folder that the step made after the limit stays, with no registry, no hold and
// no count.
func TestCLITempRootTimeLimit(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	old := tempRootLimit
	tempRootLimit = 100 * time.Millisecond
	t.Cleanup(func() { tempRootLimit = old })
	x, runDir := tempRootDir(t, true)
	stuck, free := make(chan string, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(free) }) }
	t.Cleanup(unblock)
	setAfterMkdir(t, func(dir, name string) {
		stuck <- name
		select {
		case <-free:
		case <-time.After(5 * time.Second):
		}
	})
	in := []string{"TMPDIR=" + x}
	start := time.Now()
	env, res, release, _ := privateTempRoot(in, runDir)
	rel = append(rel, release)
	b, _ := json.Marshal(res)
	if want := `{"whyNot":"the temp dir did not answer in time"}`; string(b) != want {
		t.Fatalf("answer %s, want %s", b, want)
	}
	if time.Since(start) < tempRootLimit || !slices.Equal(env, in) {
		t.Errorf("after %v env %q, want the limit and the env as it was", time.Since(start), env)
	}
	name := <-stuck
	if got := ccdFolders(t, x); len(got) != 1 || got[0] != name {
		t.Fatalf("folders %v, want the one that the step made", got)
	}
	unblock()
	// The step holds tempRoots.mu until it ends, so this lock waits for its end.
	tempRoots.mu.Lock()
	live, held := tempRoots.live, len(tempRoots.held)
	tempRoots.mu.Unlock()
	if got := ccdFolders(t, x); len(got) != 1 || got[0] != name || live != 0 || held != 0 {
		t.Fatalf("folders %v, live %d, held %d, want the late folder and no count", got, live, held)
	}
	f, err := os.Open(filepath.Join(x, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("flock of the late folder: %v, want no hold", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, tempRootRegistryName(x))); !os.IsNotExist(err) {
		t.Errorf("registry: %v, want none", err)
	}
	if fi, err := os.Lstat(filepath.Join(x, tempEntryName())); err != nil || !fi.IsDir() {
		t.Errorf("the entry: %v %v, want it to stay", fi, err)
	}
}

// The gate between the step and the limit: the first of keep and giveUp wins.
func TestCLITempRootLateGate(t *testing.T) {
	var kept tempRootLate
	if !kept.keep() || kept.giveUp() {
		t.Error("keep then giveUp: want the step to keep its folder")
	}
	var late tempRootLate
	if !late.giveUp() || late.keep() {
		t.Error("giveUp then keep: want the step to keep nothing")
	}
}
