package main

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// runMainOut drives main() as runMain does, but keeps stdout and stderr in
// files and answers their bytes.
func runMainOut(t *testing.T, args ...string) (code int, exited bool, stdout, stderr string) {
	t.Helper()
	stubOsExit(t)
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatal(err)
	}
	oldArgs, oldFS := os.Args, flag.CommandLine
	oldVersion, oldBuildTime := Version, BuildTime
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Args = append([]string{"claustrum"}, args...)
	flag.CommandLine = flag.NewFlagSet("claustrum", flag.ExitOnError)
	os.Stdout, os.Stderr = outF, errF
	func() {
		defer func() {
			os.Args, flag.CommandLine = oldArgs, oldFS
			Version, BuildTime = oldVersion, oldBuildTime
			os.Stdout, os.Stderr = oldOut, oldErr
		}()
		code, exited = catchExit(main)
	}()
	_ = outF.Close()
	_ = errF.Close()
	o, _ := os.ReadFile(outF.Name())
	e, _ := os.ReadFile(errF.Name())
	return code, exited, string(o), string(e)
}

// relativePlace is refused on every system: not_absolute on Linux, host
// elsewhere.
const relativePlace = "srv/v/server"

func relativeDenialLine() string {
	if runtime.GOOS == "linux" {
		return "claustrum: refusing to run (not_absolute) from " + filepath.Clean(relativePlace) + ": " + notAbsoluteText + "\n"
	}
	return "claustrum: refusing to run (host) from " + filepath.Clean(relativePlace) + ": " + hostText + "\n"
}

// TestCheckPlaceNotAsked pins the not_asked refusal of -check-place with
// CLAUDE_SSH_RUN_FROM unset and empty: exit 78 and the line of the 13047a51
// rows, with claustrum's program name.
func TestCheckPlaceNotAsked(t *testing.T) {
	const want = "claustrum: refusing to run (not_asked): CLAUDE_SSH_RUN_FROM is not set, or is empty, so there is nothing to check.\n"
	t.Setenv(runFromEnv, "")
	code, exited, out, errText := runMainOut(t, "-check-place")
	if !exited || code != 78 {
		t.Fatalf("exit = %d, %v, want 78", code, exited)
	}
	if out != "" || errText != want {
		t.Errorf("stdout %q, stderr %q\nwant stderr %q", out, errText, want)
	}
}

// TestCheckPlaceRefused pins exit 78 and the line of a refused place.
func TestCheckPlaceRefused(t *testing.T) {
	t.Setenv(runFromEnv, relativePlace)
	code, exited, out, errText := runMainOut(t, "-check-place")
	if !exited || code != 78 {
		t.Fatalf("exit = %d, %v, want 78", code, exited)
	}
	if out != "" || errText != relativeDenialLine() {
		t.Errorf("stdout %q, stderr %q\nwant stderr %q", out, errText, relativeDenialLine())
	}
}

// TestPlaceCheckBound pins the 8 s bound of the whole place check, as
// 13047a51 has it (measured on Linux).
func TestPlaceCheckBound(t *testing.T) {
	if placeCheckBound != 8*time.Second {
		t.Errorf("placeCheckBound %v, want 8s", placeCheckBound)
	}
}

// TestCheckPlaceUsage pins the usage text of -check-place.
func TestCheckPlaceUsage(t *testing.T) {
	runMain(t, "-version")
	f := lastMainFlagSet.Lookup("check-place")
	if f == nil {
		t.Fatal("no -check-place flag")
	}
	const want = "Check this file as CLAUDE_SSH_RUN_FROM asks and do nothing else: exit 0 only if it passes"
	if f.Usage != want {
		t.Errorf("usage = %q, want %q", f.Usage, want)
	}
}

// TestNoModeNamesEveryMode pins the line of a run with no mode flag: the
// list of modes of 13047a51 (measured on Linux and Windows), with claustrum's program
// name, and exit 2.
func TestNoModeNamesEveryMode(t *testing.T) {
	const want = "claustrum: one of --version/--install/--probe-cli/--serve/--bridge/--stop/--check-place is required\n"
	t.Setenv(runFromEnv, "")
	code, exited, out, errText := runMainOut(t)
	if !exited || code != 2 {
		t.Fatalf("exit = %d, %v, want 2", code, exited)
	}
	if out != "" || errText != want {
		t.Errorf("stdout %q, stderr %q\nwant stderr %q", out, errText, want)
	}
}

// TestProbeCLIRefusedPlace pins the -probe-cli answer of a refused place: the
// refusal line on stderr, __CLI_BAD__ on stdout, and exit 0.
func TestProbeCLIRefusedPlace(t *testing.T) {
	t.Setenv(runFromEnv, relativePlace)
	code, exited, out, errText := runMainOut(t, "-probe-cli", filepath.Join(t.TempDir(), "none"))
	if exited {
		t.Fatalf("exit %d, want a return (exit 0)", code)
	}
	if out != "__CLI_BAD__\n" || errText != relativeDenialLine() {
		t.Errorf("stdout %q, stderr %q\nwant __CLI_BAD__ and %q", out, errText, relativeDenialLine())
	}
}

