package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// The kinds of a fixture entry in the cli-dir of a prune or sweep row.
const (
	pruneFile     = iota // a regular file with mode 0755
	pruneNoExec          // a regular file with mode 0644
	pruneEmptyDir        // an empty folder
	pruneFullDir         // a folder that holds one file
	pruneFileLink        // a symlink to a file outside the cli-dir, made at real time
)

// pruneFixture is one entry of the cli-dir before the install. age is how long
// before the start its mtime lies. A negative age is an mtime in the future.
type pruneFixture struct {
	name string
	kind int
	age  time.Duration
}

// The three runs of a prune row.
const (
	pruneGoodInstall   = iota // the new CLI exits 0
	pruneFailedInstall        // the new CLI exits 1
	pruneCacheHit             // the CLI of the version is there and runs
)

// pruneCLIName stands for the file name of the new CLI in a want list. It is
// "9.9.9", and "9.9.9.exe" on Windows.
const pruneCLIName = "<cli>"

const (
	pruneMin  = time.Minute
	pruneHour = time.Hour
	pruneDay  = 24 * time.Hour
)

// vf is the file name of the CLI of version v on this system.
func vf(v string) string { return v + cliExeSuffix }

// TestPruneRows runs one -install for each prune cell of 5fd08069 and compares
// the names that are left in the cli-dir. Cells L4 to L6 ran on a Linux VM, M1
// to M2b on a macOS VM and W3 to W4b on a Windows VM. The names of a version file
// carry ".exe" on Windows, as in the W cells. The CLI is this test binary in the
// "cli-stub" helper mode.
//
// SAFETY: every path is under t.TempDir, and the home variable names a folder
// there.
func TestPruneRows(t *testing.T) {
	for _, row := range []struct {
		id     string
		keep   int
		before []pruneFixture
		run    int
		want   []string
	}{
		// Only a version file counts and goes. The file 1.1.0 has mode 0644,
		// and on Windows it has no ".exe".
		{id: "L4/M1/W3", keep: 1, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, 3 * pruneHour}, {"1.1.0", pruneNoExec, 2 * pruneHour},
			{vf("0.9.0"), pruneEmptyDir, 4 * pruneHour}, {vf("0.7.0"), pruneFullDir, 4 * pruneHour},
			{"README", pruneFile, 5 * pruneHour}, {"README.exe", pruneFile, 5 * pruneHour},
			{".blob-planted", pruneFile, 1 * pruneHour}, {vf("0.8.0"), pruneFileLink, 0},
		}, want: []string{pruneCLIName, "1.1.0", vf("0.9.0"), vf("0.7.0"), "README", "README.exe", ".blob-planted", vf("0.8.0")}},
		{id: "L5/M2a/W4a", keep: 0, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, 2 * pruneHour}, {vf("1.1.0"), pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName}},
		// The new CLI always stays, also past the keep value (claustrum's own:
		// no cell has a version file newer than the new CLI).
		{id: "new CLI past the keep value", keep: 1, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, -1 * pruneHour}, {vf("1.1.0"), pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, vf("1.0.0")}},
		{id: "keep 2", keep: 2, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, 3 * pruneHour}, {vf("1.1.0"), pruneFile, 2 * pruneHour},
			{vf("1.2.0"), pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, vf("1.2.0")}},
		{id: "tie keeps name order", keep: 2, before: []pruneFixture{
			{vf("1.0.3"), pruneFile, 2 * pruneHour}, {vf("1.0.1"), pruneFile, 2 * pruneHour},
			{vf("1.0.2"), pruneFile, 2 * pruneHour},
		}, want: []string{pruneCLIName, vf("1.0.1")}},
		{id: "failed install", keep: 0, run: pruneFailedInstall, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, 2 * pruneHour},
		}, want: []string{vf("1.0.0")}},
		{id: "cache hit", keep: 0, run: pruneCacheHit, before: []pruneFixture{
			{vf("1.0.0"), pruneFile, 2 * pruneHour},
		}, want: []string{pruneCLIName, vf("1.0.0")}},
	} {
		t.Run(row.id, func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv(homeEnvVar(), f.root)
			outside := filepath.Join(f.root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			skipped := map[string]bool{}
			for _, e := range row.before {
				if !makePruneFixture(t, f.cliDir, outside, e, start) {
					t.Logf("no privilege to make the symlink %s: the link part does not run", e.name)
					skipped[e.name] = true
				}
			}
			o := installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: row.keep}
			if row.keep == 0 {
				o.cliKeep = keepZero
			}
			switch row.run {
			case pruneCacheHit:
				placeStub(t, f.cli)
			case pruneFailedInstall:
				t.Setenv("CLAUSTRUM_TEST_STUB_EXIT", "1")
				o.cliZst = f.stubBlob(t)
			default:
				o.cliZst = f.stubBlob(t)
			}

			got := captureInstallFacts(t, o)

			wantErr := ""
			if row.run == pruneFailedInstall {
				wantErr = fmt.Sprintf("installed cli at %s is not runnable", f.cli)
			}
			if got.CliError != wantErr {
				t.Errorf("cliError = %q, want %q", got.CliError, wantErr)
			}
			want := make([]string, 0, len(row.want))
			for _, n := range row.want {
				if n == pruneCLIName {
					n = filepath.Base(f.cli)
				}
				if !skipped[n] {
					want = append(want, n)
				}
			}
			if row.run == pruneFailedInstall {
				f.wantNamesAfterFailedRun(t, row.id, want...)
			} else {
				f.wantNames(t, row.id, want...)
			}
			if !isRegularFile(filepath.Join(outside, "keep.txt")) {
				t.Error("the file in the folder outside the cli-dir is gone")
			}
		})
	}
}

