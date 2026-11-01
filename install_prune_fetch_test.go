package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the prunes of a cache miss that be1c1ca5 runs: one before the
// fetch and one after a full disk. They were measured on Linux, macOS and
// Windows unless a comment names one system.
//
// SAFETY: every path is under t.TempDir, and the home variable names a folder
// there.

// putVersions makes the version files v in dir, oldest first, one minute
// apart, as the rows do. It returns their file names.
func putVersions(t *testing.T, dir string, v ...string) []string {
	t.Helper()
	now := time.Now()
	var names []string
	for i, n := range v {
		name := vf(n)
		at := now.Add(-time.Duration(len(v)-i) * time.Minute)
		makePruneFixture(t, dir, dir, pruneFixture{name, pruneFile, 0}, at)
		names = append(names, name)
	}
	return names
}

// wantDir compares the names in dir with want, in name order.
func wantDir(t *testing.T, row, dir string, want ...string) {
	t.Helper()
	w := slices.Clone(want)
	sort.Strings(w)
	if got := pruneNames(t, dir); strings.Join(got, " ") != strings.Join(w, " ") {
		t.Errorf("%s: the cli-dir holds %q, want %q", row, got, w)
	}
}

// The prune before the fetch keeps the newest max(keep-1, 1) version files.
// Keep 0 to 5 are measured on Linux, keep 0 and 3 on macOS and Windows. The
// install has no source, so it fails, and the facts line has no cliRemoved. A
// file outside the cli-dir stays.
func TestInstallPrunesBeforeTheFetch(t *testing.T) {
	versions := []string{"1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5", "1.0.6"}
	for _, row := range []struct{ keep, kept int }{{0, 1}, {1, 1}, {2, 1}, {3, 2}, {4, 3}, {5, 4}} {
		t.Run(fmt.Sprintf("keep %d", row.keep), func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv(homeEnvVar(), f.root)
			outside := filepath.Join(f.root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			bystander := putVersions(t, outside, "1.0.0")
			names := putVersions(t, f.cliDir, versions...)
			o := installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: row.keep}
			if row.keep == 0 {
				o.cliKeep = keepZero
			}

			out := captureInstallOutput(t, o)

			cli := installCLIPath(f.cliDir, "2.0.0")
			want := factsHead(t, cli) + `false,"cliError":"cli 2.0.0 missing and no --cli-url or --cli-zst provided"}` + "\n"
			if out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			wantDir(t, "the keep formula", f.cliDir, names[len(names)-row.kept:]...)
			wantDir(t, "the bystander", outside, bystander...)
		})
	}
}

// The prune before the fetch takes version files only, each with one plain
// remove. Measured on Linux: a 0644 version name, a link, a folder, a
// fresh .fetch-123, 2.0.0.zst and notes.txt stay, and none takes a keep place.
// With keep 3 only 1.0.1 and 1.0.2 go.
func TestInstallPruneBeforeTheFetchKeepsOtherEntries(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4")
	start := time.Now()
	var others []string
	for _, e := range []pruneFixture{
		{"1.0.5", pruneNoExec, 0}, {"2.0.0.zst", pruneNoExec, 0}, {"notes.txt", pruneNoExec, 0},
		{vf("1.0.7"), pruneFileLink, 0}, {vf("1.0.8"), pruneFullDir, 0}, {".fetch-123", pruneFile, 0},
	} {
		if !makePruneFixture(t, f.cliDir, outside, e, start) {
			t.Logf("no privilege to make the symlink %s: the link part does not run", e.name)
			continue
		}
		others = append(others, e.name)
	}

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 3})

	if want := "cli 2.0.0 missing and no --cli-url or --cli-zst provided"; got.CliError != want {
		t.Errorf("cliError = %q, want %q", got.CliError, want)
	}
	wantDir(t, "other entries", f.cliDir, append(names[2:], others...)...)
}

