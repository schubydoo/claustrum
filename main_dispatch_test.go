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
	// gitTimeout joined this list with D5's flip: the -serve arm writes it too, so
	// without the restore a -serve case here would leak a deadline into every later
	// test — and the tests that assert the shipped default is 0 would fail under
	// -shuffle depending on the seed, exactly as the cli-probe-timeout leak did.
	oldGitTimeout := gitTimeout
	// filesReadRegularOnly joined for the same reason with D4's flip, and its leak
	// would be the loudest of the set: TestFilesReadRegularOnlyDefaultIsOff and both
	// socket goldens read the package var, so a -serve case leaking `true` turns
	// three unrelated tests into seed-dependent failures under -shuffle.
	oldRegularOnly := filesReadRegularOnly
	// lddProbeTimeout joined with D14's flip. Same -install arm as cliProbeTimeout,
	// and the two are one letter apart in both flag and global, so a leak here would
	// masquerade as a cli-probe-timeout leak while reading TestLddProbeTimeoutDefaultIsOff.
	oldLddTimeout := lddProbeTimeout
	t.Cleanup(func() {
		cliProbeTimeout, cliDownloadTimeout = oldProbe, oldDownload
		maxCLIBytes, maxExtractBytes = oldMaxCLI, oldMaxExtract
		gitTimeout = oldGitTimeout
		filesReadRegularOnly = oldRegularOnly
		lddProbeTimeout = oldLddTimeout
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
	if _, exited := runMain(t, "-install", "-cli-probe-timeout", "7s",
		"-cli-download-timeout", "42s", "-libc-probe-timeout", "23s"); exited {
		t.Fatal("-install should return, not exit")
	}
	if cliProbeTimeout != 7*time.Second {
		t.Errorf("cliProbeTimeout = %s, want 7s (-cli-probe-timeout must reach it, not the download or libc global)", cliProbeTimeout)
	}
	if cliDownloadTimeout != 42*time.Second {
		t.Errorf("cliDownloadTimeout = %s, want 42s (-cli-download-timeout must reach it, not the probe global)", cliDownloadTimeout)
	}
	// ⚠️ The sharpest of the three. -libc-probe-timeout and -cli-probe-timeout differ
	// by one letter, both are flag.Duration, and both are resolved two lines apart in
	// main's -install arm — so a swap compiles, vets, and passes every test that
	// exercises either deadline in isolation. Only distinct values here catch it.
	if lddProbeTimeout != 23*time.Second {
		t.Errorf("lddProbeTimeout = %s, want 23s (-libc-probe-timeout must reach it, not the CLI probe global)", lddProbeTimeout)
	}
	// The DECLARED default, the one-character regression the other assertions miss:
	// flipping flag.Duration's default argument back to 5*time.Second survives every
	// explicit-value test in the suite while shipping a binary that deadlines every
	// linux -install.
	f := lastMainFlagSet.Lookup("libc-probe-timeout")
	if f == nil {
		t.Fatal("main() did not register -libc-probe-timeout")
	}
	if f.DefValue != "0s" {
		t.Fatalf("-libc-probe-timeout declared default = %q, want \"0s\" (no deadline = reference parity)", f.DefValue)
	}
}

// runMain restores the globals main() writes, and that restore is load-bearing:
// without it a -install run here leaks its resolved values into every later test.
// Asserting it needs the cleanup to have RUN, so the call goes in a subtest and
// the assertion follows it — t.Cleanup fires at the subtest's end.
func TestRunMainRestoresInstallGlobals(t *testing.T) {
	oldProbe, oldDownload := cliProbeTimeout, cliDownloadTimeout
	oldMaxCLI, oldMaxExtract, oldLdd := maxCLIBytes, maxExtractBytes, lddProbeTimeout
	cliProbeTimeout, cliDownloadTimeout = 3*time.Second, 4*time.Second
	maxCLIBytes, maxExtractBytes, lddProbeTimeout = 11, 22, 6*time.Second
	t.Cleanup(func() {
		cliProbeTimeout, cliDownloadTimeout = oldProbe, oldDownload
		maxCLIBytes, maxExtractBytes, lddProbeTimeout = oldMaxCLI, oldMaxExtract, oldLdd
	})
	t.Run("inner", func(t *testing.T) {
		if _, exited := runMain(t, "-install", "-cli-probe-timeout", "9s",
			"-cli-download-timeout", "8s", "-max-cli-bytes", "99",
			"-libc-probe-timeout", "7s"); exited {
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
		// lddProbeTimeout joined runMain's save/restore with D14's flip and had no
		// row here — repeating exactly the omission TestRunMainRestoresServeGlobals
		// was written to close for gitTimeout and filesReadRegularOnly. Measured
		// without this row: dropping the restore leaks 7s and is caught on only
		// 2 of 10 shuffle seeds, and CI passes no -shuffle at all, so it would
		// never have gone red.
		{"lddProbeTimeout", lddProbeTimeout, 6 * time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v after runMain, want %v restored", c.name, c.got, c.want)
		}
	}
}

// The -serve half of the same contract, and it needs its own test rather than two
// more rows above: the -install arm never writes gitTimeout or filesReadRegularOnly,
// so seeding them there and asserting they survive passes even with runMain's
// restore lines deleted. Only an inner run that actually WRITES them can detect a
// missing restore. gitTimeout arrived with D5's flip and filesReadRegularOnly with
// D4's; both were added to runMain's save/restore without an assertion, and this is
// that assertion.
//
// A leak here is not cosmetic: TestGitTimeoutDefaultIsOff and
// TestFilesReadRegularOnlyDefaultIsOff both read the package vars, so a -serve case
// leaking its values turns them into seed-dependent failures under -shuffle.
func TestRunMainRestoresServeGlobals(t *testing.T) {
	t.Setenv(daemonChildEnv, "1")
	// Capture on entry rather than restoring the literals 0/0/false: writing the
	// shipped defaults back by hand couples this cleanup to what those defaults
	// happen to be, which is precisely the thing three other tests exist to pin.
	oldGit, oldExtract, oldRegular := gitTimeout, maxExtractBytes, filesReadRegularOnly
	gitTimeout, maxExtractBytes, filesReadRegularOnly = 5*time.Second, 33, true
	t.Cleanup(func() {
		gitTimeout, maxExtractBytes, filesReadRegularOnly = oldGit, oldExtract, oldRegular
	})
	t.Run("inner", func(t *testing.T) {
		// Distinct from the seeds above on purpose: equal values would pass whether
		// or not the restore ran.
		if _, exited := runMain(t, "-serve",
			"-socket", filepath.Join(t.TempDir(), "s.sock"),
			"-git-timeout", "44s", "-max-extract-bytes", "77",
			"-files-read-regular-only=false"); !exited {
			t.Fatal("-serve with no token source should exit, not return")
		}
	})
	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"gitTimeout", gitTimeout, 5 * time.Second},
		{"maxExtractBytes", maxExtractBytes, int64(33)},
		{"filesReadRegularOnly", filesReadRegularOnly, true},
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

// The -serve arm's flag-to-global wiring, the mirror of
// TestInstallArmWiresEachFlagToItsOwnGlobal above. Without it,
// `_ = cfg.effectiveGitTimeout(...)` — the flag and config key fully disconnected
// from the runtime var — builds, vets and passes the whole suite, because
// effectiveGitTimeout is tested in isolation and gitTimeout is tested in
// isolation and nothing joins them.
//
// The daemon-child sentinel makes this safe to drive: runServe takes the child
// path, finds no token source, and exits 1 within milliseconds — after main() has
// already resolved both globals. No socket is bound and nothing daemonizes.
//
// Distinct values on purpose: equal ones would pass under a swap.
func TestServeArmWiresGitTimeoutAndExtractCap(t *testing.T) {
	t.Setenv(daemonChildEnv, "1")
	if _, exited := runMain(t, "-serve",
		"-socket", filepath.Join(t.TempDir(), "s.sock"),
		"-git-timeout", "37s", "-max-extract-bytes", "4096",
		"-files-read-regular-only"); !exited {
		t.Fatal("-serve with no token source should exit, not return")
	}
	if gitTimeout != 37*time.Second {
		t.Errorf("gitTimeout = %s, want 37s — -git-timeout must reach the runtime var", gitTimeout)
	}
	if maxExtractBytes != 4096 {
		t.Errorf("maxExtractBytes = %d, want 4096 — the three -serve knobs must not be crossed", maxExtractBytes)
	}
	if !filesReadRegularOnly {
		t.Error("filesReadRegularOnly = false, want true — -files-read-regular-only must reach the runtime var")
	}
	// The DECLARED default, not the package var and not the resolver — the same
	// assertion -cli-probe-timeout and -cli-download-timeout each carry. Moving a
	// 60s into flag.Duration's default argument survives every other gitTimeout
	// assertion in the suite while shipping a binary that deadlines every -serve
	// git. Declared, not resolved: a real claustrum.conf beside the binary sets the
	// resolved value legitimately.
	f := lastMainFlagSet.Lookup("git-timeout")
	if f == nil {
		t.Fatal("main() did not register -git-timeout")
	}
	if f.DefValue != "0s" {
		t.Fatalf("-git-timeout declared default = %q, want \"0s\" (no deadline = reference parity)", f.DefValue)
	}
	// Same assertion for D4's flag, and it is the one most easily undone: flipping
	// flag.Bool's default argument to true is a one-character edit that every
	// explicit-assignment test in the suite survives.
	f = lastMainFlagSet.Lookup("files-read-regular-only")
	if f == nil {
		t.Fatal("main() did not register -files-read-regular-only")
	}
	if f.DefValue != "false" {
		t.Fatalf("-files-read-regular-only declared default = %q, want \"false\" (guard off = reference parity)", f.DefValue)
	}
}