// A negative -cli-keep after a good install counts as 0. On 5fd08069 the run
// exits 0 with the result line and an empty stderr. The older version file
// goes and the new CLI stays (cells L6, M2b and W4b, Linux, macOS and Windows
// VMs).
func TestInstallNegativeKeep(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	makePruneFixture(t, f.cliDir, f.root, pruneFixture{vf("1.0.0"), pruneFile, pruneHour}, time.Now())
	blob := f.stubBlob(t)
	stubOsExit(t)
	errPath := filepath.Join(t.TempDir(), "stderr")
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = errFile
	var out string
	code, exited := catchExit(func() {
		out = captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob, cliKeep: -1})
	})
	os.Stderr = oldErr
	_ = errFile.Close()

	if exited {
		t.Errorf("the run exited with code %d, want no exit", code)
	}
	if want := factsHead(t, f.cli) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if got, _ := os.ReadFile(errPath); len(got) != 0 {
		t.Errorf("stderr = %q, want empty", got)
	}
	f.wantNames(t, "L6", filepath.Base(f.cli))
}

// Entries with the same mtime stay in name order. This test has forty version
// files in three groups of one mtime each, because a sort that is not stable
// keeps the order of a short list by accident. With keep 20 the 14 files of the
// newest group stay, and the first 6 names of the next group.
func TestPruneTieKeepsNameOrder(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	var names []string
	for i := 0; i < 40; i++ {
		names = append(names, vf(fmt.Sprintf("1.0.%02d", i)))
	}
	var want []string
	for i, name := range names {
		makePruneFixture(t, dir, dir, pruneFixture{name, pruneFile, time.Duration(1+i%3) * time.Hour}, start)
		if i%3 == 0 || (i%3 == 1 && i <= 16) {
			want = append(want, name)
		}
	}
	pruneCLI(dir, 20, "", true, nil)
	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the cli-dir holds %q, want %q", got, want)
	}
}

// The prune removes only regular version files, each with one plain remove. A
// keep of 0 puts every entry past the keep value. A folder with content, an
// empty folder and a link with a version name stay, and so does what the link
// points to. Nothing outside the cli-dir changes.
//
// SAFETY: every path is under t.TempDir. With a prune that removes a tree or
// follows a link, the test deletes only that fixture.
func TestPruneNeverRemovesContentOrLeavesTheCLIDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(root, "cli")
	outDir := filepath.Join(root, "outside-dir")
	outFile := filepath.Join(root, "outside-file")
	deep := filepath.Join(dir, vf("2.0.0"), "sub", "deeper")
	for _, d := range []string{home, deep, outDir, filepath.Join(dir, vf("2.0.1"))} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(homeEnvVar(), home)
	keeps := []string{filepath.Join(deep, "keep.txt"), filepath.Join(outDir, "keep.txt"), outFile}
	for _, k := range keeps {
		if err := os.WriteFile(k, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, vf("1.0.0")), []byte("old cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []string{vf("2.0.0"), vf("2.0.1")}
	for name, target := range map[string]string{vf("3.0.0"): outDir, vf("3.0.1"): outFile, vf("3.0.2"): root} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			if runtime.GOOS != "windows" {
				t.Fatal(err)
			}
			t.Logf("no privilege to make the symlink %s: the link part does not run", name)
			continue
		}
		want = append(want, name)
	}
	sort.Strings(want)

	pruneCLI(dir, 0, "", true, nil)

	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the cli-dir holds %q, want %q", got, want)
	}
	for _, k := range keeps {
		if !isRegularFile(k) {
			t.Errorf("%s is gone", k)
		}
	}
	if got := pruneNames(t, root); strings.Join(got, " ") != "cli home outside-dir outside-file" {
		t.Errorf("the folder above the cli-dir holds %q", got)
	}
}

