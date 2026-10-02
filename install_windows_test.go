//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Windows side of the 89cb6289 -install measurement (Windows VM): the .exe
// file name, what the stop of a direct run reaches, and -probe-cli on a path
// with no file.
// Each stand-in CLI is a copy of this test binary with a real .exe name.

// Row W01. The CLI file is <cli-dir>\<version>.exe: in cliPath, on disk
// and as the argv[0] of the --version run.
func TestInstallWindowsCLIFileIsVersionExe(t *testing.T) {
	f := newStubFixture(t)
	want := filepath.Join(f.cliDir, "9.9.9.exe")
	if f.cli != want {
		t.Fatalf("installCLIPath = %q, want %q", f.cli, want)
	}
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	if wantOut := factsHead(t, want) + "false}\n"; out != wantOut {
		t.Errorf("W01: stdout\n got %q\nwant %q", out, wantOut)
	}
	f.wantNames(t, "W01", "9.9.9.exe")
	if start := mustLogLine(t, f, "START "); !strings.HasPrefix(start, "START argv0="+want+" ") {
		t.Errorf("W01: stub START %q, want argv[0] %s", start, want)
	}
}

// Row F12 on Windows. The "not runnable" text names the .exe path, and
// the "missing" text names the version with no suffix.
func TestInstallWindowsErrorTextsNameTheExePath(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv("CLAUSTRUM_TEST_STUB_EXIT", "1")
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: f.stubBlob(t)})
	want := factsHead(t, f.cli) + `false,"cliError":` + jsonString(t, "installed cli at "+f.cli+" is not runnable") + "}\n"
	if out != want {
		t.Errorf("F12: stdout\n got %q\nwant %q", out, want)
	}
	f.wantNames(t, "F12")

	out = captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	want = factsHead(t, f.cli) + `false,"cliError":"cli 9.9.9 missing and no --cli-url or --cli-zst provided"}` + "\n"
	if out != want {
		t.Errorf("E01: stdout\n got %q\nwant %q", out, want)
	}
}

// Rows B08, B09. At the bound the direct CLI process is ended. A child
// that it started stays alive, in the process group of the CLI or in a new one.
func TestInstallWindowsStopEndsTheCLIAndLeavesItsChild(t *testing.T) {
	for _, kind := range []string{"group", "session"} {
		t.Run(kind, func(t *testing.T) {
			f := newStubFixture(t)
			placeStub(t, f.cli)
			stubDelay(t, 20*time.Second)
			t.Setenv("CLAUSTRUM_TEST_STUB_CHILD", kind)
			// 3 s, not less: the stub must write its log and its child's pid first.
			setCLIRunBounds(t, 3*time.Second, time.Minute)
			t.Cleanup(func() { killStubChild(f.childPID) })

			start := time.Now()
			out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("the run took %v, want about the 3s bound", d)
			}
			if want := factsHead(t, f.cli) + stoppedTail + "}\n"; out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			line := mustLogLine(t, f, "START ")
			m := strings.Index(line, " pid=")
			if m < 0 {
				t.Fatalf("no pid in the stub line %q", line)
			}
			stubPID, err := strconv.Atoi(strings.Fields(line[m+len(" pid="):])[0])
			if err != nil {
				t.Fatal(err)
			}
			if !waitProcessGone(stubPID, 5*time.Second) {
				t.Errorf("the stub %d is still alive after the stop", stubPID)
			}
			child := readPIDFile(t, f.childPID)
			time.Sleep(time.Second)
			if !processAlive(t, child) {
				t.Errorf("the child %d of the stopped CLI is gone, want it alive", child)
			}
		})
	}
}

// Rows L02, L10 on Windows. With the gate the resolve answers none, so the
// CLI runs directly under the direct bound, and launcherStatus none is the last key.
func TestInstallWindowsGateUsesTheDirectBounds(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(managedLauncherGateEnv, "1")
	placeStub(t, f.cli)
	stubDelay(t, 20*time.Second)
	setCLIRunBounds(t, 3*time.Second, time.Minute)
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9"})
	if want := factsHead(t, f.cli) + stoppedTail + `,"launcherStatus":"none"}` + "\n"; out != want {
		t.Errorf("L02: stdout\n got %q\nwant %q", out, want)
	}
}

// Rows W08a to W08d. -probe-cli answers __CLI_BAD__ and starts nothing
// when no file exists at exactly the given path. It never adds ".exe".
func TestProbeCLIWindowsNeedsTheExactPath(t *testing.T) {
	probe := func(t *testing.T, path string) string {
		t.Helper()
		var out, errb bytes.Buffer
		runProbeCLI(&out, &errb, path)
		if errb.Len() != 0 {
			t.Errorf("stderr %q, want it empty", errb.String())
		}
		return out.String()
	}
	t.Run("only the .exe file", func(t *testing.T) {
		f := newStubFixture(t)
		placeStub(t, f.cli)
		bare := filepath.Join(f.cliDir, "9.9.9")
		if got := probe(t, bare); got != "__CLI_BAD__\n" {
			t.Errorf("W08a: stdout %q, want __CLI_BAD__", got)
		}
		if _, n := f.stubLogLine(t, "START "); n != 0 {
			t.Errorf("W08a: the stub ran %d times, want 0", n)
		}
		if got := probe(t, f.cli); got != "" {
			t.Errorf("W08b: stdout %q, want it empty", got)
		}
		if _, n := f.stubLogLine(t, "START "); n != 1 {
			t.Errorf("W08b: the stub ran %d times, want 1", n)
		}
	})
	t.Run("only the bare file", func(t *testing.T) {
		f := newStubFixture(t)
		bare := filepath.Join(f.cliDir, "9.9.9")
		placeStub(t, bare)
		if got := probe(t, bare); got != "" {
			t.Errorf("W08c: stdout %q, want it empty", got)
		}
		if _, n := f.stubLogLine(t, "START "); n != 1 {
			t.Errorf("W08c: the stub ran %d times, want 1", n)
		}
		if got := probe(t, f.cli); got != "__CLI_BAD__\n" {
			t.Errorf("W08d: stdout %q, want __CLI_BAD__", got)
		}
		if err := os.Remove(bare); err != nil {
			t.Fatal(err)
		}
	})
}
