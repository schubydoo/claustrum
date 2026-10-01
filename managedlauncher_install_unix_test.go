//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// -install and -probe-cli rows of the 89cb6289 VM captures (I and P rows), on unix.
// The CLI and the launcher are symlinks to this test binary. The test sets the
// helper mode in its own env, because -install and -probe-cli run them with the
// daemon env: "cli-log" for a CLI run directly, "wrap" (then cli-log) for a CLI run
// through the launcher.

type installFixture struct {
	cliDir, cli, wrap, base string
	cliLog, wrapLog         string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	f := &installFixture{
		cliDir:  filepath.Join(d, "cli"),
		wrap:    filepath.Join(d, "bin", "wrap"),
		cliLog:  filepath.Join(d, "cli.log"),
		wrapLog: filepath.Join(d, "wrap.log"),
	}
	f.cli = filepath.Join(f.cliDir, "9.9.9")
	for _, p := range []string{f.cli, f.wrap} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, p); err != nil {
			t.Fatal(err)
		}
	}
	f.base = filepath.Join(tempManagedSettingsDir(t), managedSettingsBase)
	t.Setenv("CLAUSTRUM_TEST_CLI_LOG", f.cliLog)
	t.Setenv("CLAUSTRUM_TEST_WRAP_LOG", f.wrapLog)
	return f
}

// settings writes the base file with processWrapper v.
func (f *installFixture) settings(t *testing.T, v string) {
	t.Helper()
	writeFileMode(t, f.base, []byte(pwSettings(t, v)), 0o644)
}

// helper sets the helper mode of the next CLI or launcher run, and the mode the
// wrap launcher hands the CLI.
func helperMode(t *testing.T, mode, next string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_TEST_HELPER", mode)
	t.Setenv("CLAUSTRUM_TEST_WRAP_NEXT", next)
}

// install runs -install for 9.9.9 (plus extra opts) and returns the facts line from
// "cliWasPresent" on, which is where the launcher fields and their order live.
func (f *installFixture) install(t *testing.T, zst string) string {
	t.Helper()
	out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: zst})
	line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
	i := strings.Index(line, `"cliWasPresent"`)
	if i < 0 {
		t.Fatalf("no cliWasPresent in %q", out)
	}
	if !strings.Contains(line, `"cliPath":"`+f.cli+`",`) {
		t.Errorf("cliPath is not %s in %q", f.cli, line)
	}
	return line[i:]
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func setManagedRunBound(t *testing.T, d time.Duration) {
	t.Helper()
	old := managedRunBound
	managedRunBound = d
	t.Cleanup(func() { managedRunBound = old })
}

func setManagedFirstRunBound(t *testing.T, d time.Duration) {
	t.Helper()
	old := managedFirstRunBound
	managedFirstRunBound = d
	t.Cleanup(func() { managedFirstRunBound = old })
}

func denySettingsRead(t *testing.T) {
	t.Helper()
	old := readManagedSettingsFile
	readManagedSettingsFile = func(p string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EACCES}
	}
	t.Cleanup(func() { readManagedSettingsFile = old })
}

// TestInstallGateAndDirectRuns pins I0, I8 and I1: only CLAUDE_SSH_MANAGED_LAUNCHER=1
// adds the launcher fields; without it the CLI runs directly even with a usable
// launcher; with it and no settings the status is none, the CLI runs directly and
// keeps the gate in its env (ENV-5).
func TestInstallGateAndDirectRuns(t *testing.T) {
	f := newInstallFixture(t)
	f.settings(t, f.wrap)
	helperMode(t, "cli-log", "")
	for _, gate := range []string{"", "true", "1 "} {
		t.Setenv(managedLauncherGateEnv, gate)
		_ = os.Remove(f.cliLog)
		if got := f.install(t, ""); got != `"cliWasPresent":true}`+"\n" {
			t.Errorf("gate %q: facts tail %q, want no launcher fields", gate, got)
		}
		if got, want := readLog(t, f.cliLog), "CLI --version gate="+gate+"\n"; got != want {
			t.Errorf("gate %q: CLI log %q, want a direct run %q", gate, got, want)
		}
	}
	if readLog(t, f.wrapLog) != "" {
		t.Error("the launcher ran without the gate")
	}

	t.Setenv(managedLauncherGateEnv, "1")
	_ = os.Remove(f.base)
	_ = os.Remove(f.cliLog)
	if got := f.install(t, ""); got != `"cliWasPresent":true,"launcherStatus":"none"}`+"\n" {
		t.Errorf("I1: facts tail %q", got)
	}
	if got := readLog(t, f.cliLog); got != "CLI --version gate=1\n" {
		t.Errorf("I1: CLI log %q, want a direct run that keeps the gate", got)
	}
}

