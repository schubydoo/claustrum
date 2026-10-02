package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the rules of the 89cb6289 -install measurement (Linux, macOS and
// Windows VMs). They cover the 30 s and 120 s bounds of the direct
// `--version` run and the disk state after a stop. They also cover the order of
// the steps of a first install and the default cli folder. The stand-in CLI is this test binary in the "cli-stub"
// helper mode (helperproc_test.go). No test waits for a production bound: each
// one shrinks cliRunBound and cliFirstRunBound.

// stubFixture is one cell: a root folder, a cli folder in it and the stub log.
type stubFixture struct {
	root, cliDir, cli string
	log, childPID     string
}

// newStubFixture makes the cli folder and points the stub at its log. Every CLI
// that the test starts runs in the "cli-stub" mode.
func newStubFixture(t *testing.T) *stubFixture {
	t.Helper()
	root := t.TempDir()
	f := &stubFixture{
		root:     root,
		cliDir:   filepath.Join(root, "cli"),
		log:      filepath.Join(root, "stub.log"),
		childPID: filepath.Join(root, "child.pid"),
	}
	f.cli = installCLIPath(f.cliDir, "9.9.9")
	if err := os.Mkdir(f.cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUSTRUM_TEST_HELPER", "cli-stub")
	t.Setenv("CLAUSTRUM_TEST_STUB_LOG", f.log)
	t.Setenv("CLAUSTRUM_TEST_STUB_CHILDPID", f.childPID)
	for _, k := range []string{"MS", "EXIT", "CHILD", "IGNORETERM"} {
		t.Setenv("CLAUSTRUM_TEST_STUB_"+k, "")
	}
	// No launcher gate from the environment of the test run.
	t.Setenv(managedLauncherGateEnv, "")
	return f
}

// stubDelay sets how long the stub sleeps before it exits.
func stubDelay(t *testing.T, d time.Duration) {
	t.Helper()
	t.Setenv("CLAUSTRUM_TEST_STUB_MS", strconv.FormatInt(d.Milliseconds(), 10))
}

// placeStub puts the stub at path: a symlink to this test binary, or a copy on
// Windows, where a symlink needs a privilege.
func placeStub(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(exe, path); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

var (
	stubZstOnce  sync.Once
	stubZstBytes []byte
	stubZstErr   error
)

// stubZst is this test binary as a zstd blob, the -cli-zst and -cli-url source
// of a stub. It is compressed once per test run.
func stubZst(t *testing.T) []byte {
	t.Helper()
	stubZstOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			stubZstErr = err
			return
		}
		b, err := os.ReadFile(exe)
		if err != nil {
			stubZstErr = err
			return
		}
		stubZstBytes = zstdOf(t, b)
	})
	if stubZstErr != nil {
		t.Fatal(stubZstErr)
	}
	return stubZstBytes
}

// stubBlob writes the stub blob beside the cli folder and returns its path.
func (f *stubFixture) stubBlob(t *testing.T) string {
	t.Helper()
	p := filepath.Join(f.root, "blob.zst")
	if err := os.WriteFile(p, stubZst(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubOldVersions are the four older CLI files of a cell, oldest first.
var stubOldVersions = []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"}

// litter fills the cli folder as the VM cells do. It makes an old pair of swept
// names (11 minutes old), a new pair (fresh) and four old versions.
func (f *stubFixture) litter(t *testing.T) {
	t.Helper()
	now := time.Now()
	put := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(f.cliDir, name)
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	put(".fetch-old", 11*time.Minute)
	put("old.zst", 11*time.Minute)
	put(".fetch-new", 0)
	put("new.zst", 0)
	for i, v := range stubOldVersions {
		put(v+cliExeSuffix, time.Duration(48-i)*time.Hour)
	}
}

// names is the sorted list of the cli folder.
func (f *stubFixture) names(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(f.cliDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// wantNames compares the cli folder with a list of names.
func (f *stubFixture) wantNames(t *testing.T, row string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := f.names(t); !slices.Equal(got, want) {
		t.Errorf("%s: cli folder = %v, want %v", row, got, want)
	}
}

// oldVersionFiles is the file names of the four old versions on this system.
func oldVersionFiles() []string {
	var out []string
	for _, v := range stubOldVersions {
		out = append(out, v+cliExeSuffix)
	}
	return out
}

// stubLogLine returns the first line of the stub log with the given prefix. The
// second result is how many such lines the log holds.
func (f *stubFixture) stubLogLine(t *testing.T, prefix string) (string, int) {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	first, n := "", 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			if n == 0 {
				first = l
			}
			n++
		}
	}
	return first, n
}

// setCLIRunBounds shrinks the two bounds of the direct run for one test.
func setCLIRunBounds(t *testing.T, hit, first time.Duration) {
	t.Helper()
	oldHit, oldFirst := cliRunBound, cliFirstRunBound
	cliRunBound, cliFirstRunBound = hit, first
	t.Cleanup(func() { cliRunBound, cliFirstRunBound = oldHit, oldFirst })
}

// factsHead is the facts line up to and with `"cliWasPresent":`.
func factsHead(t *testing.T, cliPath string) string {
	t.Helper()
	return `__INSTALL_RESULT__{"serverVersion":` + jsonString(t, Version) + `,"os":"` + runtime.GOOS +
		`","arch":"` + runtime.GOARCH + `","libc":"` + detectLibc() + `","cliPath":` + jsonString(t, cliPath) +
		`,"cliWasPresent":`
}

// stoppedTail is the end of the facts line of a stopped direct run.
const stoppedTail = `false,"cliError":"cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)","cliUnresponsive":true`

// Rows B04, B05, B10, B15. A present CLI that does not answer
// is stopped at the cache-hit bound. The whole stdout is one facts line with
// cliWasPresent false, the fixed text and cliUnresponsive. Then only the old
// swept pair is gone. The CLI, the fresh pair, the old versions and the -cli-zst
// blob all stay, and -cli-keep prunes nothing.
func TestInstallStopsAPresentCLIAtTheCacheHitBound(t *testing.T) {
	f := newStubFixture(t)
	f.litter(t)
	placeStub(t, f.cli)
	blob := f.stubBlob(t)
	stubDelay(t, 20*time.Second)
	// The first-run bound is far away, so a run that takes the wrong bound is not
	// stopped inside this test's limit.
	setCLIRunBounds(t, 500*time.Millisecond, time.Minute)

	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob, cliKeep: 2})
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the run took %v, want about the 500ms bound", d)
	}
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	f.wantNames(t, "B04/B10", append(oldVersionFiles(), ".fetch-new", "new.zst", filepath.Base(f.cli))...)
	if !isRegularFile(blob) {
		t.Error("B10: the -cli-zst blob was consumed. A stopped cache hit installs nothing")
	}
}

