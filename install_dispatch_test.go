package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
)

// The default must be 0 — no bound — because that is the parity position and the
// whole point of the flip. Asserted on the package variable AND on the flag's
// DECLARED default: moving a non-zero value into flag.Duration's default argument
// ships a bounded binary while leaving the package variable at zero.
func TestCLIDownloadTimeoutDefaultsOff(t *testing.T) {
	if install.DownloadTimeout != 0 {
		t.Fatalf("install.DownloadTimeout default = %s, want 0 (no bound = reference parity)", install.DownloadTimeout)
	}
	if got := (config{}).effectiveCLIDownloadTimeout(0, false); got != 0 {
		t.Fatalf("resolved default = %s, want 0", got)
	}
	// The declared default, not the resolved one: a real claustrum.conf sets the
	// resolved value legitimately, and a test reading it fails for a correct reason.
	if _, exited := runMain(t, "-install"); exited {
		t.Fatal("-install should return, not exit")
	}
	f := lastMainFlagSet.Lookup("cli-download-timeout")
	if f == nil {
		t.Fatal("main() did not register -cli-download-timeout")
	}
	if f.DefValue != "0s" {
		t.Fatalf("-cli-download-timeout declared default = %q, want \"0s\" (no bound = reference parity)", f.DefValue)
	}
}

// TestProbeCLIDispatchUnsetsTokenAndReturns drives main()'s -probe-cli arm. It must
// route to the probe (not fall through to -install/-serve), unset CLAUDE_RPC_TOKEN
// so the probed child never inherits it (reference parity), and return rather than
// exit. runMain parks stdout on /dev/null, so the token bytes are asserted by
// TestWriteProbeCLIResult in internal/install; here the discriminator is the token unset.
func TestProbeCLIDispatchUnsetsTokenAndReturns(t *testing.T) {
	t.Setenv("CLAUDE_RPC_TOKEN", "must-be-cleared")
	if _, exited := runMain(t, "-probe-cli", filepath.Join(t.TempDir(), "does-not-exist")); exited {
		t.Fatal("-probe-cli should return, not exit")
	}
	if v, ok := os.LookupEnv("CLAUDE_RPC_TOKEN"); ok {
		t.Errorf("CLAUDE_RPC_TOKEN still set to %q after -probe-cli; the arm must unset it", v)
	}
}

// TestProbeCLIDispatchSurvivesUnresolvableHome pins that -probe-cli runs BEFORE the
// home-directory resolution, so a home that cannot be resolved does not pre-empt the
// mode's always-exit-0 contract. With an empty $HOME, os.UserHomeDir fails; a build
// that resolved home before dispatching -probe-cli would osExit(1) here instead of
// classifying and returning. (Skipped on Windows, where UserHomeDir reads
// %USERPROFILE% and an empty HOME does not make it fail.)
func TestProbeCLIDispatchSurvivesUnresolvableHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir does not use $HOME on Windows")
	}
	t.Setenv("HOME", "") // make os.UserHomeDir fail
	if _, exited := runMain(t, "-probe-cli", filepath.Join(t.TempDir(), "does-not-exist")); exited {
		t.Fatal("-probe-cli must run before home resolution: an unresolvable home must not pre-empt its exit-0 classification")
	}
}

// TestProbeCLIUsageNamesTheLauncher pins USG-1: the -probe-cli usage line is the
// only -help difference, and it names the gate and __CLI_LAUNCHER__.
func TestProbeCLIUsageNamesTheLauncher(t *testing.T) {
	runMain(t, "-version")
	f := lastMainFlagSet.Lookup("probe-cli")
	if f == nil {
		t.Fatal("no -probe-cli flag")
	}
	const want = "Run the bounded --version probe on this CLI binary and exit 0: prints nothing if it runs, __CLI_HUNG__ if it had to be killed, __CLI_BAD__ if it is missing or does not run; with CLAUDE_SSH_MANAGED_LAUNCHER=1 it runs through the host's managed launcher and prints __CLI_LAUNCHER__ when the launcher is unusable, the managed settings are unreadable, or the launcher's run of it failed (reason on stderr)"
	if f.Usage != want {
		t.Errorf("usage = %q\nwant    %q", f.Usage, want)
	}
}