// TestInstallThroughLauncher pins I2: a usable launcher runs the CLI once, as
// `<launcher> <cli> --version`, with no direct run, and the gate does not reach it.
// The facts carry launcherStatus, launcher and launcherSource in that order.
func TestInstallThroughLauncher(t *testing.T) {
	f := newInstallFixture(t)
	f.settings(t, f.wrap)
	helperMode(t, "wrap", "cli-log")
	t.Setenv(managedLauncherGateEnv, "1")
	want := `"cliWasPresent":true,"launcherStatus":"usable","launcher":["` + f.wrap + `"],"launcherSource":"` + f.base + `"}` + "\n"
	if got := f.install(t, ""); got != want {
		t.Errorf("I2: facts tail\n got %q\nwant %q", got, want)
	}
	if got, want := readLog(t, f.wrapLog), "LAUNCH "+f.cli+" --version\n"; got != want {
		t.Errorf("I2: launcher log %q, want %q", got, want)
	}
	if got := readLog(t, f.cliLog); got != "CLI --version gate=\n" {
		t.Errorf("I2: CLI log %q, want one run through the launcher without the gate", got)
	}
}

// TestInstallRefusedLauncherRunsNothing pins I3 and I4 (and INS-14): an unusable or
// unreadable answer runs neither the CLI nor the launcher, keeps cliWasPresent true
// and adds no cliError, and -install logs no [LauncherHandler] line.
func TestInstallRefusedLauncherRunsNothing(t *testing.T) {
	logs := captureLogBuf(t)
	f := newInstallFixture(t)
	helperMode(t, "wrap", "cli-log")
	t.Setenv(managedLauncherGateEnv, "1")

	f.settings(t, "wrap")
	want := `"cliWasPresent":true,"launcherStatus":"unusable","launcherSource":"` + f.base + `","launcherReason":"the launcher must be an absolute path, not a bare name resolved via PATH"}` + "\n"
	if got := f.install(t, ""); got != want {
		t.Errorf("I3: facts tail\n got %q\nwant %q", got, want)
	}

	f.settings(t, f.wrap)
	denySettingsRead(t)
	want = `"cliWasPresent":true,"launcherStatus":"unreadable","launcherPath":"` + f.base + `","launcherReason":"permission denied"}` + "\n"
	if got := f.install(t, ""); got != want {
		t.Errorf("I4: facts tail\n got %q\nwant %q", got, want)
	}
	if readLog(t, f.cliLog)+readLog(t, f.wrapLog) != "" {
		t.Error("a refused launcher must run nothing")
	}
	if strings.Contains(logs.String(), "[LauncherHandler]") {
		t.Errorf("-install must not log a [LauncherHandler] line:\n%s", logs.String())
	}
}

// TestInstallNoCliPathAddsNoLauncherField pins the rows without -cli-version, with
// and without -cli-dir (Linux VM, 89cb6289). With the gate, the facts line of an
// empty cliPath carries no launcher field. That holds for a usable launcher, an
// unusable one and none. Nothing runs.
func TestInstallNoCliPathAddsNoLauncherField(t *testing.T) {
	f := newInstallFixture(t)
	helperMode(t, "wrap", "cli-log")
	t.Setenv(managedLauncherGateEnv, "1")
	for _, state := range []string{"usable", "unusable", "none"} {
		switch state {
		case "usable":
			f.settings(t, f.wrap)
		case "unusable":
			f.settings(t, "wrap")
		case "none":
			if err := os.Remove(f.base); err != nil {
				t.Fatal(err)
			}
		}
		for _, o := range []installOpts{{}, {cliDir: f.cliDir}} {
			out := captureInstallOutput(t, o)
			if want := `"cliPath":"","cliWasPresent":false}` + "\n"; !strings.HasSuffix(out, want) {
				t.Errorf("%s, cliDir %q: facts line %q, want the end %q", state, o.cliDir, out, want)
			}
		}
	}
	if readLog(t, f.cliLog)+readLog(t, f.wrapLog) != "" {
		t.Error("an install with no CLI path must run nothing")
	}
}