// makePruneFixture makes one fixture entry. It answers false when a symlink
// cannot be made on Windows, which needs a privilege there.
func makePruneFixture(t *testing.T, dir, outside string, e pruneFixture, start time.Time) bool {
	t.Helper()
	p := filepath.Join(dir, e.name)
	var err error
	switch e.kind {
	case pruneFile:
		err = os.WriteFile(p, []byte("old cli"), 0o755)
	case pruneNoExec:
		err = os.WriteFile(p, []byte("old cli"), 0o644)
	case pruneEmptyDir:
		err = os.Mkdir(p, 0o755)
	case pruneFullDir:
		if err = os.Mkdir(p, 0o755); err == nil {
			err = os.WriteFile(filepath.Join(p, "inner.txt"), []byte("keep"), 0o644)
		}
	case pruneFileLink:
		if e.age != 0 {
			t.Fatalf("a link fixture cannot carry an age: %s", e.name)
		}
		target := filepath.Join(outside, "keep.txt")
		at := start.Add(-6 * pruneHour)
		if err := os.Chtimes(target, at, at); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			if runtime.GOOS == "windows" {
				return false
			}
			t.Fatal(err)
		}
		return true // the link keeps the mtime of its creation
	}
	if err != nil {
		t.Fatal(err)
	}
	// After the content is in place: the write of the inner file moved the
	// mtime of its folder.
	at := start.Add(-e.age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return true
}

// pruneNames lists the names in dir, in name order.
func pruneNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// The home guard of the tree delete refuses after the prune before the fetch.
// The cli-dir is the parent of the home folder and the version is its leaf
// name, so the CLI path is the home folder. With a keep of 0 the prune before
// the fetch keeps the newest version file. The install fails, and nothing more
// goes after the refusal.
//
// SAFETY: every path is under t.TempDir, and the home variable names a fixture
// folder there. Without the guard the test deletes only that fixture.
func TestPruneDoesNotRunAfterTheHomeGuardRefusal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "users")
	const version = "alice"
	home := installCLIPath(dir, version)
	keep := filepath.Join(home, "keep.txt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	start := time.Now()
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		makePruneFixture(t, dir, root, pruneFixture{vf(v), pruneFile, time.Duration(3-i) * pruneHour}, start)
	}
	blob := filepath.Join(root, "blob.zst")
	if err := os.WriteFile(blob, zstdOf(t, []byte("#\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	f := captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: version, cliZst: blob, cliKeep: keepZero})

	if want := fmt.Sprintf("cli path must not be or contain the home directory: %q", home); f.CliError != want {
		t.Errorf("cliError = %q, want %q", f.CliError, want)
	}
	if !isRegularFile(keep) {
		t.Error("the file in the home folder is gone")
	}
	want := []string{vf("1.2.0"), version}
	sort.Strings(want)
	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the cli-dir holds %q, want %q: nothing is removed after the refusal", got, want)
	}
	if !isRegularFile(blob) {
		t.Error("the blob was consumed")
	}
}

// cliEntryHoldsHome is the home guard of the prune and the sweeps (D2). It
// refuses the home folder and a folder that holds it, also by identity through
// a link above the home folder. Another folder and a file pass.
//
// SAFETY: the guard only answers. The test removes nothing.
func TestCLIEntryHoldsHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "users", "alice")
	other := filepath.Join(root, "users", "bob")
	for _, d := range []string{home, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(home, "1.0.0")
	if err := os.WriteFile(file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	cases := []struct {
		p    string
		want bool
	}{{home, true}, {filepath.Dir(home), true}, {other, false}, {file, false}}
	// The home folder through a link above it: a lexical test does not see it.
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "users"), link); err == nil {
		cases = append(cases, struct {
			p    string
			want bool
		}{filepath.Join(link, "alice"), true})
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	for _, c := range cases {
		fi, err := os.Lstat(c.p)
		if err != nil {
			t.Fatal(err)
		}
		if got := cliEntryHoldsHome(c.p, fi); got != c.want {
			t.Errorf("cliEntryHoldsHome(%s) = %v, want %v", c.p, got, c.want)
		}
	}
}

