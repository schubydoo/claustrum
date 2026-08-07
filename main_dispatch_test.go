package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// runMain drives the real main() with a synthetic argv. main registers its
// flags on the global flag.CommandLine (already populated by the previous
// call, and by the testing package), so each run gets a fresh FlagSet; Version
// and BuildTime are restored because resolveVersion may stamp them from the
// test binary's own build info; stdout is parked on /dev/null so -version and
// -install output doesn't pollute the test log.
//
// The -install and -serve arms of main() also WRITE package globals
// (cliProbeTimeout, maxCLIBytes, maxExtractBytes) and never restore them, so
// without the save/restore below a `-install` run here leaks its resolved values
// into every later test. Reproduced 2026-08-07: with a claustrum.conf holding
// `cli-probe-timeout = 30s` beside a pre-built test binary, 4 of 6 -test.shuffle
// seeds failed TestCLIProbeTimeoutDefaultsOff with "default = 30s"; the two that
// passed were the seeds that ran the assertion first, and the same seeds with no
// conf present passed. Not reachable under plain `go test` (loadConfig reads
// beside os.Executable(), the build-cache temp dir), but it goes live with
// -shuffle, with a pre-built binary run from a directory holding a conf, or as
// soon as a case here passes one of those flags.
// lastMainFlagSet is the FlagSet the most recent runMain handed to main(), kept
// after runMain restores flag.CommandLine so a test can inspect flag defaults.
var lastMainFlagSet *flag.FlagSet

func runMain(t *testing.T, args ...string) (code int, exited bool) {
	t.Helper()
	stubOsExit(t)
	oldArgs, oldFS := os.Args, flag.CommandLine
	oldVersion, oldBuildTime := Version, BuildTime
	// t.Cleanup, NOT the defer below: the defer runs when runMain returns, which
	// would put the globals back before the caller can assert on what main()
	// actually resolved. t.Cleanup still contains the leak to this one test.
	oldProbe, oldMaxCLI, oldMaxExtract := cliProbeTimeout, maxCLIBytes, maxExtractBytes
	t.Cleanup(func() {
		cliProbeTimeout, maxCLIBytes, maxExtractBytes = oldProbe, oldMaxCLI, oldMaxExtract
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