// Row B11. A stopped cache hit sends no request to the -cli-url server.
func TestInstallStoppedCacheHitDownloadsNothing(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	stubDelay(t, 20*time.Second)
	setCLIRunBounds(t, 500*time.Millisecond, time.Minute)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("never asked for"))
	}))
	defer srv.Close()

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliURL: srv.URL + "/cli.zst", cliChecksum: "x"})
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q (no fetch key, no progress line)", out, want)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the server got %d requests, want 0", n)
	}
}

// The passing side (row B03). A present CLI that answers under the bound is
// present, and a good cache hit sweeps nothing.
func TestInstallPresentCLIUnderTheBoundIsPresent(t *testing.T) {
	f := newStubFixture(t)
	f.litter(t)
	placeStub(t, f.cli)
	stubDelay(t, 300*time.Millisecond)
	setCLIRunBounds(t, 30*time.Second, time.Minute)

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 2})
	if want := factsHead(t, f.cli) + "true}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	f.wantNames(t, "B03", append(oldVersionFiles(), ".fetch-new", ".fetch-old", "new.zst", "old.zst", filepath.Base(f.cli))...)
}

// Rows F04, F05, F15. The two bounds are two values. A CLI that answers
// between them passes right after its install and is stopped on the cache hit of
// the next run.
func TestInstallFirstRunHasItsOwnBound(t *testing.T) {
	f := newStubFixture(t)
	stubDelay(t, 2*time.Second)
	setCLIRunBounds(t, 400*time.Millisecond, 30*time.Second)

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	if want := factsHead(t, f.cli) + "false}\n"; out != want {
		t.Errorf("F04: stdout\n got %q\nwant %q", out, want)
	}
	if !isRegularFile(f.cli) {
		t.Fatal("F04: the CLI was not installed")
	}

	out = captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("the cache hit of the same CLI: stdout\n got %q\nwant %q", out, want)
	}
}

// Rows F05, F07, F15. A new CLI that does not answer is stopped
// at the first-run bound, with the same text. The new CLI stays at its final name,
// the -cli-zst blob is consumed, the old swept pair is gone and nothing is pruned.
func TestInstallStopsANewCLIAtTheFirstRunBound(t *testing.T) {
	f := newStubFixture(t)
	f.litter(t)
	blob := f.stubBlob(t)
	stubDelay(t, 20*time.Second)
	// The cache-hit bound is far away, so a run that takes the wrong bound is not
	// stopped inside this test's limit.
	setCLIRunBounds(t, time.Minute, 500*time.Millisecond)

	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob, cliKeep: 2})
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the run took %v, want about the 500ms bound", d)
	}
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	f.wantNames(t, "F05", append(oldVersionFiles(), ".fetch-new", "new.zst", filepath.Base(f.cli))...)
	if !isRegularFile(f.cli) {
		t.Error("F05: the new CLI must stay at its final name")
	}
	if _, err := os.Lstat(blob); !os.IsNotExist(err) {
		t.Errorf("F05: the blob must be consumed, Lstat err = %v", err)
	}
}