// TestInstallLauncherRunFails pins I5, I7 and I11: a failed launcher run is
// probe_failed with the reason and the capped stderr; the CLI file stays,
// cliWasPresent stays true and there is no cliError. A signal reads "terminated by
// SIGTERM" and an empty stderr omits launcherStderr.
func TestInstallLauncherRunFails(t *testing.T) {
	f := newInstallFixture(t)
	f.settings(t, f.wrap)
	t.Setenv(managedLauncherGateEnv, "1")
	head := `"cliWasPresent":true,"launcherStatus":"probe_failed","launcher":["` + f.wrap + `"],"launcherSource":"` + f.base + `",`

	helperMode(t, "fail-launcher", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "L5: to stderr\n")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "3")
	if got, want := f.install(t, ""), head+`"launcherReason":"exit status 3","launcherStderr":"L5: to stderr\n"}`+"\n"; got != want {
		t.Errorf("I5: facts tail\n got %q\nwant %q", got, want)
	}

	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", strings.Repeat("E", 10000))
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "1")
	want := head + `"launcherReason":"exit status 1","launcherStderr":"` + strings.Repeat("E", 8192) + `\n[` + "\xe2\x80\xa6" + ` 1808 more bytes]"}` + "\n"
	if got := f.install(t, ""); got != want {
		t.Errorf("I7: facts tail (len %d, want %d) ends %q, want %q", len(got), len(want), got[max(0, len(got)-60):], want[len(want)-60:])
	}

	helperMode(t, "selfterm", "")
	if got, want := f.install(t, ""), head+`"launcherReason":"terminated by SIGTERM"}`+"\n"; got != want {
		t.Errorf("I11: facts tail\n got %q\nwant %q", got, want)
	}
	if !isRegularFile(f.cli) {
		t.Error("the CLI file must stay")
	}
}

// TestInstallLauncherRunStopped pins I6 with a short bound: a launcher run that does
// not end is stopped, cliWasPresent is false with the unresponsive cliError and
// cliUnresponsive, the status is unresponsive, and the CLI file stays.
func TestInstallLauncherRunStopped(t *testing.T) {
	f := newInstallFixture(t)
	f.settings(t, f.wrap)
	t.Setenv(managedLauncherGateEnv, "1")
	helperMode(t, "slow:5", "")
	setManagedRunBound(t, 300*time.Millisecond)
	start := time.Now()
	want := `"cliWasPresent":false,"cliError":"cli unresponsive: the installed Claude Code binary was started through the host's managed launcher ` + f.wrap +
		` and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish","cliUnresponsive":true,"launcherStatus":"unresponsive","launcher":["` +
		f.wrap + `"],"launcherSource":"` + f.base + `","launcherReason":"did not exit within 33s and was stopped"}` + "\n"
	if got := f.install(t, ""); got != want {
		t.Errorf("I6: facts tail\n got %q\nwant %q", got, want)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("the stop took %v, want about the 300ms bound", d)
	}
	if !isRegularFile(f.cli) {
		t.Error("the CLI file must stay")
	}
}

