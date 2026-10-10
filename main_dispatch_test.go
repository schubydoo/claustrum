package main

import (
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
// (cliDownloadTimeout, maxCLIBytes, maxExtractBytes) and never restore them, so
// without the save/restore below a `-install` run here leaks its resolved values
// into every later test. Reproduced 2026-08-07 with the cli-probe-timeout key,
// which is retired since. A claustrum.conf beside a pre-built test binary made 4
// of 6 -test.shuffle seeds fail a default-is-off assertion. Plain `go test` does
// not reach it: loadConfig reads beside os.Executable(), the build-cache temp
// dir. It goes live with -shuffle and with a pre-built binary that runs from a
// directory with a conf. It also goes live when a case here passes such a flag.
func runMain(t *testing.T, args ...string) (code int, exited bool) {
	t.Helper()
	stubOsExit(t)
	oldArgs, oldFS := os.Args, flag.CommandLine
	oldVersion, oldBuildTime := Version, BuildTime
	// t.Cleanup, NOT the defer below: the defer runs when runMain returns, which
	// would put the globals back before the caller can assert on what main()
	// actually resolved. t.Cleanup still contains the leak to this one test.
	oldDownload := cliDownloadTimeout
	oldMaxCLI, oldMaxExtract := maxCLIBytes, maxExtractBytes
	// gitTimeout joined this list with D5's flip: the -serve arm writes it too, so
	// without the restore a -serve case here would leak a deadline into every later
	// test — and the tests that assert the shipped default is 0 would fail under
	// -shuffle depending on the seed, exactly as the cli-probe-timeout leak did.
	oldGitTimeout := gitTimeout
	t.Cleanup(func() {
		cliDownloadTimeout = oldDownload
		maxCLIBytes, maxExtractBytes = oldMaxCLI, oldMaxExtract
		gitTimeout = oldGitTimeout
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
// the deadline directly (as the fetchToFile tests do) exercises the READ.
// Asserting flag.DefValue covers the DECLARED default. The line that joins them
// was unasserted: main() resolves each flag into its own global.
//
// Three duration flags sit side by side in main's -install arm. Two of them are
// deprecated no-ops: -cli-probe-timeout (D11, retired) and -libc-probe-timeout
// (D14, retired). Only -cli-download-timeout sets a global. A crossed line
// compiles and passes every isolated test. So this test pins that neither
// deprecated flag reaches any bound, and that each logs its own warning once.
//
// Distinct values on purpose: equal ones would pass under a swap.
func TestInstallArmWiresEachFlagToItsOwnGlobal(t *testing.T) {
	buf := captureLogBuf(t)
	if _, exited := runMain(t, "-install", "-cli-probe-timeout", "7s",
		"-cli-download-timeout", "42s", "-libc-probe-timeout", "23s"); exited {
		t.Fatal("-install should return, not exit")
	}
	if cliDownloadTimeout != 42*time.Second {
		t.Errorf("cliDownloadTimeout = %s, want 42s (-cli-download-timeout must reach it, and no other flag may)", cliDownloadTimeout)
	}
	// ⚠️ The sharpest checks. The four bounds below are the reference's fixed
	// values, and no flag reaches any of them.
	if cliRunBound != 30*time.Second {
		t.Errorf("cliRunBound = %s, want the fixed 30s (no flag may reach it)", cliRunBound)
	}
	if cliFirstRunBound != 120*time.Second {
		t.Errorf("cliFirstRunBound = %s, want the fixed 120s (no flag may reach it)", cliFirstRunBound)
	}
	if lddProbeTimeout != 5*time.Second {
		t.Errorf("lddProbeTimeout = %s, want the fixed 5s (no flag may reach it)", lddProbeTimeout)
	}
	if installHeaderTimeout != 60*time.Second {
		t.Errorf("installHeaderTimeout = %s, want the fixed 60s (no flag may reach it)", installHeaderTimeout)
	}
	if n := strings.Count(buf.String(), "-libc-probe-timeout is deprecated and ignored"); n != 1 {
		t.Errorf("-libc-probe-timeout deprecation warnings = %d, want exactly 1; log:\n%s", n, buf.String())
	}
	// Both texts of the deprecated flag name the direct run. A launcher run has
	// other bounds.
	if !strings.Contains(buf.String(), "The direct --version run always uses its 30s and 120s bounds") {
		t.Errorf("the -cli-probe-timeout warning does not name the direct run; log:\n%s", buf.String())
	}
	if f := lastMainFlagSet.Lookup("cli-probe-timeout"); f == nil || !strings.Contains(f.Usage, "The direct <cli> --version run of -install") {
		t.Errorf("the -cli-probe-timeout help text does not name the direct run: %+v", f)
	}
	if n := strings.Count(buf.String(), "-cli-probe-timeout is deprecated and ignored"); n != 1 {
		t.Errorf("-cli-probe-timeout deprecation warnings = %d, want exactly 1; log:\n%s", n, buf.String())
	}
}

// Each deprecation warning fires for the flag or the config key, once, and not
// at all when neither is present.
func TestWarnDeprecatedProbeFlags(t *testing.T) {
	for name, warn := range map[string]func(bool, bool){
		"-libc-probe-timeout":      warnDeprecatedLibcProbe,
		"-cli-probe-timeout":       warnDeprecatedCLIProbe,
		"-files-read-regular-only": warnDeprecatedReadRegularOnly,
	} {
		for _, tc := range []struct {
			flagSet, keySeen bool
			want             int
		}{{false, false, 0}, {true, false, 1}, {false, true, 1}, {true, true, 1}} {
			var buf syncBuffer
			oldOut := log.Writer()
			log.SetOutput(&buf)
			warn(tc.flagSet, tc.keySeen)
			log.SetOutput(oldOut)
			if n := strings.Count(buf.String(), name+" is deprecated and ignored"); n != tc.want {
				t.Errorf("%s flag=%v key=%v: %d warnings, want %d", name, tc.flagSet, tc.keySeen, n, tc.want)
			}
		}
	}
}

// The claustrum.conf key reaches the same warning as the flag. With
// `cli-probe-timeout = 20s` in the file and no flag, -install logs the
// -cli-probe-timeout warning once and no -libc-probe-timeout warning.
func TestInstallWarnsForTheDeprecatedCLIProbeConfigKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("cli-probe-timeout = 20s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubOsExecutable(t, filepath.Join(dir, "claustrum"), nil)
	buf := captureLogBuf(t)
	if _, exited := runMain(t, "-install"); exited {
		t.Fatal("-install should return, not exit")
	}
	if n := strings.Count(buf.String(), "-cli-probe-timeout is deprecated and ignored"); n != 1 {
		t.Errorf("-cli-probe-timeout deprecation warnings = %d, want exactly 1; log:\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "-libc-probe-timeout is deprecated") {
		t.Errorf("the cli-probe-timeout key logged the -libc-probe-timeout warning:\n%s", buf.String())
	}
}

// The claustrum.conf key reaches the same warning as the flag. With
// `files-read-regular-only = true` in the file and no flag, -serve logs the
// warning once. A plain -serve logs none.
func TestServeWarnsForTheDeprecatedReadRegularOnlyConfigKey(t *testing.T) {
	for _, tc := range []struct {
		name, conf string
		want       int
	}{{"key", "files-read-regular-only = true\n", 1}, {"no key", "git-timeout = 0s\n", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(daemonChildEnv, "1")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(tc.conf), 0o644); err != nil {
				t.Fatal(err)
			}
			stubOsExecutable(t, filepath.Join(dir, "claustrum"), nil)
			buf := captureLogBuf(t)
			if _, exited := runMain(t, "-serve", "-socket", filepath.Join(t.TempDir(), "s.sock")); !exited {
				t.Fatal("-serve with no token source should exit, not return")
			}
			if n := strings.Count(buf.String(), "-files-read-regular-only is deprecated and ignored"); n != tc.want {
				t.Errorf("deprecation warnings = %d, want %d; log:\n%s", n, tc.want, buf.String())
			}
		})
	}
}

// A plain -install with neither deprecated flag logs neither warning.
func TestInstallWithoutDeprecatedFlagsLogsNoWarning(t *testing.T) {
	buf := captureLogBuf(t)
	if _, exited := runMain(t, "-install"); exited {
		t.Fatal("-install should return, not exit")
	}
	if strings.Contains(buf.String(), "deprecated and ignored") {
		t.Errorf("a plain -install logged a deprecation warning:\n%s", buf.String())
	}
}

// runMain restores the globals main() writes, and that restore is load-bearing:
// without it a -install run here leaks its resolved values into every later test.
// Asserting it needs the cleanup to have RUN, so the call goes in a subtest and
// the assertion follows it — t.Cleanup fires at the subtest's end.
func TestRunMainRestoresInstallGlobals(t *testing.T) {
	oldDownload := cliDownloadTimeout
	oldMaxCLI, oldMaxExtract := maxCLIBytes, maxExtractBytes
	cliDownloadTimeout = 4 * time.Second
	maxCLIBytes, maxExtractBytes = 11, 22
	t.Cleanup(func() {
		cliDownloadTimeout = oldDownload
		maxCLIBytes, maxExtractBytes = oldMaxCLI, oldMaxExtract
	})
	t.Run("inner", func(t *testing.T) {
		if _, exited := runMain(t, "-install",
			"-cli-download-timeout", "8s", "-max-cli-bytes", "99"); exited {
			t.Fatal("-install should return, not exit")
		}
	})
	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"cliDownloadTimeout", cliDownloadTimeout, 4 * time.Second},
		{"maxCLIBytes", maxCLIBytes, int64(11)},
		{"maxExtractBytes", maxExtractBytes, int64(22)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v after runMain, want %v restored", c.name, c.got, c.want)
		}
	}
}

// The -serve half of the same contract, and it needs its own test rather than two
// more rows above: the -install arm never writes gitTimeout, so seeding it there
// and asserting it survives passes even with runMain's restore lines deleted. Only
// an inner run that actually WRITES it can detect a missing restore.
//
// A leak here is not cosmetic: TestGitTimeoutDefaultIsOff reads the package var,
// so a -serve case leaking its value turns it into a seed-dependent failure under
// -shuffle.
func TestRunMainRestoresServeGlobals(t *testing.T) {
	t.Setenv(daemonChildEnv, "1")
	// Capture on entry rather than restoring the literals 0/0: writing the
	// shipped defaults back by hand couples this cleanup to what those defaults
	// happen to be, which is precisely the thing other tests exist to pin.
	oldGit, oldExtract := gitTimeout, maxExtractBytes
	gitTimeout, maxExtractBytes = 5*time.Second, 33
	t.Cleanup(func() {
		gitTimeout, maxExtractBytes = oldGit, oldExtract
	})
	t.Run("inner", func(t *testing.T) {
		// Distinct from the seeds above on purpose: equal values would pass whether
		// or not the restore ran.
		if _, exited := runMain(t, "-serve",
			"-socket", filepath.Join(t.TempDir(), "s.sock"),
			"-git-timeout", "44s", "-max-extract-bytes", "77"); !exited {
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
		// -stop exits 0 in every state: a missing daemon prints "none" and returns
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

// Mode precedence: -bridge and -serve win over -stop. With -stop -bridge the
// references run the bridge, and with -stop -serve they run serve (measured
// against f6010b97 and 90fca6e6 on a Linux VM). Each winner here exits 1, where
// -stop returns (exit 0), so the exit tells the modes apart.
//
// SAFETY: every socket is in a temp dir. If -stop wins by mistake, it finds no
// daemon there and prints "none". The -serve row takes the daemon-child path,
// which exits 1 with no token source before it binds anything.
func TestStopYieldsToBridgeAndServe(t *testing.T) {
	deadSock := filepath.Join(t.TempDir(), "none.sock")
	t.Run("stop+bridge runs the bridge", func(t *testing.T) {
		code, exited := runMain(t, "-stop", "-bridge", "-socket", deadSock)
		if !exited || code != 1 {
			t.Errorf("-stop -bridge: exited=%v code=%d, want the bridge's dial failure (exit 1)", exited, code)
		}
	})
	t.Run("stop+serve runs serve", func(t *testing.T) {
		t.Setenv(daemonChildEnv, "1")
		t.Setenv(tokenPipeEnv, "")
		code, exited := runMain(t, "-stop", "-serve", "-socket", deadSock)
		if !exited || code != 1 {
			t.Errorf("-stop -serve: exited=%v code=%d, want serve's missing-token exit 1", exited, code)
		}
	})
}

// NOTE deliberately untested here: main's -bridge happy path (the lone
// `return` after runBridge succeeds). bridge_test.go and
// integration_eof_test.go test runBridge itself, with os.Stdin and os.Stdout
// swapped for pipes.

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
	buf := captureLogBuf(t)
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
		t.Errorf("maxExtractBytes = %d, want 4096 — the two -serve knobs must not be crossed", maxExtractBytes)
	}
	// -files-read-regular-only is a deprecated no-op (D4, retired). It logs its
	// warning once.
	if n := strings.Count(buf.String(), "-files-read-regular-only is deprecated and ignored"); n != 1 {
		t.Errorf("-files-read-regular-only deprecation warnings = %d, want exactly 1; log:\n%s", n, buf.String())
	}
	// The DECLARED default, not the package var and not the resolver — the same
	// assertion -cli-download-timeout carries. Moving a
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
	if lastMainFlagSet.Lookup("files-read-regular-only") == nil {
		t.Fatal("main() did not register the deprecated -files-read-regular-only")
	}
}

// main resolves the home directory for the modes that need it (install/serve/bridge/
// stop and the no-mode error path) and exits 1 when it cannot. -version and
// -probe-cli dispatch BEFORE home resolution and are unaffected — see
// TestProbeCLIDispatchSurvivesUnresolvableHome. os.UserHomeDir reads HOME on Unix and
// USERPROFILE on Windows; clearing both makes it fail on every CI leg. A bare
// invocation reaches the home resolution before the no-mode error, so its osExit(1)
// fires first.
func TestMainExitsWhenHomeUnresolvable(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	code, exited := runMain(t)
	if !exited || code != 1 {
		t.Errorf("main with no resolvable home: exited=%v code=%d, want exited=true code=1", exited, code)
	}
}

// `-install -cli-version <v>` with no -cli-dir needs the home folder for its
// default cli folder. With no home folder it exits 2, prints the reason on stderr
// and prints nothing on stdout. Measured on a Linux VM against f6010b97 and
// 89cb6289 (row H07, HOME not in the environment). The stderr prefix is
// claustrum's own. Every other case keeps exit 1, and no row measures those.
func TestInstallExitsTwoWithoutHomeForTheDefaultCLIDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"version, no dir (H07)", []string{"-install", "-cli-version", "9.9.9"}, 2},
		{"version, empty dir", []string{"-install", "-cli-version", "9.9.9", "-cli-dir", ""}, 2},
		{"version and dir", []string{"-install", "-cli-version", "9.9.9", "-cli-dir", t.TempDir()}, 1},
		{"no version", []string{"-install"}, 1},
		{"bridge", []string{"-bridge", "-cli-version", "9.9.9"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldErr := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = w
			code, exited := runMain(t, tc.args...)
			_ = w.Close()
			os.Stderr = oldErr
			errOut, _ := io.ReadAll(r)
			_ = r.Close()
			if !exited || code != tc.want {
				t.Errorf("exited=%v code=%d, want exit %d", exited, code, tc.want)
			}
			if !strings.HasPrefix(string(errOut), "claustrum: cannot resolve home directory: ") ||
				strings.Count(string(errOut), "\n") != 1 {
				t.Errorf("stderr = %q, want one line with the home-directory reason", errOut)
			}
			if want := "claustrum: cannot resolve home directory: $HOME is not defined\n"; runtime.GOOS == "linux" && string(errOut) != want {
				t.Errorf("stderr = %q, want %q", errOut, want)
			}
		})
	}
}