// Row F14. A file that does not run sits at the final name, and the blob
// holds a slow CLI. The run of the new CLI gets the first-run bound. The class of
// the bound follows the install in this run, not a file that was there before.
func TestInstallOverAFailingFileUsesTheFirstRunBound(t *testing.T) {
	f := newStubFixture(t)
	if err := os.WriteFile(f.cli, []byte("not a cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubDelay(t, 1500*time.Millisecond)
	setCLIRunBounds(t, 300*time.Millisecond, 30*time.Second)

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	if want := factsHead(t, f.cli) + "false}\n"; out != want {
		t.Errorf("F14: stdout\n got %q\nwant %q", out, want)
	}
	if fi, err := os.Stat(f.cli); err != nil || fi.Size() < 1024 {
		t.Errorf("F14: the new CLI did not replace the old file (err %v)", err)
	}
}

// The clock of the bound starts before the process start call. Measured
// on a Windows VM against 89cb6289: the wall time of a stopped run is 30.01 to
// 30.06 s whatever the start delay of the CLI is. The seam makes the start call
// take 3 s. With a 4 s bound the stop comes about 4 s after the call, not 7 s.
// The real start of the stub counts toward the bound too, so the window leaves
// 2.9 s for it and for the stop.
func TestRunCLIVersionClockStartsBeforeTheStartCall(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	stubDelay(t, 20*time.Second)
	old := startCLIRun
	startCLIRun = func(cmd *exec.Cmd) error {
		time.Sleep(3 * time.Second)
		return old(cmd)
	}
	t.Cleanup(func() { startCLIRun = old })

	start := time.Now()
	got := runCLIVersion(f.cli, 4*time.Second)
	elapsed := time.Since(start)
	if got != cliRunStopped {
		t.Fatalf("result %d, want cliRunStopped", got)
	}
	// A clock that starts after the start call stops at 7.0 s or later.
	if elapsed < 4*time.Second || elapsed > 6900*time.Millisecond {
		t.Errorf("the stop came after %v, want about 4s: the start call counts toward the bound", elapsed)
	}
}

// claustrum does not close its idle connections after the download, so the
// connection stays open after the body until the transport's 90 s idle limit.
// On a macOS VM 89cb6289 and claustrum both close it 90.0 to 90.1 s after the
// last body byte (cells F08b, L08b, L09b). The CLI of this test runs for 1 s, so
// the test server sees no closed connection until the install step has returned.
func TestInstallKeepsTheDownloadConnectionOpenDuringTheRun(t *testing.T) {
	f := newStubFixture(t)
	stubDelay(t, time.Second)
	zst := stubZst(t)
	sum := sha256.Sum256(zst)
	var active, closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateActive:
			active.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9",
		cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
	if strings.Contains(out, "cliError") || !isRegularFile(f.cli) {
		t.Fatalf("stdout %q, want a good install", out)
	}
	if active.Load() == 0 {
		t.Fatal("the server saw no active connection")
	}
	if n := closed.Load(); n != 0 {
		t.Errorf("the server saw %d closed connections before the install step returned, want 0", n)
	}
}

// The home guard of the tree delete at the final name (D2). The reference removes
// a folder at <cli-dir>/<version> as a tree, also when it is the home folder
// (cell H1) or contains it (cell H5). claustrum refuses both: the folder is not
// removed, the blob stays and no CLI runs. With the home folder elsewhere the
// same folder is replaced, as before (cell H3). The cell names are the Linux
// ones. docs/DIVERGENCES.md D2 lists the cells of the three systems.
//
// SAFETY: every path is under t.TempDir, and the home variable names a fixture
// folder there. Without the guard the test deletes only that fixture.
func TestInstallRefusesToReplaceTheHomeFolder(t *testing.T) {
	for _, tc := range []struct {
		cell           string
		version        string // the leaf name that -cli-version gives
		homeIsElswhere bool
		refused        bool
	}{
		{"H1: the final name is the home folder", "alice", false, true},
		{"H5: the final name contains the home folder", "users", false, true},
		{"H3: the home folder is elsewhere", "alice", true, false},
	} {
		t.Run(tc.cell, func(t *testing.T) {
			f := newStubFixture(t)
			// On Windows the final name carries the suffix, so the fixture folder
			// of that name carries it too.
			users := filepath.Join(f.root, "cell", "users")
			alice := filepath.Join(users, "alice")
			cliDir := users
			switch tc.version {
			case "alice":
				alice = filepath.Join(users, "alice"+cliExeSuffix)
			case "users":
				users = filepath.Join(f.root, "cell", "users"+cliExeSuffix)
				alice = filepath.Join(users, "alice")
				cliDir = filepath.Join(f.root, "cell")
			}
			keeps := []string{filepath.Join(alice, "keep1.txt"), filepath.Join(alice, "docs", "keep2.txt")}
			for _, k := range keeps {
				if err := os.MkdirAll(filepath.Dir(k), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(k, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			home := alice
			if tc.homeIsElswhere {
				home = filepath.Join(f.root, "otherhome")
				if err := os.Mkdir(home, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(homeEnvVar(), home)
			blob := f.stubBlob(t)
			cli := installCLIPath(cliDir, tc.version)
			facts := captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: tc.version, cliZst: blob})
			if facts.CliPath != cli {
				t.Errorf("cliPath = %q, want %q", facts.CliPath, cli)
			}
			_, runs := f.stubLogLine(t, "START ")
			if !tc.refused {
				if facts.CliError != "" || !isRegularFile(cli) || runs != 1 {
					t.Errorf("CliError %q, CLI file %v, runs %d: want the folder replaced by the CLI", facts.CliError, isRegularFile(cli), runs)
				}
				return
			}
			if want := fmt.Sprintf("cli path must not be or contain the home directory: %q", cli); facts.CliError != want {
				t.Errorf("CliError = %q, want %q", facts.CliError, want)
			}
			for _, k := range keeps {
				if !isRegularFile(k) {
					t.Errorf("%s was deleted", k)
				}
			}
			if !isRegularFile(blob) {
				t.Error("the blob was consumed")
			}
			if runs != 0 {
				t.Errorf("a CLI ran %d times, want 0", runs)
			}
			assertNoStagingLeftover(t, cli)
		})
	}
}

// Row F13. A download before the run is outside the bound, so a download
// that takes longer than the bound does not use it up.
func TestInstallDownloadTimeIsOutsideTheBound(t *testing.T) {
	f := newStubFixture(t)
	stubDelay(t, 100*time.Millisecond)
	const serverDelay = 5 * time.Second
	setCLIRunBounds(t, time.Minute, serverDelay-500*time.Millisecond)
	zst := stubZst(t)
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		time.Sleep(serverDelay)
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9",
		cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
	if d := time.Since(start); d < serverDelay {
		t.Fatalf("the run took %v, under the %v download. The fixture did not pass the bound", d, serverDelay)
	}
	line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
	if !regexp.MustCompile(`"cliWasPresent":false,"fetch":\{"bytes":\d+,"ms":\d+,"longestPauseMs":\d+\}\}\n$`).MatchString(line) {
		t.Errorf("F13: facts line %q, want no cliError", line)
	}
}

// Row B14. A CLI that exits 0 and leaves a process that holds its stdout
// and stderr answers at once. The run waits for the CLI, not for its output.
func TestInstallDoesNotWaitForAHolderOfTheCLIOutput(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	t.Setenv("CLAUSTRUM_TEST_STUB_CHILD", "hold")
	setCLIRunBounds(t, 20*time.Second, time.Minute)
	t.Cleanup(func() { killStubChild(f.childPID) })

	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the run took %v. It waited for the holder of the output (30s)", d)
	}
	if want := factsHead(t, f.cli) + "true}\n"; out != want {
		t.Errorf("B14: stdout\n got %q\nwant %q", out, want)
	}
}

// killStubChild ends the child that a stub recorded in its pid file, if any.
func killStubChild(pidFile string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
		// On Windows the wait lets the process release its executable, which is
		// in the temp folder of the test. Elsewhere it returns at once.
		_, _ = p.Wait()
	}
}

// Rows F01, F02. With nothing at the final name, the new CLI
// runs at its final path. At that run its folder holds the CLI, the fresh pair
// and the old versions. It holds no `.fetch-` temp of this install, no download
// blob and no old swept pair. The prune follows the good run.
func TestInstallNewCLIRunsAtItsFinalName(t *testing.T) {
	zst := stubZst(t)
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	defer srv.Close()
	for _, source := range []string{"-cli-zst", "-cli-url"} {
		t.Run(source, func(t *testing.T) {
			f := newStubFixture(t)
			f.litter(t)
			o := installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 2}
			if source == "-cli-zst" {
				o.cliZst = f.stubBlob(t)
			} else {
				o.cliURL, o.cliChecksum = srv.URL+"/cli.zst", hex.EncodeToString(sum[:])
			}
			out := captureInstallOutput(t, o)
			line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
			if !strings.HasPrefix(line, factsHead(t, f.cli)+"false") || strings.Contains(line, "cliError") {
				t.Fatalf("facts line %q, want a good install", line)
			}
			start, n := f.stubLogLine(t, "START ")
			if n != 1 || !strings.HasPrefix(start, "START argv0="+f.cli+" ") {
				t.Errorf("stub START lines: %d, first %q, want one run with argv[0] %s", n, start, f.cli)
			}
			dir, _ := f.stubLogLine(t, "DIR ")
			wantDir := append(oldVersionFiles(), ".fetch-new", "new.zst", filepath.Base(f.cli))
			slices.Sort(wantDir)
			if want := "DIR " + strings.Join(wantDir, ","); dir != want {
				t.Errorf("the folder at the --version run\n got %q\nwant %q", dir, want)
			}
			// -cli-keep 2 keeps the new CLI and the newest old version.
			f.wantNames(t, "F01 prune", ".fetch-new", "new.zst", "1.3.0"+cliExeSuffix, filepath.Base(f.cli))
		})
	}
}

// Rows F12, G05. A new CLI that exits non-zero is at its final name when
// it runs, and it is gone after the run. The text names the final path. The blob
// is consumed, the old swept pair is gone and nothing is pruned.
func TestInstallRemovesANewCLIThatDoesNotRun(t *testing.T) {
	f := newStubFixture(t)
	f.litter(t)
	blob := f.stubBlob(t)
	t.Setenv("CLAUSTRUM_TEST_STUB_EXIT", "1")

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob, cliKeep: 2})
	want := factsHead(t, f.cli) + `false,"cliError":` + jsonString(t, "installed cli at "+f.cli+" is not runnable") + "}\n"
	if out != want {
		t.Errorf("F12: stdout\n got %q\nwant %q", out, want)
	}
	start, n := f.stubLogLine(t, "START ")
	if n != 1 || !strings.HasPrefix(start, "START argv0="+f.cli+" ") {
		t.Errorf("stub START lines: %d, first %q, want one run with argv[0] %s", n, start, f.cli)
	}
	f.wantNames(t, "F12", append(oldVersionFiles(), ".fetch-new", "new.zst")...)
	if _, err := os.Lstat(blob); !os.IsNotExist(err) {
		t.Errorf("F12: the blob must be consumed, Lstat err = %v", err)
	}
}

