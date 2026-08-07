package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lastMainFlagSet is the FlagSet the most recent runMain handed to main(), kept
// after runMain restores flag.CommandLine so a test can inspect flag defaults.
var lastMainFlagSet *flag.FlagSet

// runMain drives the real main() with a synthetic argv. main registers its
// flags on the global flag.CommandLine (already populated by the previous
// call, and by the testing package), so each run gets a fresh FlagSet; Version
// and BuildTime are restored because resolveVersion may stamp them from the
// test binary's own build info; stdout is parked on /dev/null so -version and
// -install output doesn't pollute the test log.
//
// The -install and -serve arms of main() also WRITE package globals
// (cliProbeTimeout, cliDownloadTimeout, maxCLIBytes, maxExtractBytes) and never
// restore them, so
// without the save/restore below a `-install` run here leaks its resolved values
// into every later test. Reproduced 2026-08-07: with a claustrum.conf holding
// `cli-probe-timeout = 30s` beside a pre-built test binary, 4 of 6 -test.shuffle
// seeds failed TestCLIProbeTimeoutDefaultsOff with "default = 30s"; the two that
// passed were the seeds that ran the assertion first, and the same seeds with no
// conf present passed. Not reachable under plain `go test` (loadConfig reads
// beside os.Executable(), the build-cache temp dir), but it goes live with
// -shuffle, with a pre-built binary run from a directory holding a conf, or as
// soon as a case here passes one of those flags.
func runMain(t *testing.T, args ...string) (code int, exited bool) {
	t.Helper()
	stubOsExit(t)
	oldArgs, oldFS := os.Args, flag.CommandLine
	oldVersion, oldBuildTime := Version, BuildTime
	// t.Cleanup, NOT the defer below: the defer runs when runMain returns, which
	// would put the globals back before the caller can assert on what main()
	// actually resolved. t.Cleanup still contains the leak to this one test.
	oldProbe, oldDownload := cliProbeTimeout, cliDownloadTimeout
	oldMaxCLI, oldMaxExtract := maxCLIBytes, maxExtractBytes
	t.Cleanup(func() {
		cliProbeTimeout, cliDownloadTimeout = oldProbe, oldDownload
		maxCLIBytes, maxExtractBytes = oldMaxCLI, oldMaxExtract
	})
	oldStdout := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"claustrum"}, args...)
	flag.CommandLine = flag.NewFlagSet("claustrum", flag.ExitOnError)
	// Kept for assertions on the DECLARED defaults main() registers, which is a
	// different question from the value it resolves: a claustrum.conf beside the
	// binary legitimately changes the latter and must not fail such a test.
	lastMainFlagSet = flag.CommandLine
	os.Stdout = devnull
	defer func() {
		os.Args, flag.CommandLine = oldArgs, oldFS
		Version, BuildTime = oldVersion, oldBuildTime
		os.Stdout = oldStdout
		_ = devnull.Close()
	}()
	return catchExit(main)
}

// The -install arm's flag-to-global wiring, which nothing else covers. Setting
// the deadline directly (as the isRunnable/fetchToFile tests do) exercises the
// READ; asserting flag.DefValue covers the DECLARED default. The line joining
// them — main() resolving each flag into its own global — was unasserted, and
// swapping the two assignment targets passed the whole suite, `-race` included,
// while shipping a binary where -cli-download-timeout set the probe deadline and
// vice versa.
//
// Distinct values on purpose: equal ones would pass under a swap.
func TestInstallArmWiresEachFlagToItsOwnGlobal(t *testing.T) {
	if _, exited := runMain(t, "-install", "-cli-probe-timeout", "7s", "-cli-download-timeout", "42s"); exited {
		t.Fatal("-install should return, not exit")
	}
	if cliProbeTimeout != 7*time.Second {
		t.Errorf("cliProbeTimeout = %s, want 7s (-cli-probe-timeout must reach it, not the download global)", cliProbeTimeout)
	}
	if cliDownloadTimeout != 42*time.Second {
		t.Errorf("cliDownloadTimeout = %s, want 42s (-cli-download-timeout must reach it, not the probe global)", cliDownloadTimeout)
	}
}

// runMain restores the globals main() writes, and that restore is load-bearing:
// without it a -install run here leaks its resolved values into every later test.
// Asserting it needs the cleanup to have RUN, so the call goes in a subtest and
// the assertion follows it — t.Cleanup fires at the subtest's end.
func TestRunMainRestoresInstallGlobals(t *testing.T) {
	cliProbeTimeout, cliDownloadTimeout = 3*time.Second, 4*time.Second
	maxCLIBytes, maxExtractBytes = 11, 22
	t.Cleanup(func() {
		cliProbeTimeout, cliDownloadTimeout = 0, 0
		maxCLIBytes, maxExtractBytes = 0, 0
	})
	t.Run("inner", func(t *testing.T) {
		if _, exited := runMain(t, "-install", "-cli-probe-timeout", "9s", "-cli-download-timeout", "8s", "-max-cli-bytes", "99"); exited {
			t.Fatal("-install should return, not exit")
		}
	})
	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"cliProbeTimeout", cliProbeTimeout, 3 * time.Second},
		{"cliDownloadTimeout", cliDownloadTimeout, 4 * time.Second},
		{"maxCLIBytes", maxCLIBytes, int64(11)},
		{"maxExtractBytes", maxExtractBytes, int64(22)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v after runMain, want %v restored", c.name, c.got, c.want)
		}
	}
}

// The non-daemonizing dispatch arms of main: each mode flag must route to its
// runner and return (or exit) the way the CLI contract promises.
func TestMainDispatch(t *testing.T) {
	deadSock := filepath.Join(t.TempDir(), "none.sock")
	cases := []struct {
		name     string
		args     []string
		wantExit bool
		wantCode int
	}{
		{"version", []string{"-version"}, false, 0},
		// no cliDir/cliVersion: -install still prints its facts JSON and returns
		{"install no cli work", []string{"-install"}, false, 0},
		// a missing daemon is a silent no-op for -stop (exit 0 by returning)
		{"stop missing daemon", []string{"-stop", "-socket", deadSock}, false, 0},
		// -bridge to a dead socket is a hard error
		{"bridge dead socket", []string{"-bridge", "-socket", deadSock}, true, 1},
		// no mode flag at all: usage error, exit 2
		{"no mode flag", nil, true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, exited := runMain(t, tc.args...)
			if exited != tc.wantExit || (exited && code != tc.wantCode) {
				t.Errorf("main %v: exited=%v code=%d, want exited=%v code=%d",
					tc.args, exited, code, tc.wantExit, tc.wantCode)
			}
		})
	}
}

// NOTE deliberately untested here: main's -bridge happy path (the lone
// `return` after runBridge succeeds). runBridge returns on the FIRST copy
// finishing and leaks the other copy goroutine, which still reads the
// os.Stdout/os.Stdin globals — so any runMain-style swap-and-restore of those
// globals races with the leaked goroutine (caught by -race in CI). The happy
// path is covered by bridge_test.go against the real fds.