// TestInstallFreshExtractThroughLauncher pins I10: a fresh install extracts, then
// runs the CLI once through the launcher, and reports cliWasPresent false with the
// usable fields. The launcher gets the staged file, not the final path (GAP-3, an
// older gap of claustrum). The run right after the extract has its own bound: a
// launcher that outlasts the cache-hit bound still ends as usable (row H4 measured
// that with -cli-url). A run stopped at the first-run bound reports the
// unresponsive cliError and names 123s in launcherReason. The CLI is installed all
// the same (row H2, Linux VM).
func TestInstallFreshExtractThroughLauncher(t *testing.T) {
	f := newInstallFixture(t)
	if err := os.Remove(f.cli); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(t.TempDir(), "stub.zst")
	f.settings(t, f.wrap)
	t.Setenv(managedLauncherGateEnv, "1")
	// The launcher logs its args and exits 0 without running the extracted stub.
	helperMode(t, "fail-launcher", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "0")
	writeFileMode(t, blob, zstdOf(t, []byte("#\n")), 0o644)
	want := `"cliWasPresent":false,"launcherStatus":"usable","launcher":["` + f.wrap + `"],"launcherSource":"` + f.base + `"}` + "\n"
	if got := f.install(t, blob); got != want {
		t.Errorf("I10: facts tail\n got %q\nwant %q", got, want)
	}
	if log := readLog(t, f.wrapLog); !strings.HasPrefix(log, "LAUNCH "+f.cliDir+string(filepath.Separator)+".fetch-") || !strings.HasSuffix(log, " --version\n") || strings.Count(log, "LAUNCH") != 1 {
		t.Errorf("I10: launcher log %q, want one run on the staged file", log)
	}
	if !isRegularFile(f.cli) {
		t.Error("I10: the CLI was not installed")
	}

	if err := os.Remove(f.cli); err != nil {
		t.Fatal(err)
	}
	helperMode(t, "slow:1", "")
	setManagedRunBound(t, 300*time.Millisecond)
	setManagedFirstRunBound(t, 30*time.Second)
	writeFileMode(t, blob, zstdOf(t, []byte("#\n")), 0o644)
	if got := f.install(t, blob); got != want {
		t.Errorf("H4: a first run longer than the cache-hit bound: facts tail\n got %q\nwant %q", got, want)
	}
	if !isRegularFile(f.cli) {
		t.Error("H4: the CLI was not installed")
	}

	if err := os.Remove(f.cli); err != nil {
		t.Fatal(err)
	}
	helperMode(t, "slow:5", "")
	setManagedFirstRunBound(t, 300*time.Millisecond)
	writeFileMode(t, blob, zstdOf(t, []byte("#\n")), 0o644)
	want = `"cliWasPresent":false,"cliError":"cli unresponsive: the installed Claude Code binary was started through the host's managed launcher ` + f.wrap +
		` and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish","cliUnresponsive":true,"launcherStatus":"unresponsive","launcher":["` +
		f.wrap + `"],"launcherSource":"` + f.base + `","launcherReason":"did not exit within 123s and was stopped"}` + "\n"
	if got := f.install(t, blob); got != want {
		t.Errorf("H2: facts tail\n got %q\nwant %q", got, want)
	}
	if !isRegularFile(f.cli) {
		t.Error("H2: a stopped first run still installs the CLI")
	}
	if _, err := os.Lstat(blob); !os.IsNotExist(err) {
		t.Errorf("H2: the blob must be consumed, Lstat err = %v", err)
	}
}

// TestInstallFetchComesBeforeLauncherFields pins the -cli-url rows with the gate
// (Linux VM, 89cb6289): fetch comes after cliError and cliUnresponsive and before
// launcherStatus. Rows F-U (usable), F-E404 (a failed download) and H1 (a stopped
// first run).
func TestInstallFetchComesBeforeLauncherFields(t *testing.T) {
	f := newInstallFixture(t)
	if err := os.Remove(f.cli); err != nil {
		t.Fatal(err)
	}
	f.settings(t, f.wrap)
	t.Setenv(managedLauncherGateEnv, "1")
	helperMode(t, "fail-launcher", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "0")
	zst := zstdOf(t, []byte("#\n"))
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli.zst" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(zst)
	}))
	defer srv.Close()
	fetch := `"fetch":\{"bytes":\d+,"ms":\d+,"longestPauseMs":\d+\},`
	usable := `"launcherStatus":"usable","launcher":\["` + regexp.QuoteMeta(f.wrap) + `"\],"launcherSource":"` + regexp.QuoteMeta(f.base) + `"`
	run := func(row, path, wantTail string) {
		t.Helper()
		out := captureInstallOutput(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9",
			cliURL: srv.URL + path, cliChecksum: hex.EncodeToString(sum[:])})
		if !regexp.MustCompile(`"cliWasPresent":false,` + wantTail + `\}\n$`).MatchString(out) {
			t.Errorf("%s: facts line %q, want the tail %s", row, out, wantTail)
		}
	}
	run("F-U", "/cli.zst", fetch+usable)
	if !isRegularFile(f.cli) {
		t.Fatal("F-U: the CLI was not installed")
	}
	if err := os.Remove(f.cli); err != nil {
		t.Fatal(err)
	}
	run("F-E404", "/missing", `"cliError":"download failed with status 404",`+fetch+usable)

	helperMode(t, "slow:5", "")
	setManagedFirstRunBound(t, 300*time.Millisecond)
	run("H1", "/cli.zst", `"cliError":"cli unresponsive: [^"]*","cliUnresponsive":true,`+fetch+
		`"launcherStatus":"unresponsive","launcher":\["`+regexp.QuoteMeta(f.wrap)+`"\],"launcherSource":"`+regexp.QuoteMeta(f.base)+
		`","launcherReason":"did not exit within 123s and was stopped"`)
	if !isRegularFile(f.cli) {
		t.Error("H1: a stopped first run still installs the CLI")
	}
}

// probe runs the -probe-cli mode for the fixture CLI and returns stdout and stderr.
func (f *installFixture) probe(t *testing.T) (string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	runProbeCLI(&out, &errb, f.cli)
	return out.String(), errb.String()
}

