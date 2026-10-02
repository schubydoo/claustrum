package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The home guard of the tree delete at the final name, with a link in one of
// the two paths (D2). The lexical test of wipesHomeDir sees two different
// spellings there. cliFolderHoldsHome compares the folder at the final name
// with the home folder and with each parent folder of it, by identity.
//
// SAFETY: every path is under t.TempDir, and the home variable names a fixture
// folder there. Without the guard the test deletes only that fixture.
//
// On Windows a symlink needs a privilege, so a cell skips when the symlink
// cannot be made. install_homeguard_windows_test.go runs the same cells with a
// junction, which needs none.
//
// docs/DIVERGENCES.md D2 lists the cells that were measured against 89cb6289.
// In each of them the reference removes the folder.
func TestInstallHomeGuardResolvesSymlinks(t *testing.T) {
	homeGuardLinkCells(t, func(t *testing.T, link, target string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("no privilege to make a symlink: %v", err)
			}
			t.Fatal(err)
		}
	})
}

// homeGuardLinkCells runs the cells with one kind of link. Each cell has the
// folder "real", the folder F = real/LEAF at the final name, and the link
// "link". A cell with a chain also has "link2", which points to "link". LEAF is
// the -cli-version with the suffix of the system. The paths of a cell are
// relative to the cell folder.
//
// A cell name goes into the path of t.TempDir, and the junction cells give that
// path to cmd.exe. A name holds letters, spaces and hyphens only: cmd.exe reads
// a comma and some other characters as separators.
func homeGuardLinkCells(t *testing.T, mkLink func(t *testing.T, link, target string)) {
	for _, tc := range []struct {
		cell      string
		linkTo    string // the target of "link"
		home      string // the home variable
		noHome    bool   // the home path names no folder
		cliDirVia string // -cli-dir: "real" or "link"
		refused   bool
	}{
		{"is home and the home path has the link", "real", "link/LEAF", false, "real", true},
		{"is home and the cli-dir has the link", "real", "real/LEAF", false, "link", true},
		{"holds home and the home path has the link", "real", "link/LEAF/alice", false, "real", true},
		{"holds home and the cli-dir has the link", "real", "real/LEAF/alice", false, "link", true},
		{"holds home and the link points below the final name", "real/LEAF/users", "link/alice", false, "real", true},
		{"holds home two levels down and the cli-dir has the link", "real", "real/LEAF/users/alice", false, "link", true},
		{"holds home and the home path has a chain of two links", "real", "link2/LEAF/alice", false, "real", true},
		{"holds a home path with no folder and the link", "real", "link/LEAF/ghost/alice", true, "real", true},
		{"holds a home path with no folder in the same spelling", "real", "real/LEAF/ghost/alice", true, "real", true},
		{"not home and the cli-dir has the link", "real", "../otherhome", false, "link", false},
		{"not home and the home is a sibling with a longer name", "real", "link/LEAFx", false, "real", false},
	} {
		t.Run(tc.cell, func(t *testing.T) {
			f := newStubFixture(t)
			cell := filepath.Join(f.root, "cell")
			const version = "v"
			leaf := version + cliExeSuffix
			at := func(rel string) string {
				return filepath.Join(cell, filepath.FromSlash(strings.ReplaceAll(rel, "LEAF", leaf)))
			}
			final := at("real/LEAF")
			keeps := []string{filepath.Join(final, "keep1.txt"), filepath.Join(final, "users", "keep2.txt")}
			for _, k := range keeps {
				if err := os.MkdirAll(filepath.Dir(k), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(k, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mkLink(t, at("link"), at(tc.linkTo))
			if strings.HasPrefix(tc.home, "link2/") {
				mkLink(t, at("link2"), at("link"))
			}
			home := at(tc.home)
			if !tc.noHome {
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(homeEnvVar(), home)
			cliDir := at(tc.cliDirVia)
			blob := f.stubBlob(t)
			cli := installCLIPath(cliDir, version)
			facts := captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: version, cliZst: blob})
			if facts.CliPath != cli {
				t.Errorf("cliPath = %q, want %q", facts.CliPath, cli)
			}
			_, runs := f.stubLogLine(t, "START ")
			if !tc.refused {
				if facts.CliError != "" || !isRegularFile(cli) || runs != 1 {
					t.Errorf("CliError %q, CLI file %v, runs %d: want the folder replaced by the CLI", facts.CliError, isRegularFile(cli), runs)
				}
				return
			}
			if want := fmt.Sprintf("cli path must not be or contain the home directory: %q", cli); facts.CliError != want {
				t.Errorf("CliError = %q, want %q", facts.CliError, want)
			}
			for _, k := range keeps {
				if !isRegularFile(k) {
					t.Errorf("%s was deleted", k)
				}
			}
			if !isRegularFile(blob) {
				t.Error("the blob was consumed")
			}
			if runs != 0 {
				t.Errorf("a CLI ran %d times, want 0", runs)
			}
			assertNoStagingLeftover(t, cli)
		})
	}
}