// One install sweeps once, before the run of the new CLI. An entry that passes
// the age gate during the --version run stays, because no second sweep follows
// the run.
//
// The margins: the file is 5 s under the age gate when the install starts, so
// the first sweep keeps it unless the steps before that sweep take over 5 s. The
// stub runs for 7 s, so the file is over the gate by 2 s or more after the run,
// and a second sweep there removes it. A run that took 5 s or more before the
// stub started cannot tell the two apart, so the test skips there.
func TestInstallSweepsOnceBeforeTheRun(t *testing.T) {
	f := newStubFixture(t)
	blob := f.stubBlob(t)
	stubDelay(t, 7*time.Second)
	edge := filepath.Join(f.cliDir, ".fetch-edge")
	if err := os.WriteFile(edge, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-sweepMinAge + 5*time.Second)
	if err := os.Chtimes(edge, at, at); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob})
	total := time.Since(t0)
	if want := factsHead(t, f.cli) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if age := time.Since(at); age <= sweepMinAge {
		t.Fatalf("the fixture is %v old after the run, not over the %v age gate", age, sweepMinAge)
	}
	// The time before the first sweep is at most the total minus the stub delay.
	if before := total - 7*time.Second; before >= 5*time.Second {
		t.Skipf("the install took %v outside the stub run: the first sweep was possibly past the 5s margin", before)
	}
	f.wantNames(t, "one sweep", ".fetch-edge", filepath.Base(f.cli))
}

