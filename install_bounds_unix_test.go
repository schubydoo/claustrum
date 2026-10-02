//go:build !windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Linux and macOS side of the 89cb6289 -install measurement. It covers what
// the stop of a direct run reaches, the launcher gate, and the modes of the
// default cli folder.

// stubField is the value of one `key=value` field of a stub START line.
func stubField(t *testing.T, line, key string) int {
	t.Helper()
	m := regexp.MustCompile(` ` + key + `=(\d+)`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no %s in the stub line %q", key, line)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitStubChild reads the pid that the stub recorded for its child.
func waitStubChild(t *testing.T, f *stubFixture) int {
	t.Helper()
	pid := readGrandchildPID(t, f.childPID)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// goneWithin reports whether pid stops existing within d.
func goneWithin(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// Rows B04, B07, B08, F09, F10. The direct CLI runs in a process group of
// its own, in the session of the -install process and as its child. The stop at
// the bound reaches a CLI that ignores SIGTERM, and it ends a child in the group
// of the CLI.
func TestInstallDirectRunStopsTheWholeGroup(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	stubDelay(t, 20*time.Second)
	t.Setenv("CLAUSTRUM_TEST_STUB_CHILD", "group")
	t.Setenv("CLAUSTRUM_TEST_STUB_IGNORETERM", "1")
	// 5 s, not less: the stub must write its log and its child's pid before the
	// stop, and a first start is slow on a loaded macOS runner.
	setCLIRunBounds(t, 5*time.Second, time.Minute)

	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("B07: the run took %v. A CLI that ignores SIGTERM must be stopped at the bound", d)
	}
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	line := mustLogLine(t, f, "START ")
	pid, pgid := stubField(t, line, "pid"), stubField(t, line, "pgid")
	if pgid != pid {
		t.Errorf("B04: the stub ran with pgid %d and pid %d, want a process group of its own", pgid, pid)
	}
	if got, want := stubField(t, line, "ppid"), os.Getpid(); got != want {
		t.Errorf("B04: the parent of the stub is %d, want the -install process %d", got, want)
	}
	if got, mySid := stubField(t, line, "sid"), ownSessionID(); got != mySid {
		t.Errorf("B04: the stub ran in session %d, want the session of the -install process %d", got, mySid)
	}
	child := waitStubChild(t, f)
	if !goneWithin(child, 2*time.Second) {
		t.Errorf("B08: the child %d in the group of the CLI is still alive after the stop", child)
	}
	if !goneWithin(pid, 2*time.Second) {
		t.Errorf("the stub %d is still alive after the stop", pid)
	}
}

// Rows B09, F11. A child of the CLI in a new session survives the stop.
func TestInstallDirectRunStopLeavesANewSessionChild(t *testing.T) {
	f := newStubFixture(t)
	placeStub(t, f.cli)
	stubDelay(t, 20*time.Second)
	t.Setenv("CLAUSTRUM_TEST_STUB_CHILD", "session")
	setCLIRunBounds(t, 5*time.Second, time.Minute)

	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	child := waitStubChild(t, f)
	time.Sleep(500 * time.Millisecond)
	if err := syscall.Kill(child, 0); err != nil {
		t.Errorf("B09: the child %d in a new session is gone after the stop (%v), want it alive", child, err)
	}
}

// gateFixture is a stubFixture with the launcher gate on and an isolated managed
// settings folder. base is the settings file, and wrap is a launcher that runs
// the CLI in the "cli-stub" mode.
type gateFixture struct {
	*stubFixture
	base, wrap, wrapLog string
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	g := &gateFixture{stubFixture: newStubFixture(t)}
	g.base = filepath.Join(tempManagedSettingsDir(t), managedSettingsBase)
	g.wrap = filepath.Join(g.root, "bin", "wrap")
	g.wrapLog = filepath.Join(g.root, "wrap.log")
	if err := os.Mkdir(filepath.Dir(g.wrap), 0o755); err != nil {
		t.Fatal(err)
	}
	placeStub(t, g.wrap)
	t.Setenv("CLAUSTRUM_TEST_WRAP_LOG", g.wrapLog)
	t.Setenv(managedLauncherGateEnv, "1")
	return g
}

// useLauncher writes the policy and makes every run go through the launcher.
func (g *gateFixture) useLauncher(t *testing.T) {
	t.Helper()
	writeFileMode(t, g.base, []byte(pwSettings(t, g.wrap)), 0o644)
	helperMode(t, "wrap", "cli-stub")
}

// Rows L02, L07. With the gate set and no policy the CLI runs directly,
// under the direct bounds and with the direct text. launcherStatus none is the
// last key, and the CLI keeps the gate variable.
func TestInstallGateWithoutPolicyUsesTheDirectBounds(t *testing.T) {
	g := newGateFixture(t)
	g.litter(t)
	placeStub(t, g.cli)
	stubDelay(t, 20*time.Second)
	setCLIRunBounds(t, 5*time.Second, time.Minute)

	out := captureInstallOutput(t, installOpts{cliDir: g.cliDir, cliVersion: "9.9.9", cliKeep: 2})
	if want := factsHead(t, g.cli) + stoppedTail + `,"launcherStatus":"none"}` + "\n"; out != want {
		t.Errorf("L02: stdout\n got %q\nwant %q", out, want)
	}
	if line := mustLogLine(t, g.stubFixture, "START "); !strings.HasSuffix(line, " gate=1") {
		t.Errorf("L02: stub START %q, want the gate variable with the value 1", line)
	}
	g.wantNames(t, "L02", append(oldVersionFiles(), ".fetch-new", "new.zst", "9.9.9")...)

	// L07: a new CLI that does not answer, stopped at the first-run bound.
	g2 := newGateFixture(t)
	g2.litter(t)
	stubDelay(t, 20*time.Second)
	setCLIRunBounds(t, time.Minute, 500*time.Millisecond)
	out = captureInstallOutput(t, installOpts{cliDir: g2.cliDir, cliVersion: "9.9.9", cliZst: g2.stubBlob(t), cliKeep: 2})
	if want := factsHead(t, g2.cli) + stoppedTail + `,"launcherStatus":"none"}` + "\n"; out != want {
		t.Errorf("L07: stdout\n got %q\nwant %q", out, want)
	}
	g2.wantNames(t, "L07", append(oldVersionFiles(), ".fetch-new", "new.zst", "9.9.9")...)
}

// With the gate set and a cli folder that cannot be made, the facts line holds
// an empty cliPath, the mkdir text and no launcher field (cell G1, Linux VM,
// f6010b97 and 89cb6289).
func TestInstallGatePrintsNoLauncherFieldBesideAnEmptyCliPath(t *testing.T) {
	g := newGateFixture(t)
	p := filepath.Join(g.root, "p")
	if err := os.WriteFile(p, []byte("a regular file"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: filepath.Join(p, "cli"), cliVersion: "9.9.9", cliZst: g.stubBlob(t)})
	want := factsHead(t, "") + `false,"cliError":"mkdir cli dir: mkdir ` + p + `: not a directory"}` + "\n"
	if out != want {
		t.Errorf("G1: stdout\n got %q\nwant %q", out, want)
	}
}

// Rows L01, L06. With a usable launcher the direct bounds do not apply. A
// CLI that is slower than the direct bound and faster than the launcher bound
// passes, on a cache hit and right after an install. The launcher gets the final
// path both times.
func TestInstallLauncherRunIgnoresTheDirectBounds(t *testing.T) {
	g := newGateFixture(t)
	g.useLauncher(t)
	placeStub(t, g.cli)
	stubDelay(t, 1500*time.Millisecond)
	setCLIRunBounds(t, 300*time.Millisecond, 300*time.Millisecond)
	usable := `,"launcherStatus":"usable","launcher":["` + g.wrap + `"],"launcherSource":"` + g.base + `"}` + "\n"

	out := captureInstallOutput(t, installOpts{cliDir: g.cliDir, cliVersion: "9.9.9"})
	if want := factsHead(t, g.cli) + "true" + usable; out != want {
		t.Errorf("L01: stdout\n got %q\nwant %q", out, want)
	}

	if err := os.Remove(g.cli); err != nil {
		t.Fatal(err)
	}
	out = captureInstallOutput(t, installOpts{cliDir: g.cliDir, cliVersion: "9.9.9", cliZst: g.stubBlob(t)})
	if want := factsHead(t, g.cli) + "false" + usable; out != want {
		t.Errorf("L06: stdout\n got %q\nwant %q", out, want)
	}
	if got, want := readLog(t, g.wrapLog), strings.Repeat("LAUNCH "+g.cli+" --version\n", 2); got != want {
		t.Errorf("launcher log %q, want two runs on the final path %q", got, want)
	}
}

// Rows L03, L05. A launcher run that is stopped on a cache hit removes the
// old swept pair. The CLI, the fresh pair and the old versions stay, and nothing
// is pruned.
func TestInstallStoppedLauncherRunOnACacheHitSweeps(t *testing.T) {
	g := newGateFixture(t)
	g.useLauncher(t)
	g.litter(t)
	placeStub(t, g.cli)
	stubDelay(t, 20*time.Second)
	setManagedRunBound(t, 500*time.Millisecond)

	out := captureInstallOutput(t, installOpts{cliDir: g.cliDir, cliVersion: "9.9.9", cliKeep: 2})
	if !strings.Contains(out, `"cliUnresponsive":true,"launcherStatus":"unresponsive",`) {
		t.Fatalf("L03: stdout %q, want a stopped launcher run", out)
	}
	g.wantNames(t, "L03", append(oldVersionFiles(), ".fetch-new", "new.zst", "9.9.9")...)
}

// Row L09 for the order of the keys, row L10 for the none status. With the
// gate, fetch comes after cliUnresponsive and before launcherStatus.
func TestInstallGateStoppedDownloadKeyOrder(t *testing.T) {
	g := newGateFixture(t)
	stubDelay(t, 20*time.Second)
	setCLIRunBounds(t, time.Minute, 500*time.Millisecond)
	zst := stubZst(t)
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	defer srv.Close()

	out := captureInstallOutput(t, installOpts{cliDir: g.cliDir, cliVersion: "9.9.9",
		cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
	line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
	re := regexp.MustCompile(`"cliUnresponsive":true,"fetch":\{"bytes":\d+,"ms":\d+,"longestPauseMs":\d+\},"launcherStatus":"none"\}\n$`)
	if !strings.HasPrefix(line, factsHead(t, g.cli)+stoppedTail+`,"fetch":`) || !re.MatchString(line) {
		t.Errorf("facts line %q, want cliUnresponsive, then fetch, then launcherStatus none", line)
	}
}

// Rows H01, H08, H09. The three new levels of the default cli folder get
// mode 0700, with umask 000 too. A level that exists keeps its mode. The installed
// CLI has mode 0755 (row H03).
func TestInstallDefaultCLIDirModes(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	f := newStubFixture(t)
	home := filepath.Join(f.root, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	levels := []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude", "remote"),
		filepath.Join(home, ".claude", "remote", "ccd-cli")}
	modes := func(t *testing.T, row string, want os.FileMode) {
		t.Helper()
		for _, d := range levels {
			fi, err := os.Stat(d)
			if err != nil {
				t.Fatalf("%s: %v", row, err)
			}
			if got := fi.Mode().Perm(); got != want {
				t.Errorf("%s: %s has mode %04o, want %04o", row, d, got, want)
			}
		}
	}

	_ = captureInstallOutput(t, installOpts{home: home, cliVersion: "9.9.9"})
	modes(t, "H01/H09 (new levels, umask 000)", 0o700)

	out := captureInstallOutput(t, installOpts{home: home, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	if strings.Contains(out, "cliError") {
		t.Fatalf("H03: stdout %q, want a good install", out)
	}
	if fi, err := os.Stat(filepath.Join(levels[2], "9.9.9")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("H03: the installed CLI: err %v, mode %v, want 0755", err, fi)
	}

	// H08: levels that exist with 0755 keep 0755.
	home2 := filepath.Join(f.root, "home2")
	levels = []string{filepath.Join(home2, ".claude"), filepath.Join(home2, ".claude", "remote"),
		filepath.Join(home2, ".claude", "remote", "ccd-cli")}
	if err := os.MkdirAll(levels[2], 0o755); err != nil {
		t.Fatal(err)
	}
	_ = captureInstallOutput(t, installOpts{home: home2, cliVersion: "9.9.9"})
	modes(t, "H08 (existing levels)", 0o755)
}
