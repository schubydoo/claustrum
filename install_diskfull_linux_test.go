package main

import (
	"strings"
	"syscall"
	"testing"
)

// A user quota starts the prune after a full disk on Linux (be1c1ca5,
// measured): with 1.0.1 to 1.0.6 and keep 10 the cliError holds "disk quota
// exceeded", 1.0.6 stays and cliRemoved is 5.
//
// SAFETY: every path is under t.TempDir.
func TestInstallQuotaStartsTheSecondPruneOnLinux(t *testing.T) {
	f := newStubFixture(t)
	t.Setenv(homeEnvVar(), f.root)
	names := putVersions(t, f.cliDir, "1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5", "1.0.6")
	failCloseWith(t, syscall.EDQUOT)

	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "2.0.0", cliKeep: 10, cliZst: f.stubBlob(t)})

	if !strings.HasSuffix(got.CliError, ": disk quota exceeded") || got.CliRemoved != 5 {
		t.Errorf("cliError = %q, cliRemoved = %d, want the quota text and 5", got.CliError, got.CliRemoved)
	}
	wantDir(t, "a user quota", f.cliDir, names[5])
}