// TestServeRefusedPlaceRemovesToken pins that -serve with a refused place
// exits 78 and removes a regular -token-file.
func TestServeRefusedPlaceRemovesToken(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runFromEnv, relativePlace)
	code, exited, _, errText := runMainOut(t, "-serve", "-socket", filepath.Join(dir, "s.sock"), "-token-file", tok)
	if !exited || code != 78 {
		t.Fatalf("exit = %d, %v, want 78 (stderr %q)", code, exited, errText)
	}
	if errText != relativeDenialLine() {
		t.Errorf("stderr %q, want %q", errText, relativeDenialLine())
	}
	if _, err := os.Lstat(tok); !os.IsNotExist(err) {
		t.Errorf("token file still there (%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "s.sock")); !os.IsNotExist(err) {
		t.Errorf("socket path made (%v)", err)
	}
}

// TestProbeCLIUnsetPlaceRunsProbe pins that -probe-cli with CLAUDE_SSH_RUN_FROM
// unset checks nothing: the probe answers, and stderr has no refusal line.
func TestProbeCLIUnsetPlaceRunsProbe(t *testing.T) {
	t.Setenv(runFromEnv, "")
	code, exited, out, errText := runMainOut(t, "-probe-cli", filepath.Join(t.TempDir(), "none"))
	if exited {
		t.Fatalf("exit %d, want a return (exit 0)", code)
	}
	if out != "__CLI_BAD__\n" || errText != "" {
		t.Errorf("stdout %q, stderr %q, want the probe answer and no refusal", out, errText)
	}
}

// TestStopServeRefusedPlace pins that -stop with -serve runs serve, so it
// checks the place and removes the -token-file.
func TestStopServeRefusedPlace(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runFromEnv, relativePlace)
	code, exited, _, errText := runMainOut(t, "-stop", "-serve", "-socket", filepath.Join(dir, "s.sock"), "-token-file", tok)
	if !exited || code != 78 || errText != relativeDenialLine() {
		t.Fatalf("exit %d, %v, stderr %q, want 78 and the refusal", code, exited, errText)
	}
	if _, err := os.Lstat(tok); !os.IsNotExist(err) {
		t.Errorf("token file still there (%v)", err)
	}
}

// TestInstallServeRefusedPlaceKeepsToken pins that -install wins over -serve,
// so a refused place exits 78 and keeps the -token-file.
func TestInstallServeRefusedPlaceKeepsToken(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runFromEnv, relativePlace)
	code, exited, _, errText := runMainOut(t, "-install", "-serve", "-socket", filepath.Join(dir, "s.sock"), "-token-file", tok)
	if !exited || code != 78 || errText != relativeDenialLine() {
		t.Fatalf("exit %d, %v, stderr %q, want 78 and the refusal", code, exited, errText)
	}
	if _, err := os.Lstat(tok); err != nil {
		t.Errorf("token file gone under -install: %v", err)
	}
}

// TestRefusedPlaceModes pins which modes refuse with exit 78. -stop alone
// does not check, and -bridge and -install do. No mode refuses too.
func TestRefusedPlaceModes(t *testing.T) {
	t.Setenv(runFromEnv, relativePlace)
	sock := filepath.Join(t.TempDir(), "none.sock")
	for _, args := range [][]string{{"-bridge", "-socket", sock}, {"-install"}, {}} {
		code, exited, _, errText := runMainOut(t, args...)
		if !exited || code != 78 || errText != relativeDenialLine() {
			t.Errorf("%v: exit %d, %v, stderr %q, want 78 and the refusal", args, code, exited, errText)
		}
	}
	code, exited, out, errText := runMainOut(t, "-stop", "-socket", sock)
	if exited || errText != "" || out != "none\n" {
		t.Errorf("-stop: exit %d, %v, stdout %q, stderr %q, want none and no check", code, exited, out, errText)
	}
}

// TestGateRunFromKeepsTokenOutsideServe pins that only a serve run removes
// the -token-file.
func TestGateRunFromKeepsTokenOutsideServe(t *testing.T) {
	stubOsExit(t)
	tok := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	code, exited := catchExit(func() {
		gateRunFrom(func(s string) { got += s }, relativePlace, "v", false, tok)
	})
	if !exited || code != 78 || got != relativeDenialLine() {
		t.Fatalf("exit %d, %v, line %q", code, exited, got)
	}
	if _, err := os.Lstat(tok); err != nil {
		t.Errorf("token file gone outside a serve run: %v", err)
	}
	// An empty place checks nothing.
	code, exited = catchExit(func() {
		gateRunFrom(func(s string) { got += s }, "", "v", true, tok)
	})
	if exited {
		t.Fatalf("empty place exited %d", code)
	}
	if _, err := os.Lstat(tok); err != nil {
		t.Errorf("token file gone with an empty place: %v", err)
	}
}

// TestPlaceVersion pins the version of the shape: the override first.
func TestPlaceVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "abc"
	if got := placeVersion(""); got != "abc" {
		t.Errorf("placeVersion(\"\") = %q", got)
	}
	if got := placeVersion("def"); got != "def" {
		t.Errorf("placeVersion(\"def\") = %q", got)
	}
}

// TestRemoveRefusedTokenFileKinds pins the kinds that the token remove takes
// on every system: a regular file goes, a folder and a missing path stay.
func TestRemoveRefusedTokenFileKinds(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg")
	if err := os.WriteFile(reg, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeRefusedTokenFile(reg)
	if _, err := os.Lstat(reg); !os.IsNotExist(err) {
		t.Errorf("regular file kept (%v)", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	removeRefusedTokenFile(sub)
	if fi, err := os.Lstat(sub); err != nil || !fi.IsDir() {
		t.Errorf("empty folder removed (%v)", err)
	}
	removeRefusedTokenFile(filepath.Join(dir, "missing"))
}