// The prune before the fetch runs before the request of a download: 1.0.1
// and 1.0.2 are gone when the server gets the request. A refused connection
// and a checksum mismatch prune too (all measured on Linux).
func TestInstallPrunesBeforeTheDownload(t *testing.T) {
	zst := stubZst(t)
	t.Run("a fetch that runs", func(t *testing.T) {
		f := newStubFixture(t)
		t.Setenv(homeEnvVar(), f.root)
		names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4")
		var mu sync.Mutex
		var atRequest []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ents, _ := os.ReadDir(f.cliDir)
			mu.Lock()
			for _, e := range ents {
				atRequest = append(atRequest, e.Name())
			}
			mu.Unlock()
			_, _ = w.Write(zst)
		}))
		defer srv.Close()
		sum := sha256.Sum256(zst)

		got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 3,
			cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})

		if got.CliError != "" {
			t.Fatalf("cliError = %q, want a good install", got.CliError)
		}
		mu.Lock()
		seen := strings.Join(atRequest, " ")
		mu.Unlock()
		if want := strings.Join(names[2:], " "); seen != want {
			t.Errorf("the cli-dir at the request held %q, want %q", seen, want)
		}
		wantDir(t, "a fetch that runs", f.cliDir, append(names[2:], filepath.Base(f.cli))...)
	})
	t.Run("a refused connection", func(t *testing.T) {
		f := newStubFixture(t)
		t.Setenv(homeEnvVar(), f.root)
		names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4")
		url := refusedURL(t)

		got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 3,
			cliURL: url, cliChecksum: strings.Repeat("0", 64)})

		if !strings.HasPrefix(got.CliError, "download failed: ") || got.Fetch == nil {
			t.Errorf("cliError = %q, fetch = %v, want a failed download with a fetch object", got.CliError, got.Fetch)
		}
		wantDir(t, "a refused connection", f.cliDir, names[2:]...)
	})
	t.Run("a checksum mismatch", func(t *testing.T) {
		f := newStubFixture(t)
		t.Setenv(homeEnvVar(), f.root)
		names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(zst) }))
		defer srv.Close()

		got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 3,
			cliURL: srv.URL + "/cli.zst", cliChecksum: strings.Repeat("0", 64)})

		if !strings.HasPrefix(got.CliError, "checksum mismatch: ") {
			t.Errorf("cliError = %q, want a checksum mismatch", got.CliError)
		}
		wantDir(t, "a checksum mismatch", f.cliDir, names[2:]...)
	})
}

// failCloseWith makes the close of the staging file fail with a write error
// of errno, as a full disk does. It returns a func that gives the path of the
// staging file.
func failCloseWith(t *testing.T, errno error) func() string {
	t.Helper()
	old := closeFetchTemp
	t.Cleanup(func() { closeFetchTemp = old })
	var mu sync.Mutex
	var name string
	closeFetchTemp = func(f *os.File) error {
		_ = f.Close()
		mu.Lock()
		name = f.Name()
		mu.Unlock()
		return &os.PathError{Op: "write", Path: f.Name(), Err: errno}
	}
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return name
	}
}

// After a full disk a second prune keeps one version file, whatever
// -cli-keep says, and "cliRemoved" right after cliError counts its removes.
// A count of 0 omits it. Keep 5 with three versions gives cliRemoved 2
// (Linux, macOS, Windows). With six versions keep 1 and 2 print no
// cliRemoved and keep 10 prints 5 (Linux). A user quota gives the same
// (Linux, Windows). The filler and the blob stay. The error number is each
// one of diskFullErrors.
func TestInstallPrunesAfterAFullDisk(t *testing.T) {
	six := []string{"1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5", "1.0.6"}
	for _, errno := range diskFullErrors {
		for _, row := range []struct {
			id       string
			keep     int
			versions []string
			removed  int
		}{
			{"keep 5 of three", 5, []string{"1.0.1", "1.0.2", "1.0.3"}, 2},
			{"keep 1 of six", 1, six, 0},
			{"keep 2 of six", 2, six, 0},
			{"keep 10 of six", 10, six, 5},
		} {
			t.Run(fmt.Sprintf("%d/%s", errno, row.id), func(t *testing.T) {
				f := newStubFixture(t)
				t.Setenv(homeEnvVar(), f.root)
				names := putVersions(t, f.cliDir, row.versions...)
				filler := filepath.Join(f.cliDir, "filler")
				if err := os.WriteFile(filler, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				blob := f.stubBlob(t)
				staged := failCloseWith(t, errno)

				out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: row.keep, cliZst: blob})

				cli := installCLIPath(f.cliDir, "2.0.0")
				cliErr := (&os.PathError{Op: "write", Path: staged(), Err: errno}).Error()
				want := factsHead(t, cli) + `false,"cliError":` + jsonString(t, "decompressing: "+cliErr)
				if row.removed > 0 {
					want += fmt.Sprintf(`,"cliRemoved":%d`, row.removed)
				}
				want += "}\n"
				if out != want {
					t.Errorf("stdout\n got %q\nwant %q", out, want)
				}
				wantDir(t, row.id, f.cliDir, names[len(names)-1], "filler")
				if !isRegularFile(blob) {
					t.Error("the blob was consumed")
				}
			})
		}
	}
}