// TestProbeCLIThroughLauncher pins P1-P6 and PRB-7's none case: with the gate a
// usable launcher runs the probe and prints nothing; an unusable or unreadable
// launcher prints __CLI_LAUNCHER__ and a reason without running the CLI; a failed
// run prints __CLI_LAUNCHER__ and four stderr lines; a stopped run prints
// __CLI_HUNG__; without the gate, or with a none answer, the probe runs directly.
func TestProbeCLIThroughLauncher(t *testing.T) {
	f := newInstallFixture(t)
	f.settings(t, f.wrap)
	t.Setenv(managedLauncherGateEnv, "1")

	helperMode(t, "wrap", "cli-log")
	if o, e := f.probe(t); o != "" || e != "" {
		t.Errorf("P1: stdout %q stderr %q, want both empty", o, e)
	}
	if got := readLog(t, f.wrapLog) + readLog(t, f.cliLog); got != "LAUNCH "+f.cli+" --version\nCLI --version gate=\n" {
		t.Errorf("P1: runs %q, want one run through the launcher", got)
	}

	_ = os.Remove(f.wrapLog)
	_ = os.Remove(f.cliLog)
	f.settings(t, "wrap")
	o, e := f.probe(t)
	if want := "claude-ssh: " + f.cli + " was not run: managed launcher unusable: the launcher must be an absolute path, not a bare name resolved via PATH\n"; o != "__CLI_LAUNCHER__\n" || e != want {
		t.Errorf("P2: stdout %q stderr %q, want __CLI_LAUNCHER__ and %q", o, e, want)
	}

	f.settings(t, f.wrap)
	realRead := readManagedSettingsFile
	denySettingsRead(t)
	o, e = f.probe(t)
	if want := "claude-ssh: " + f.cli + " was not run: managed settings unreadable: " + f.base + ": permission denied\n"; o != "__CLI_LAUNCHER__\n" || e != want {
		t.Errorf("P3: stdout %q stderr %q, want __CLI_LAUNCHER__ and %q", o, e, want)
	}
	readManagedSettingsFile = realRead
	if readLog(t, f.cliLog)+readLog(t, f.wrapLog) != "" {
		t.Error("P2/P3 must run nothing")
	}

	helperMode(t, "fail-launcher", "")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "L5: to stderr\n")
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "3")
	o, e = f.probe(t)
	want := "claude-ssh: the managed launcher's run printed:\nL5: to stderr\n\nclaude-ssh: " + f.cli + " --version through the managed launcher " + f.wrap + " did not succeed: exit status 3\n"
	if o != "__CLI_LAUNCHER__\n" || e != want {
		t.Errorf("P4: stdout %q stderr\n%q, want __CLI_LAUNCHER__ and\n%q", o, e, want)
	}
	t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "")
	if _, e = f.probe(t); e != "claude-ssh: "+f.cli+" --version through the managed launcher "+f.wrap+" did not succeed: exit status 3\n" {
		t.Errorf("empty launcher stderr: %q, want the last line only (claustrum's choice)", e)
	}

	helperMode(t, "slow:5", "")
	setManagedRunBound(t, 300*time.Millisecond)
	o, e = f.probe(t)
	want = "claude-ssh: cli unresponsive: the installed Claude Code binary was started through the host's managed launcher " + f.wrap + " and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish\n"
	if o != "__CLI_HUNG__\n" || e != want {
		t.Errorf("P5: stdout %q stderr %q, want __CLI_HUNG__ and %q", o, e, want)
	}

	helperMode(t, "cli-log", "")
	_ = os.Remove(f.wrapLog)
	_ = os.Remove(f.cliLog)
	t.Setenv(managedLauncherGateEnv, "")
	if o, e := f.probe(t); o != "" || e != "" || readLog(t, f.cliLog) != "CLI --version gate=\n" || readLog(t, f.wrapLog) != "" {
		t.Errorf("P6: stdout %q stderr %q runs %q, want one direct run", o, e, readLog(t, f.cliLog)+readLog(t, f.wrapLog))
	}
	t.Setenv(managedLauncherGateEnv, "1")
	_ = os.Remove(f.base)
	_ = os.Remove(f.cliLog)
	if o, e := f.probe(t); o != "" || e != "" || readLog(t, f.cliLog) != "CLI --version gate=1\n" {
		t.Errorf("gate with no settings: stdout %q stderr %q runs %q, want one direct run", o, e, readLog(t, f.cliLog))
	}
}