// Rows E01, B12. A cache miss with no source answers "missing" and still
// sweeps: the old swept pair goes, and the fresh pair and the old versions stay.
// The same holds when a file that does not run sits at the final name.
func TestInstallSweepsAfterAnAttemptWithNoSource(t *testing.T) {
	for _, row := range []string{"E01", "B12"} {
		t.Run(row, func(t *testing.T) {
			f := newStubFixture(t)
			f.litter(t)
			want := append(oldVersionFiles(), ".fetch-new", "new.zst")
			if row == "B12" {
				if err := os.WriteFile(f.cli, []byte("not a cli"), 0o755); err != nil {
					t.Fatal(err)
				}
				want = append(want, filepath.Base(f.cli))
			}
			out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliKeep: 2})
			wantOut := factsHead(t, f.cli) + `false,"cliError":"cli 9.9.9 missing and no --cli-url or --cli-zst provided"}` + "\n"
			if out != wantOut {
				t.Errorf("stdout\n got %q\nwant %q", out, wantOut)
			}
			f.wantNames(t, row, want...)
		})
	}
}

// An occupied final name (rows E11, E11b, F14). What is at
// the final name goes before the new CLI runs. A folder there is removed with
// the file in it, and a file there is replaced. At its --version run the new CLI
// is at the final path: its argv[0] is that path, and its folder holds the CLI
// file only.
func TestInstallClearsTheFinalNameBeforeTheNewCLIRuns(t *testing.T) {
	for _, shape := range []string{"a folder (E11)", "a file that does not run (F14)"} {
		t.Run(shape, func(t *testing.T) {
			f := newStubFixture(t)
			if strings.HasPrefix(shape, "a folder") {
				if err := os.Mkdir(f.cli, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.cli, "inner"), []byte("inner"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(f.cli, []byte("not a cli"), 0o755); err != nil {
				t.Fatal(err)
			}
			blob := f.stubBlob(t)
			out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob})
			if want := factsHead(t, f.cli) + "false}\n"; out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			start, n := f.stubLogLine(t, "START ")
			if n != 1 || !strings.HasPrefix(start, "START argv0="+f.cli+" ") {
				t.Errorf("stub START lines: %d, first %q, want one run with argv[0] %s", n, start, f.cli)
			}
			if dir, want := mustLogLine(t, f, "DIR "), "DIR "+filepath.Base(f.cli); dir != want {
				t.Errorf("the folder at the --version run: %q, want %q", dir, want)
			}
			if fi, err := os.Lstat(f.cli); err != nil || !fi.Mode().IsRegular() || fi.Size() < 1024 {
				t.Errorf("the final name after the run: err %v, want the CLI file", err)
			}
			if _, err := os.Lstat(blob); !os.IsNotExist(err) {
				t.Errorf("the blob must be consumed, Lstat err = %v", err)
			}
		})
	}
}