// A full disk during a download takes the second prune too, and cliRemoved
// comes right before the fetch object. Linux measured it with a user quota. The other error numbers follow the code.
func TestInstallPrunesAfterAFullDiskOfADownload(t *testing.T) {
	zst := stubZst(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(zst) }))
	defer srv.Close()
	for _, errno := range diskFullErrors {
		t.Run(fmt.Sprintf("%d", errno), func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv(homeEnvVar(), f.root)
			names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")
			failCloseWith(t, errno)

			out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 5,
				cliURL: srv.URL + "/cli.zst", cliChecksum: strings.Repeat("0", 64)})

			if !strings.Contains(out, `,"cliRemoved":2,"fetch":{`) {
				t.Errorf("stdout %q, want cliRemoved 2 right before the fetch object", out)
			}
			wantDir(t, "a full disk of a download", f.cliDir, names[2])
		})
	}
}

// A file of the named version in the cli-dir stays and takes no keep place in
// the prune before the fetch. Measured on Linux: with a random 0755 2.0.0,
// keep 3 and no source, only 1.0.1 and 1.0.2 go.
func TestInstallPruneBeforeTheFetchSkipsTheNamedVersion(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4")
	named := installCLIPath(f.cliDir, "2.0.0")
	if err := os.WriteFile(named, []byte("random bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 3})

	if want := "cli 2.0.0 missing and no --cli-url or --cli-zst provided"; got.CliError != want {
		t.Errorf("cliError = %q, want %q", got.CliError, want)
	}
	wantDir(t, "the named version", f.cliDir, names[2], names[3], filepath.Base(named))
}

// Another error of the staging step takes no second prune. With keep 5 and
// three version files nothing goes, and there is no cliRemoved.
func TestInstallOtherErrorTakesNoSecondPrune(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")
	failCloseWith(t, errors.New("input/output error"))

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 5, cliZst: f.stubBlob(t)})

	if got.CliError == "" || got.CliRemoved != 0 {
		t.Errorf("cliError = %q, cliRemoved = %d, want an error and no count", got.CliError, got.CliRemoved)
	}
	wantDir(t, "another error", f.cliDir, names...)
}

// Both prunes of a cache miss skip the -cli-zst blob, also when it sits in the
// cli-dir under a version name with an exec bit (claustrum's own guard, as in
// the sweeps). The blob 1.0.0 is the oldest entry. With keep 1 the prune before
// the fetch keeps 1.0.3. A staging error then fails the install, so the
// blob is not consumed. After a full disk the second prune has nothing left
// to remove.
func TestInstallPrunesSkipTheBlob(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("full disk %v", full), func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv(homeEnvVar(), f.root)
			blob := filepath.Join(f.cliDir, vf("1.0.0"))
			if err := os.WriteFile(blob, stubZst(t), 0o755); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(blob, old, old); err != nil {
				t.Fatal(err)
			}
			names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")
			if full {
				failCloseWith(t, diskFullErrors[0])
			} else {
				failCloseWith(t, errors.New("input/output error"))
			}

			got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 1, cliZst: blob})

			if got.CliError == "" || got.CliRemoved != 0 {
				t.Errorf("cliError = %q, cliRemoved = %d, want an error and no count", got.CliError, got.CliRemoved)
			}
			wantDir(t, "the blob", f.cliDir, names[2], filepath.Base(blob))
		})
	}
}

// A version that D6 refuses prunes nothing: the refusal comes first.
func TestInstallPruneBeforeTheFetchWaitsForD6(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "a/b", cliKeep: 1})

	if want := validateCLIVersion("a/b"); want == nil || got.CliError != want.Error() {
		t.Errorf("cliError = %q, want the D6 refusal %v", got.CliError, want)
	}
	wantDir(t, "a D6 refusal", f.cliDir, names...)
}

// After a full disk a non-runnable file of the named version stays and takes
// no keep place, so the newest older version stays too (claustrum's own, not
// measured).
func TestInstallFullDiskSkipsTheNamedVersion(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")
	named := installCLIPath(f.cliDir, "2.0.0")
	if err := os.WriteFile(named, []byte("random bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	failCloseWith(t, diskFullErrors[0])

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 5, cliZst: f.stubBlob(t)})

	if got.CliRemoved != 2 {
		t.Errorf("cliRemoved = %d, want 2 (cliError %q)", got.CliRemoved, got.CliError)
	}
	wantDir(t, "the named version after a full disk", f.cliDir, names[2], filepath.Base(named))
}

// The lowest int as -cli-keep keeps one file in the prune before the fetch,
// as a negative keep does, and does not wrap (claustrum's own). runInstall
// runs directly here, because the capture helpers map that value to 0.
func TestInstallPruneBeforeTheFetchLowestKeep(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3")
	out, err := os.Create(filepath.Join(f.root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = out
	runInstall(installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: math.MinInt})
	os.Stdout = old
	_ = out.Close()

	wantDir(t, "the lowest keep", f.cliDir, names[2])
}
