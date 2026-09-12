package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setProbeCLITimeout sets the -probe-cli probe deadline for one test and restores
// it, so a shrunk deadline cannot leak into the rest of the suite (mirrors
// setCLIProbeTimeout for the D11 probe).
func setProbeCLITimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := probeCLITimeout
	probeCLITimeout = d
	t.Cleanup(func() { probeCLITimeout = old })
}

// TestProbeCLIRunnableClassifies pins the three-way verdict the reference's
// -probe-cli mode reports: a CLI that exits 0 on `--version` "runs", one that exits
// non-zero or is missing is "bad", and one the deadline has to kill is "hung". The
// runs/bad tokens were measured byte-for-byte against 19f30c46; the deadline-kill
// path is exercised here with a shrunk bound. A mutant that returned probeCLIBad for
// a timeout (dropping the ctx.Err() check) fails the hung arm; one that returned
// probeCLIRuns on any error fails the bad arms.
func TestProbeCLIRunnableClassifies(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		// .exe suffix: Go's exec on Windows only resolves a path that carries an
		// extension (same reason TestIsRunnable_ProbeTimeoutOptIn uses it).
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// runs: exits 0 on --version.
	ok := write("ok.exe", fakeCLI(t, 0))
	if v := probeCLIRunnable(ok); v != probeCLIRuns {
		t.Errorf("exit-0 CLI: verdict = %d, want probeCLIRuns (%d)", v, probeCLIRuns)
	}

	// bad: exits non-zero on --version.
	bad := write("bad.exe", fakeCLI(t, 3))
	if v := probeCLIRunnable(bad); v != probeCLIBad {
		t.Errorf("exit-3 CLI: verdict = %d, want probeCLIBad (%d)", v, probeCLIBad)
	}

	// bad: the binary is missing.
	if v := probeCLIRunnable(filepath.Join(dir, "does-not-exist.exe")); v != probeCLIBad {
		t.Errorf("missing CLI: verdict = %d, want probeCLIBad (%d)", v, probeCLIBad)
	}

	// hung: the deadline has to kill it. A 1s sleeper against a 100ms bound (the 1s
	// is a process-leak budget on Unix — see slowCLI; do not raise it).
	slow := write("slow.exe", slowCLI(t, 1))
	setProbeCLITimeout(t, 100*time.Millisecond)
	if v := probeCLIRunnable(slow); v != probeCLIHung {
		t.Errorf("slow CLI under a 100ms bound: verdict = %d, want probeCLIHung (%d)", v, probeCLIHung)
	}
}

// TestWriteProbeCLIResult pins the exact stdout the -probe-cli mode writes for each
// verdict, matching 19f30c46 byte-for-byte: nothing on success (fmt.Fprintln is not
// called), and the token plus a trailing newline otherwise.
func TestWriteProbeCLIResult(t *testing.T) {
	cases := []struct {
		v    probeCLIVerdict
		want string
	}{
		{probeCLIRuns, ""},
		{probeCLIHung, "__CLI_HUNG__\n"},
		{probeCLIBad, "__CLI_BAD__\n"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		writeProbeCLIResult(&buf, c.v)
		if got := buf.String(); got != c.want {
			t.Errorf("verdict %d: output = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestProbeCLIDispatchUnsetsTokenAndReturns drives main()'s -probe-cli arm. It must
// route to the probe (not fall through to -install/-serve), unset CLAUDE_RPC_TOKEN
// so the probed child never inherits it (reference parity), and return rather than
// exit. runMain parks stdout on /dev/null, so the token bytes are asserted by
// TestWriteProbeCLIResult; here the discriminator is the token unset.
func TestProbeCLIDispatchUnsetsTokenAndReturns(t *testing.T) {
	t.Setenv("CLAUDE_RPC_TOKEN", "must-be-cleared")
	if _, exited := runMain(t, "-probe-cli", filepath.Join(t.TempDir(), "does-not-exist")); exited {
		t.Fatal("-probe-cli should return, not exit")
	}
	if v, ok := os.LookupEnv("CLAUDE_RPC_TOKEN"); ok {
		t.Errorf("CLAUDE_RPC_TOKEN still set to %q after -probe-cli; the arm must unset it", v)
	}
}