// An occupied final name. A new CLI that exits non-zero leaves no CLI
// at the final name, whatever was there before: the F12 shape. The reference
// side of this exact case is not measured.
func TestInstallLeavesNoCLIAfterANewCLIThatDoesNotRun(t *testing.T) {
	for _, shape := range []string{"a folder", "a file"} {
		t.Run(shape, func(t *testing.T) {
			f := newStubFixture(t)
			if shape == "a folder" {
				if err := os.Mkdir(f.cli, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.cli, "inner"), []byte("inner"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(f.cli, []byte("not a cli"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUSTRUM_TEST_STUB_EXIT", "1")
			out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
			want := factsHead(t, f.cli) + `false,"cliError":` + jsonString(t, "installed cli at "+f.cli+" is not runnable") + "}\n"
			if out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			if start := mustLogLine(t, f, "START "); !strings.HasPrefix(start, "START argv0="+f.cli+" ") {
				t.Errorf("stub START %q, want argv[0] %s", start, f.cli)
			}
			f.wantNames(t, shape)
		})
	}
}

// The single-path-component rule of D6 runs before the earlier delete. A
// -cli-version with a path separator, or "." or "..", is refused before any file
// is touched: nothing is deleted, nothing is staged and no CLI runs. Each victim
// is a folder with a file, the shape that the clear of the final name removes
// as a tree. The blob holds a CLI that exits non-zero, so the remove after a
// failed run is in reach too.
func TestInstallVersionGuardRunsBeforeTheEarlierDelete(t *testing.T) {
	for _, version := range []string{"../victim", "sub/victim", `sub\victim`, "..", "."} {
		t.Run(version, func(t *testing.T) {
			f := newStubFixture(t)
			t.Setenv("CLAUSTRUM_TEST_STUB_EXIT", "1")
			var keeps []string
			for _, dir := range []string{
				filepath.Join(f.root, "victim"), filepath.Join(f.cliDir, "sub", "victim"), filepath.Join(f.cliDir, `sub\victim`),
			} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					// A backslash in a name is a separator on Windows, where this
					// folder is the "sub/victim" one.
					continue
				}
				keep := filepath.Join(dir, "keep.txt")
				if err := os.WriteFile(keep, []byte("important"), 0o600); err != nil {
					t.Fatal(err)
				}
				keeps = append(keeps, keep)
			}
			before := f.names(t)
			blob := f.stubBlob(t)
			facts := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: version, cliZst: blob})
			if want := "cli version " + strconv.Quote(version) + " must be a single path component"; facts.CliError != want {
				t.Errorf("CliError = %q, want %q", facts.CliError, want)
			}
			for _, keep := range keeps {
				if !isRegularFile(keep) {
					t.Errorf("%s was deleted", keep)
				}
			}
			if after := f.names(t); !slices.Equal(after, before) {
				t.Errorf("cli folder = %v, want it unchanged %v", after, before)
			}
			if !isRegularFile(blob) {
				t.Error("the blob was consumed")
			}
			if _, n := f.stubLogLine(t, "START "); n != 0 {
				t.Errorf("a CLI ran %d times, want 0", n)
			}
		})
	}
}

// A staging file that is gone before the rename, with nothing at the final name,
// is the "staging file vanished" error after one retry. The final name stays
// empty, and the -cli-zst blob is consumed, because decompression succeeded. The
// chmod seam removes the staging file, as a concurrent sweep does.
func TestEnsureCLIReportsAVanishedStagingFileAtAnEmptyFinalName(t *testing.T) {
	old := chmodStaged
	var calls int
	chmodStaged = func(p string, _ os.FileMode) error { calls++; return os.Remove(p) }
	t.Cleanup(func() { chmodStaged = old })

	f := newStubFixture(t)
	blob := f.stubBlob(t)
	err := ensureCLI(installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob}, f.cli)
	if err == nil || !strings.HasPrefix(err.Error(), "staging file vanished before install: ") {
		t.Fatalf("ensureCLI = %v, want the staging-vanished error", err)
	}
	if calls != 2 {
		t.Errorf("the staging step ran %d times, want 2 (one retry)", calls)
	}
	f.wantNames(t, "vanished")
	if _, statErr := os.Lstat(blob); !os.IsNotExist(statErr) {
		t.Errorf("the blob must be consumed, Lstat err = %v", statErr)
	}
	if _, n := f.stubLogLine(t, "START "); n != 0 {
		t.Errorf("the stub ran %d times, want 0", n)
	}
}

// Row F08. A stopped -cli-url first install prints cliUnresponsive before
// fetch. The download blob is gone before the run, and the new CLI stays.
func TestInstallStoppedDownloadPutsFetchAfterCliUnresponsive(t *testing.T) {
	f := newStubFixture(t)
	stubDelay(t, 20*time.Second)
	// 5 s, not less: the stub must write its log before the stop.
	setCLIRunBounds(t, time.Minute, 5*time.Second)
	zst := stubZst(t)
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9",
		cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
	line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
	head := factsHead(t, f.cli) + stoppedTail + `,"fetch":{"bytes":` + strconv.Itoa(len(zst)) + `,"ms":`
	if !strings.HasPrefix(line, head) || !regexp.MustCompile(`"ms":\d+,"longestPauseMs":\d+\}\}\n$`).MatchString(line) {
		t.Errorf("F08: facts line\n got %q\nwant the start %q and fetch as the last key", line, head)
	}
	if dir, want := mustLogLine(t, f, "DIR "), "DIR "+filepath.Base(f.cli); dir != want {
		t.Errorf("the folder at the --version run: %q, want %q (no download blob, no temp)", dir, want)
	}
	f.wantNames(t, "F08", filepath.Base(f.cli))
}

// mustLogLine is the first stub log line with the prefix. The stub writes its
// log at its start, so a stopped stub has written it too.
func mustLogLine(t *testing.T, f *stubFixture, prefix string) string {
	t.Helper()
	l, n := f.stubLogLine(t, prefix)
	if n == 0 {
		t.Fatalf("the stub log holds no %q line", prefix)
	}
	return l
}

