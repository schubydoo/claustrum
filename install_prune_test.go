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

// The kinds of a fixture entry in the cli-dir of a prune row.
const (
	pruneFile        = iota // a regular file
	pruneEmptyDir           // an empty folder
	pruneFullDir            // a folder that holds one file
	pruneReadOnly           // a regular file with no write permission
	pruneReadOnlyDir        // an empty folder with no write permission
	pruneDirLink            // a symlink to a folder outside the cli-dir, made at real time
	pruneDeadLink           // a symlink to a path that does not exist, made at real time
)

// pruneEntry is one entry of the cli-dir before the install. age is how long
// before the start its mtime lies. A negative age is an mtime in the future.
type pruneEntry struct {
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

// e4321 is the fixture of rows C-1, C-3, C-4, C-13, C-14 and C-15: four empty
// folders, 4 h to 1 h old.
func e4321(kind int) []pruneEntry {
	return []pruneEntry{
		{"e1", kind, 4 * pruneHour}, {"e2", kind, 3 * pruneHour},
		{"e3", kind, 2 * pruneHour}, {"e4", kind, 1 * pruneHour},
	}
}

// TestPruneRows runs one -install for each measured C row of 89cb6289 and
// compares the names that are left in the cli-dir. "LM" rows ran on a Linux VM
// and a macOS VM (rows C-0 to C-15). "W" rows ran on a Windows VM (cells C-00
// to C-18). The rule has no part that depends on the system, so each row runs
// on every system. The one exception is the read-only folder of cell C-05.
//
// Not covered here, because the fixture needs a second process or a system
// program: the Windows cells C-06 (a junction), C-08 to C-10 and C-11b (a file
// that is open or runs). claustrum and 89cb6289 are equal in those cells.
//
// SAFETY: every path is under t.TempDir, and the home variable names a folder
// there.
func TestPruneRows(t *testing.T) {
	for _, row := range []struct {
		id      string
		keep    int
		before  []pruneEntry
		run     int
		tmpInE1 bool // TMPDIR is <cli-dir>/e1 during the run
		windows bool // the fixture exists on Windows only
		want    []string
	}{
		{id: "LM/C-0", keep: 3, want: []string{pruneCLIName}},
		{id: "LM/C-1", keep: 3, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e3", "e4"}},
		{id: "LM/C-2", keep: 3, before: e4321(pruneFullDir), want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},
		{id: "LM/C-3", keep: 5, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},
		{id: "LM/C-4", keep: 4, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e2", "e3", "e4"}},
		{id: "LM/C-5", keep: 3, before: []pruneEntry{
			{"v1", pruneFile, 4 * pruneHour}, {"v2", pruneFile, 3 * pruneHour},
			{"e3", pruneEmptyDir, 2 * pruneHour}, {"e4", pruneEmptyDir, 1 * pruneHour},
		}, want: []string{pruneCLIName, "e3", "e4"}},
		{id: "LM/C-6", keep: 2, before: []pruneEntry{
			{"e1", pruneEmptyDir, 4 * pruneHour}, {"f2", pruneFullDir, 3 * pruneHour},
			{"v3", pruneFile, 2 * pruneHour}, {"v4", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "f2", "v4"}},
		// Row C-7 has links that are 4 h and 3 h old, two files that are 2 h and
		// 1 h old, and keep 2. Go has no portable lchtimes, so the links here
		// are fresh. The two files carry a future mtime and keep is 3, so both
		// links are past the keep value as in the row.
		{id: "LM/C-7", keep: 3, before: []pruneEntry{
			{"ln", pruneDirLink, 0}, {"dl", pruneDeadLink, 0},
			{"v3", pruneFile, -2 * pruneHour}, {"v4", pruneFile, -3 * pruneHour},
		}, want: []string{pruneCLIName, "v3", "v4"}},
		{id: "LM/C-8", keep: 3, before: []pruneEntry{
			{"x.zst", pruneEmptyDir, 20 * pruneMin}, {".fetch-d", pruneEmptyDir, 20 * pruneMin},
			{"e1", pruneEmptyDir, 4 * pruneHour}, {"v2", pruneFile, 3 * pruneHour},
			{"v3", pruneFile, 2 * pruneHour}, {"v4", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "v3", "v4"}},
		{id: "LM/C-9", keep: 3, before: []pruneEntry{
			{"x.zst", pruneEmptyDir, 0}, {".fetch-d", pruneEmptyDir, 0},
			{"e1", pruneEmptyDir, 4 * pruneHour}, {"v2", pruneFile, 3 * pruneHour},
			{"v3", pruneFile, 2 * pruneHour}, {"v4", pruneFile, 1 * pruneHour},
		}, want: []string{".fetch-d", pruneCLIName, "v3", "v4", "x.zst"}},
		{id: "LM/C-10", keep: 3, before: []pruneEntry{
			{"p.zst.part", pruneEmptyDir, 8 * pruneDay}, {"q.zst.part", pruneEmptyDir, 6 * pruneDay},
		}, want: []string{pruneCLIName, "q.zst.part"}},
		{id: "LM/C-11", keep: 2, before: []pruneEntry{
			{"e1", pruneEmptyDir, -1 * pruneHour}, {"e2", pruneEmptyDir, -1 * pruneHour},
			{"v3", pruneFile, 2 * pruneHour}, {"v4", pruneFile, 1 * pruneHour},
		}, want: []string{"e1", "e2"}},
		// The Linux rows C-12 and C-12b differ in the order of creation only.
		{id: "LM/C-12 Linux", keep: 3, before: []pruneEntry{
			{"ea", pruneEmptyDir, 2 * pruneHour}, {"eb", pruneEmptyDir, 2 * pruneHour},
			{"v4", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "ea", "v4"}},
		{id: "LM/C-12b Linux", keep: 3, before: []pruneEntry{
			{"eb", pruneEmptyDir, 2 * pruneHour}, {"ea", pruneEmptyDir, 2 * pruneHour},
			{"v4", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "ea", "v4"}},
		{id: "LM/C-12 macOS", keep: 3, before: []pruneEntry{
			{"e4", pruneEmptyDir, 1 * pruneHour}, {"ta", pruneEmptyDir, 2 * pruneHour},
			{"tb", pruneEmptyDir, 2 * pruneHour},
		}, want: []string{pruneCLIName, "e4", "ta"}},
		{id: "LM/C-12b macOS", keep: 2, before: []pruneEntry{
			{"ta", pruneEmptyDir, 2 * pruneHour}, {"tb", pruneEmptyDir, 2 * pruneHour},
			{"tc", pruneEmptyDir, 2 * pruneHour},
		}, want: []string{pruneCLIName, "ta"}},
		{id: "LM/C-13", keep: 3, before: e4321(pruneEmptyDir), tmpInE1: true, want: []string{pruneCLIName, "e3", "e4"}},
		{id: "LM/C-14", keep: 3, before: e4321(pruneEmptyDir), run: pruneFailedInstall, want: []string{"e1", "e2", "e3", "e4"}},
		{id: "LM/C-15", keep: 3, before: e4321(pruneEmptyDir), run: pruneCacheHit, want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},

		{id: "W/C-00", keep: 3, want: []string{pruneCLIName}},
		{id: "W/C-01", keep: 3, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e3", "e4"}},
		{id: "W/C-02", keep: 3, before: e4321(pruneFullDir), want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},
		{id: "W/C-03k5", keep: 5, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},
		{id: "W/C-03k4", keep: 4, before: e4321(pruneEmptyDir), want: []string{pruneCLIName, "e2", "e3", "e4"}},
		{id: "W/C-04", keep: 3, before: []pruneEntry{
			{"v1.exe", pruneFile, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
			{"e3", pruneEmptyDir, 2 * pruneHour}, {"e4", pruneEmptyDir, 1 * pruneHour},
		}, want: []string{pruneCLIName, "e3", "e4"}},
		{id: "W/C-05", keep: 3, windows: true, before: []pruneEntry{
			{"e1", pruneReadOnlyDir, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
			{"v3.exe", pruneFile, 2 * pruneHour}, {"v4.exe", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "e1", "v3.exe", "v4.exe"}},
		{id: "W/C-07", keep: 3, before: []pruneEntry{
			{"v1.exe", pruneReadOnly, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
			{"v3.exe", pruneFile, 2 * pruneHour}, {"v4.exe", pruneFile, 1 * pruneHour},
		}, want: []string{pruneCLIName, "v3.exe", "v4.exe"}},
		{id: "W/C-11dir", keep: 3, before: []pruneEntry{
			{"f1", pruneEmptyDir, -1 * pruneHour}, {"f2", pruneEmptyDir, -1 * pruneHour},
			{"f3", pruneEmptyDir, -1 * pruneHour},
		}, want: []string{"f1", "f2", "f3"}},
		{id: "W/C-11file", keep: 3, before: []pruneEntry{
			{"f1", pruneFile, -1 * pruneHour}, {"f2", pruneFile, -1 * pruneHour},
			{"f3", pruneFile, -1 * pruneHour},
		}, want: []string{"f1", "f2", "f3"}},
		// Cell C-12 is the one row where claustrum differs (D18). 89cb6289
		// counts the planted ".blob-planted" file, so it removes v1.exe and
		// v2.exe (Windows VM). claustrum does not count that name, so v2.exe
		// takes the second place and stays.
		{id: "W/C-12", keep: 2, before: []pruneEntry{
			{".blob-planted", pruneFile, 1 * pruneHour},
			{"v1.exe", pruneFile, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
		}, want: []string{".blob-planted", pruneCLIName, "v2.exe"}},
		{id: "W/C-13", keep: 0, before: []pruneEntry{
			{"v1.exe", pruneFile, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
		}, want: nil},
		{id: "W/C-14k1", keep: 1, before: []pruneEntry{
			{"1.0.0", pruneFile, 4 * pruneHour}, {"1.0.0.exe", pruneFile, 3 * pruneHour},
		}, want: []string{pruneCLIName}},
		{id: "W/C-14k2", keep: 2, before: []pruneEntry{
			{"1.0.0", pruneFile, 4 * pruneHour}, {"1.0.0.exe", pruneFile, 3 * pruneHour},
		}, want: []string{"1.0.0.exe", pruneCLIName}},
		{id: "W/C-15", keep: 3, before: []pruneEntry{
			{"e4", pruneEmptyDir, 1 * pruneHour}, {"ta", pruneEmptyDir, 2 * pruneHour},
			{"tb", pruneEmptyDir, 2 * pruneHour},
		}, want: []string{pruneCLIName, "e4", "ta"}},
		{id: "W/C-16", keep: 3, before: e4321(pruneEmptyDir), run: pruneFailedInstall, want: []string{"e1", "e2", "e3", "e4"}},
		{id: "W/C-17", keep: 3, before: e4321(pruneEmptyDir), run: pruneCacheHit, want: []string{pruneCLIName, "e1", "e2", "e3", "e4"}},
		{id: "W/C-18", keep: 3, before: []pruneEntry{
			{".fetch-d", pruneEmptyDir, 20 * pruneMin}, {"x.zst", pruneEmptyDir, 20 * pruneMin},
			{".fetch-e", pruneEmptyDir, 1 * pruneMin}, {"y.zst", pruneEmptyDir, 1 * pruneMin},
			{"p.zst.part", pruneFile, 8 * pruneDay}, {"q.zst.part", pruneFile, 6 * pruneDay},
			{"e1", pruneEmptyDir, 4 * pruneHour}, {"v2.exe", pruneFile, 3 * pruneHour},
			{"v3.exe", pruneFile, 2 * pruneHour}, {"v4.exe", pruneFile, 1 * pruneHour},
		}, want: []string{".fetch-e", pruneCLIName, "q.zst.part", "v3.exe", "v4.exe", "y.zst"}},
	} {
		t.Run(row.id, func(t *testing.T) {
			if row.windows && runtime.GOOS != "windows" {
				t.Skip("the read-only attribute of a folder exists on Windows only")
			}
			root := t.TempDir()
			home := filepath.Join(root, "home")
			dir := filepath.Join(root, "cli")
			outside := filepath.Join(root, "outside")
			for _, d := range []string{home, dir, outside} {
				if err := os.Mkdir(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(homeEnvVar(), home)
			if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var full []string
			for _, e := range row.before {
				if !makePruneEntry(t, dir, outside, e, start) {
					t.Skipf("no privilege to make the symlink %s", e.name)
				}
				if e.kind == pruneFullDir {
					full = append(full, e.name)
				}
			}

			const version = "9.9.9"
			cli := installCLIPath(dir, version)
			o := installOpts{cliDir: dir, cliVersion: version, cliKeep: row.keep}
			if row.keep == 0 {
				o.cliKeep = keepZero
			}
			switch row.run {
			case pruneCacheHit:
				if err := os.WriteFile(cli, fakeCLI(t, 0), 0o755); err != nil {
					t.Fatal(err)
				}
				at := start.Add(-5 * pruneHour)
				if err := os.Chtimes(cli, at, at); err != nil {
					t.Fatal(err)
				}
			default:
				exit := 0
				if row.run == pruneFailedInstall {
					exit = 1
				}
				o.cliZst = filepath.Join(root, "blob.zst")
				if err := os.WriteFile(o.cliZst, zstdOf(t, fakeCLI(t, exit)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if row.tmpInE1 {
				t.Setenv("TMPDIR", filepath.Join(dir, "e1"))
			}

			f := captureInstallFacts(t, o)

			if f.CliPath != cli {
				t.Errorf("cliPath = %q, want %q", f.CliPath, cli)
			}
			wantErr := ""
			if row.run == pruneFailedInstall {
				wantErr = fmt.Sprintf("installed cli at %s is not runnable", cli)
			}
			if f.CliError != wantErr {
				t.Errorf("cliError = %q, want %q", f.CliError, wantErr)
			}
			if f.CliWasPresent != (row.run == pruneCacheHit) {
				t.Errorf("cliWasPresent = %v", f.CliWasPresent)
			}
			want := make([]string, 0, len(row.want))
			for _, n := range row.want {
				if n == pruneCLIName {
					n = filepath.Base(cli)
				}
				want = append(want, n)
			}
			sort.Strings(want)
			if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("the cli-dir holds %q, want %q", got, want)
			}
			for _, n := range full {
				if !isRegularFile(filepath.Join(dir, n, "inner.txt")) {
					t.Errorf("the file in the folder %s is gone", n)
				}
			}
			if !isRegularFile(filepath.Join(outside, "keep.txt")) {
				t.Error("the file in the folder outside the cli-dir is gone")
			}
		})
	}
}

// makePruneEntry makes one fixture entry. It answers false when a symlink
// cannot be made on Windows, which needs a privilege there.
func makePruneEntry(t *testing.T, dir, outside string, e pruneEntry, start time.Time) bool {
	t.Helper()
	p := filepath.Join(dir, e.name)
	var err error
	switch e.kind {
	case pruneFile, pruneReadOnly:
		err = os.WriteFile(p, []byte("old cli"), 0o755)
	case pruneEmptyDir, pruneReadOnlyDir:
		err = os.Mkdir(p, 0o755)
	case pruneFullDir:
		if err = os.Mkdir(p, 0o755); err == nil {
			err = os.WriteFile(filepath.Join(p, "inner.txt"), []byte("keep"), 0o644)
		}
	case pruneDirLink, pruneDeadLink:
		target := outside
		if e.kind == pruneDeadLink {
			target = filepath.Join(outside, "nothing-here")
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
	// The permission goes last, after the mtime is set.
	switch e.kind {
	case pruneReadOnly:
		err = os.Chmod(p, 0o444)
	case pruneReadOnlyDir:
		err = os.Chmod(p, 0o555)
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	}
	if err != nil {
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

// Entries with the same mtime stay in name order. The rows have two and three
// such entries (rows C-12 and C-12b, cell C-15). This test has forty folders in
// three groups of one mtime each, because a sort that is not stable keeps the
// order of a short list by accident. With keep 20 the 14 folders of the newest
// group stay, and the first 6 names of the next group.
func TestPruneTieKeepsNameOrder(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	var want []string
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("t%02d", i)
		p := filepath.Join(dir, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		at := start.Add(-time.Duration(1+i%3) * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 || (i%3 == 1 && i <= 16) {
			want = append(want, name)
		}
	}
	pruneCLI(dir, 20)
	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the cli-dir holds %q, want %q", got, want)
	}
}

// The prune removes with one plain remove for each entry. This test gives it a
// keep of 0, so every entry is past the keep value, and shows three things. A
// folder with content stays, at every depth. A symlink goes as a link, and what
// it points to outside the cli-dir stays. Nothing outside the cli-dir changes.
//
// SAFETY: every path is under t.TempDir. With a prune that removes a tree or
// follows a link, the test deletes only that fixture.
func TestPruneNeverRemovesContentOrLeavesTheCLIDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(root, "cli")
	outDir := filepath.Join(root, "outside-dir")
	outFile := filepath.Join(root, "outside-file")
	deep := filepath.Join(dir, "full", "sub", "deeper")
	for _, d := range []string{home, deep, outDir, filepath.Join(dir, "empty")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(homeEnvVar(), home)
	keeps := []string{
		filepath.Join(deep, "keep.txt"),
		filepath.Join(dir, "full", "keep.txt"),
		filepath.Join(outDir, "keep.txt"),
		outFile,
	}
	for _, k := range append(keeps, filepath.Join(dir, "v1")) {
		if err := os.WriteFile(k, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{"lnk-dir": outDir, "lnk-file": outFile, "lnk-up": root}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			if runtime.GOOS != "windows" {
				t.Fatal(err)
			}
			t.Logf("no privilege to make the symlink %s: the link part does not run", name)
		}
	}

	pruneCLI(dir, 0)

	if got := pruneNames(t, dir); strings.Join(got, " ") != "full" {
		t.Errorf("the cli-dir holds %q, want only the folder with content", got)
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

// The home guard of the tree delete refuses before the prune can run. The
// cli-dir is the parent of the home folder and the version is its leaf name, so
// the CLI path is the home folder. The install fails, and a failed install does
// not prune. So nothing in the cli-dir goes, with a keep of 0.
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
	for _, e := range append(e4321(pruneEmptyDir), pruneEntry{"v0", pruneFile, 5 * pruneHour}) {
		makePruneEntry(t, dir, root, e, start)
	}
	before := pruneNames(t, dir)
	blob := filepath.Join(root, "blob.zst")
	if err := os.WriteFile(blob, zstdOf(t, fakeCLI(t, 0)), 0o600); err != nil {
		t.Fatal(err)
	}

	f := captureInstallFacts(t, installOpts{cliDir: dir, cliVersion: version, cliZst: blob, cliKeep: keepZero})

	if want := fmt.Sprintf("cli path must not be or contain the home directory: %q", home); f.CliError != want {
		t.Errorf("cliError = %q, want %q", f.CliError, want)
	}
	if !isRegularFile(keep) {
		t.Error("the file in the home folder is gone")
	}
	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Errorf("the cli-dir holds %q, want %q: nothing is removed after the refusal", got, before)
	}
	if !isRegularFile(blob) {
		t.Error("the blob was consumed")
	}
}

// The prune and the sweep skip an entry that is the home folder (D2). The
// cli-dir is the parent of the home folder, so the home folder is an entry of
// it. The home folder is empty, so one plain remove takes it without the guard.
// In each case a second entry goes, which shows that the pass ran.
//
// The case "prune, home newest" shows the place of a skipped entry. The home
// folder is counted, so with keep 1 it takes the one place and the file goes.
//
// SAFETY: every path is under t.TempDir, and the home variable names an EMPTY
// fixture folder there. Without the guard the test removes only that folder.
func TestHousekeepingSkipsTheHomeFolder(t *testing.T) {
	for _, c := range []struct {
		id      string
		home    string
		homeAge time.Duration
		others  []pruneEntry
		run     func(dir string, now time.Time)
		want    []string
	}{
		{id: "prune, home oldest", home: "alice", homeAge: 5 * pruneHour,
			others: []pruneEntry{
				{"v0", pruneFile, 4 * pruneHour}, {"v1", pruneFile, 2 * pruneHour},
				{"v2", pruneFile, 1 * pruneHour},
			},
			run:  func(dir string, _ time.Time) { pruneCLI(dir, 2) },
			want: []string{"alice", "v1", "v2"}},
		{id: "prune, home newest", home: "alice", homeAge: 1 * pruneHour,
			others: []pruneEntry{{"v1", pruneFile, 2 * pruneHour}},
			run:    func(dir string, _ time.Time) { pruneCLI(dir, 1) },
			want:   []string{"alice"}},
		{id: "sweep, h.zst", home: "h.zst", homeAge: 20 * pruneMin,
			others: []pruneEntry{{"x.zst", pruneEmptyDir, 20 * pruneMin}},
			run:    sweepFetchTemps,
			want:   []string{"h.zst"}},
		{id: "sweep, h.zst.part", home: "h.zst.part", homeAge: 8 * pruneDay,
			others: []pruneEntry{{"p.zst.part", pruneEmptyDir, 8 * pruneDay}},
			run:    sweepFetchTemps,
			want:   []string{"h.zst.part"}},
	} {
		t.Run(c.id, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "users")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(homeEnvVar(), filepath.Join(dir, c.home))
			now := time.Now()
			for _, e := range append(c.others, pruneEntry{c.home, pruneEmptyDir, c.homeAge}) {
				makePruneEntry(t, dir, root, e, now)
			}

			c.run(dir, now)

			if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("the cli-dir holds %q, want %q", got, c.want)
			}
		})
	}
}