// main hands the home folder to runInstall, so `-install -cli-version <v>` with no
// -cli-dir works in <home>/.claude/remote/ccd-cli. The run ends with the
// "missing" answer and still makes the folder chain (rows H01 and H10).
func TestInstallDefaultCLIDirComesFromTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	if runtime.GOOS == "windows" {
		// USERPROFILE is the home folder on Windows, and HOME is not read (H10).
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOME", other)
	} else {
		t.Setenv("HOME", home)
	}
	if _, exited := runMain(t, "-install", "-cli-version", "9.9.9"); exited {
		t.Fatal("-install should return, not exit")
	}
	if fi, err := os.Stat(filepath.Join(home, ".claude", "remote", "ccd-cli")); err != nil || !fi.IsDir() {
		t.Errorf("the default cli folder was not made under the home folder: %v", err)
	}
	if ents, err := os.ReadDir(other); err != nil || len(ents) != 0 {
		t.Errorf("the other folder holds %v (err %v), want it untouched", ents, err)
	}
}

// TestSigpipeIgnoredForStdoutModes pins that -probe-cli and -install ignore SIGPIPE
// (both write structured output to stdout, fd 1, where Go's default terminates the
// process on a closed-pipe write), while -version does not. The seam counts the calls
// without touching the test process's real signal disposition.
func TestSigpipeIgnoredForStdoutModes(t *testing.T) {
	old := ignoreSigpipe
	t.Cleanup(func() { ignoreSigpipe = old })
	var calls int
	ignoreSigpipe = func() { calls++ }

	calls = 0
	runMain(t, "-probe-cli", filepath.Join(t.TempDir(), "nope"))
	if calls != 1 {
		t.Errorf("-probe-cli: ignoreSigpipe called %d times, want 1", calls)
	}

	calls = 0
	runMain(t, "-install", "-cli-dir", t.TempDir(), "-cli-version", "nope")
	if calls != 1 {
		t.Errorf("-install: ignoreSigpipe called %d times, want 1", calls)
	}

	calls = 0
	runMain(t, "-version")
	if calls != 0 {
		t.Errorf("-version: ignoreSigpipe called %d times, want 0 (only the stdout-protocol modes ignore SIGPIPE)", calls)
	}
}