// Rows H01, H03, H05. With -cli-version and no -cli-dir, or an empty one,
// the cli folder is <home>/.claude/remote/ccd-cli. A run with no source answers
// "missing" with that cliPath and makes the folder chain. A blob installs there.
func TestInstallDefaultCLIDir(t *testing.T) {
	f := newStubFixture(t)
	home := filepath.Join(f.root, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "remote", "ccd-cli")
	cli := installCLIPath(dir, "9.9.9")

	out := captureInstallOutput(t, installOpts{home: home, cliVersion: "9.9.9"})
	want := factsHead(t, cli) + `false,"cliError":"cli 9.9.9 missing and no --cli-url or --cli-zst provided"}` + "\n"
	if out != want {
		t.Errorf("H01: stdout\n got %q\nwant %q", out, want)
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Errorf("H01: the default folder after a \"missing\" answer: entries %v, err %v, want it made and empty", ents, err)
	}

	blob := f.stubBlob(t)
	out = captureInstallOutput(t, installOpts{home: home, cliDir: "", cliVersion: "9.9.9", cliZst: blob})
	if want := factsHead(t, cli) + "false}\n"; out != want {
		t.Errorf("H03: stdout\n got %q\nwant %q", out, want)
	}
	if !isRegularFile(cli) {
		t.Error("H03: the CLI was not installed in the default folder")
	}
	if _, err := os.Lstat(blob); !os.IsNotExist(err) {
		t.Errorf("H03: the blob must be consumed, Lstat err = %v", err)
	}
	if start := mustLogLine(t, f, "START "); !strings.HasPrefix(start, "START argv0="+cli+" ") {
		t.Errorf("H03: stub START %q, want argv[0] %s", start, cli)
	}

	// An explicit -cli-dir wins over the default.
	out = captureInstallOutput(t, installOpts{home: home, cliDir: f.cliDir, cliVersion: "9.9.9"})
	if !strings.HasPrefix(out, factsHead(t, f.cli)) {
		t.Errorf("explicit -cli-dir: stdout %q, want the cliPath %s", out, f.cli)
	}
}

// The single-path-component rule of D6 guards the default cli folder too. A
// version that leaves the folder is refused before any file is touched. Without
// the guard the install replaces the folder beside the default folder.
func TestInstallDefaultCLIDirKeepsTheVersionGuard(t *testing.T) {
	f := newStubFixture(t)
	home := filepath.Join(f.root, "home")
	victim := filepath.Join(home, ".claude", "remote", "victim")
	keep := filepath.Join(victim, "keep.txt")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	facts := captureInstallFacts(t, installOpts{home: home, cliVersion: "../victim", cliZst: f.stubBlob(t)})
	if !strings.Contains(facts.CliError, "single path component") {
		t.Errorf("CliError = %q, want the D6 refusal", facts.CliError)
	}
	if !isRegularFile(keep) {
		t.Error("the folder beside the default cli folder was destroyed")
	}
}

