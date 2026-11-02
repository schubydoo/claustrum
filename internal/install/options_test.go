package install

import "testing"

// TestOptionsGiveEachFlagItsOwnField: Run gives each field of Options to the
// field of installOpts with the same name. Distinct values on purpose: equal
// ones pass under a swap.
func TestOptionsGiveEachFlagItsOwnField(t *testing.T) {
	got := Options{
		CLIDir: "dir", CLIVersion: "version", CLIURL: "url",
		CLIChecksum: "checksum", CLIZst: "zst", CLIKeep: 7, Home: "home",
	}.opts()
	want := installOpts{
		cliDir: "dir", cliVersion: "version", cliURL: "url",
		cliChecksum: "checksum", cliZst: "zst", cliKeep: 7, home: "home",
	}
	if got != want {
		t.Errorf("opts() = %+v, want %+v", got, want)
	}
}