// TestSweepRows runs the sweep cells of 5fd08069 (L8, L9, M3a and W5, Linux,
// macOS and Windows VMs). The keep value is 20, so the prune removes nothing,
// as in the cells. Only a regular file with a sweep name goes.
func TestSweepRows(t *testing.T) {
	cells := []pruneFixture{
		{"p.zst.part", pruneFile, 8 * pruneDay}, {"1.0.0.zst.part", pruneFile, 8 * pruneDay},
		{"1.0.1.zst.part", pruneFile, 7*pruneDay - pruneHour}, {".fetch-d", pruneFile, 20 * pruneMin},
		{".fetch-123", pruneFile, 20 * pruneMin}, {"x.zst", pruneFile, 20 * pruneMin},
		{"1.0.0.zst", pruneFile, 20 * pruneMin}, {".fetch-9", pruneEmptyDir, 20 * pruneMin},
		{".fetch-e", pruneFullDir, 20 * pruneMin},
	}
	stays := []string{".fetch-9", ".fetch-d", ".fetch-e", "1.0.1.zst.part", "p.zst.part", "x.zst"}
	// A link with a sweep name.
	links := []pruneFixture{{"2.0.0.zst", pruneFileLink, 0}}
	for _, row := range []struct {
		id     string
		before []pruneFixture
		hit    bool
		want   []string
	}{
		{id: "L8/M3a/W5", before: cells, want: append([]string{pruneCLIName}, stays...)},
		{id: "L9 cache hit", before: cells, hit: true,
			want: append([]string{pruneCLIName, ".fetch-123", "1.0.0.zst"}, stays...)},
		{id: "link", before: links, want: []string{pruneCLIName, "2.0.0.zst"}},
	} {
		t.Run(row.id, func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv(homeEnvVar(), f.root)
			if err := os.WriteFile(filepath.Join(f.root, "keep.txt"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			skipped := map[string]bool{}
			for _, e := range row.before {
				if !makePruneFixture(t, f.cliDir, f.root, e, start) {
					t.Logf("no privilege to make the symlink %s: the link part does not run", e.name)
					skipped[e.name] = true
				}
			}
			o := installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 20}
			if row.hit {
				placeStub(t, f.cli)
			} else {
				o.cliZst = f.stubBlob(t)
			}
			if got := captureInstallFacts(t, o); got.CliError != "" || got.CliWasPresent != row.hit {
				t.Fatalf("cliError = %q, cliWasPresent = %v", got.CliError, got.CliWasPresent)
			}
			want := make([]string, 0, len(row.want))
			for _, n := range row.want {
				if n == pruneCLIName {
					n = filepath.Base(f.cli)
				}
				if !skipped[n] {
					want = append(want, n)
				}
			}
			f.wantNames(t, row.id, want...)
		})
	}
}

// The bound of the sweep of 7 days. 7 days and 5 s go, and 7 days less 5 s
// stay (cells L10 and M3b of 5fd08069, Linux and macOS VMs).
func TestSweepZstPartsBound(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	makePruneFixture(t, dir, dir, pruneFixture{"1.0.0.zst.part", pruneFile, 7*pruneDay - 5*time.Second}, now)
	makePruneFixture(t, dir, dir, pruneFixture{"1.0.1.zst.part", pruneFile, 7*pruneDay + 5*time.Second}, now)
	sweepZstParts(dir, now)
	if got := pruneNames(t, dir); strings.Join(got, " ") != "1.0.0.zst.part" {
		t.Errorf("the cli-dir holds %q, want 1.0.0.zst.part only", got)
	}
}