// Row H06. With no -cli-version the cliPath is empty, nothing runs and no
// folder is made, with or without -cli-dir. The default folder does not apply.
func TestInstallWithoutVersionMakesNoFolder(t *testing.T) {
	f := newStubFixture(t)
	home := filepath.Join(f.root, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(f.root, "absent-cli")
	for _, o := range []installOpts{{home: home}, {home: home, cliDir: absent}} {
		out := captureInstallOutput(t, o)
		if want := factsHead(t, "") + "false}\n"; out != want {
			t.Errorf("H06, cliDir %q: stdout\n got %q\nwant %q", o.cliDir, out, want)
		}
	}
	if ents, err := os.ReadDir(home); err != nil || len(ents) != 0 {
		t.Errorf("the home folder holds %v (err %v), want nothing made", ents, err)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Errorf("the -cli-dir folder was made, Lstat err = %v", err)
	}
	if _, n := f.stubLogLine(t, "START "); n != 0 {
		t.Errorf("the stub ran %d times, want 0", n)
	}
}

// Row E10. A cli folder that cannot be made answers an empty cliPath with
// the "mkdir cli dir: " text. The blob stays.
func TestInstallAnswersAnEmptyCliPathWhenTheFolderCannotBeMade(t *testing.T) {
	f := newStubFixture(t)
	p := filepath.Join(f.root, "p")
	if err := os.WriteFile(p, []byte("a regular file"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob := f.stubBlob(t)
	out := captureInstallOutput(t, installOpts{cliDir: filepath.Join(p, "cli"), cliVersion: "9.9.9", cliZst: blob})
	head := factsHead(t, "") + `false,"cliError":"mkdir cli dir: `
	if !strings.HasPrefix(out, head) || strings.Count(out, "\n") != 1 {
		t.Errorf("E10: stdout %q, want one line that starts %q", out, head)
	}
	if want := factsHead(t, "") + `false,"cliError":"mkdir cli dir: mkdir ` + p + `: not a directory"}` + "\n"; runtime.GOOS == "linux" && out != want {
		t.Errorf("E10: stdout\n got %q\nwant %q", out, want)
	}
	if !isRegularFile(blob) {
		t.Error("E10: the blob must stay")
	}
}

// The Windows name rule (rows W02 to W07, E11a, E11b), run with the suffix seam so
// that it runs on every system. The CLI file is <version>.exe. Only that name
// counts as present. A version that ends in ".exe" gets the suffix too. The prune
// counts every file, with or without the suffix. A folder at the bare name stays,
// and a folder at the .exe name is replaced.
func TestInstallExeNameRule(t *testing.T) {
	old := cliExeSuffix
	cliExeSuffix = ".exe"
	t.Cleanup(func() { cliExeSuffix = old })

	mk := func(t *testing.T) *stubFixture {
		f := newStubFixture(t)
		if got, want := f.cli, filepath.Join(f.cliDir, "9.9.9.exe"); got != want {
			t.Fatalf("installCLIPath = %q, want %q", got, want)
		}
		return f
	}
	bare := func(f *stubFixture) string { return filepath.Join(f.cliDir, "9.9.9") }
	notACLI := func(t *testing.T, p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("not a cli"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missing := `false,"cliError":"cli 9.9.9 missing and no --cli-url or --cli-zst provided"}` + "\n"
	run := func(t *testing.T, f *stubFixture, zst string) string {
		t.Helper()
		return captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: zst})
	}

	t.Run("W02: only the .exe file is present", func(t *testing.T) {
		f := mk(t)
		placeStub(t, f.cli)
		if out, want := run(t, f, ""), factsHead(t, f.cli)+"true}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if start := mustLogLine(t, f, "START "); !strings.HasPrefix(start, "START argv0="+f.cli+" ") {
			t.Errorf("stub START %q, want argv[0] %s", start, f.cli)
		}
	})
	t.Run("W03: only the bare file counts for nothing", func(t *testing.T) {
		f := mk(t)
		placeStub(t, bare(f))
		if out, want := run(t, f, ""), factsHead(t, f.cli)+missing; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if _, n := f.stubLogLine(t, "START "); n != 0 {
			t.Errorf("the bare file ran %d times, want 0", n)
		}
	})
	t.Run("W04: a failing bare file beside a good .exe file", func(t *testing.T) {
		f := mk(t)
		placeStub(t, f.cli)
		notACLI(t, bare(f))
		if out, want := run(t, f, ""), factsHead(t, f.cli)+"true}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
	})
	t.Run("W05: a failing .exe file beside a good bare file", func(t *testing.T) {
		f := mk(t)
		notACLI(t, f.cli)
		placeStub(t, bare(f))
		if out, want := run(t, f, ""), factsHead(t, f.cli)+missing; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if _, n := f.stubLogLine(t, "START "); n != 0 {
			t.Errorf("the bare file ran %d times, want 0", n)
		}
	})
	t.Run("W06: a version that ends in .exe", func(t *testing.T) {
		f := mk(t)
		cli := filepath.Join(f.cliDir, "9.9.9.exe.exe")
		out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9.exe", cliZst: f.stubBlob(t)})
		if want := factsHead(t, cli) + "false}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		f.wantNames(t, "W06", "9.9.9.exe.exe")
	})
	t.Run("W07: the prune counts files with and without the suffix", func(t *testing.T) {
		f := mk(t)
		now := time.Now()
		for i, name := range []string{"1.0.0.exe", "1.1.0.exe", "1.2.0.exe", "1.0.0", "1.1.0"} {
			p := filepath.Join(f.cliDir, name)
			notACLI(t, p)
			at := now.Add(-time.Duration(48-i) * time.Hour)
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
		out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t), cliKeep: 1})
		if want := factsHead(t, f.cli) + "false}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if dir, want := mustLogLine(t, f, "DIR "), "DIR 1.0.0,1.0.0.exe,1.1.0,1.1.0.exe,1.2.0.exe,9.9.9.exe"; dir != want {
			t.Errorf("the folder at the --version run: %q, want %q", dir, want)
		}
		f.wantNames(t, "W07", "9.9.9.exe")
	})
	t.Run("E11a: a folder at the bare name stays", func(t *testing.T) {
		f := mk(t)
		if err := os.Mkdir(bare(f), 0o755); err != nil {
			t.Fatal(err)
		}
		inner := filepath.Join(bare(f), "inner.txt")
		notACLI(t, inner)
		if out, want := run(t, f, f.stubBlob(t)), factsHead(t, f.cli)+"false}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if !isRegularFile(inner) || !isRegularFile(f.cli) {
			t.Error("the folder at the bare name must stay, and the CLI must be at the .exe name")
		}
	})
	t.Run("E11b: a folder at the .exe name is replaced", func(t *testing.T) {
		f := mk(t)
		if err := os.Mkdir(f.cli, 0o755); err != nil {
			t.Fatal(err)
		}
		notACLI(t, filepath.Join(f.cli, "inner.txt"))
		if out, want := run(t, f, f.stubBlob(t)), factsHead(t, f.cli)+"false}\n"; out != want {
			t.Errorf("stdout\n got %q\nwant %q", out, want)
		}
		if !isRegularFile(f.cli) {
			t.Error("the CLI file must replace the folder at the .exe name")
		}
		f.wantNames(t, "E11b", "9.9.9.exe")
	})
}
