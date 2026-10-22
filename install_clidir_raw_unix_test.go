//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The -cli-dir text as given (round tier3-01, cells L-HS10b, L-V1, L-V2 and
// L-V3 on the Linux VM, M-HS10b on the macOS VM). 5fd08069 makes every missing
// folder of the raw text with mode 0700 before it looks for the CLI. The CLI
// path stays the cleaned join of the cli-dir and the version.

// wantEmptyFolder checks that dir is a new 0700 folder with nothing in it.
func wantEmptyFolder(t *testing.T, dir string) {
	t.Helper()
	wantFolder0700(t, dir)
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Errorf("%s: err %v, %d entries, want an empty folder", dir, err, len(ents))
	}
}

// Cell HS10b. -cli-dir D/sub/.. with sub missing makes D/sub and answers the
// "missing" error with the cli path D/1.0.0.
func TestInstallMakesTheFoldersOfTheRawCLIDir(t *testing.T) {
	d := rawCLIDirCell(t)
	out := captureInstallOutput(t, installOpts{cliDir: d + "/sub/..", cliVersion: "1.0.0"})
	if want := factsHead(t, filepath.Join(d, "1.0.0")) + missingTail; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantEmptyFolder(t, filepath.Join(d, "sub"))
}

// Cell V1. With D/sub a regular file the answer is the mkdir error with an empty
// cli path, and the file stays.
func TestInstallRawCLIDirThroughAFileAnswersTheMkdirError(t *testing.T) {
	d := rawCLIDirCell(t)
	sub := filepath.Join(d, "sub")
	if err := os.WriteFile(sub, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: d + "/sub/..", cliVersion: "1.0.0"})
	want := factsHead(t, "") + `false,"cliError":` + jsonString(t, "mkdir cli dir: mkdir "+sub+": not a directory") + "}\n"
	if out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantContent(t, sub, "keep\n")
}

// Cell V2. -cli-dir D/a/b/../.. makes D/a and D/a/b.
func TestInstallMakesEachMissingFolderOfTheRawCLIDir(t *testing.T) {
	d := rawCLIDirCell(t)
	out := captureInstallOutput(t, installOpts{cliDir: d + "/a/b/../..", cliVersion: "1.0.0"})
	if want := factsHead(t, filepath.Join(d, "1.0.0")) + missingTail; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, filepath.Join(d, "a"))
	wantEmptyFolder(t, filepath.Join(d, "a", "b"))
}

// Cell V3. On a cache hit the folder step runs too: D/sub is made, and the CLI
// at D/1.0.0 is present.
func TestInstallMakesTheRawCLIDirOnACacheHit(t *testing.T) {
	d := rawCLIDirCell(t)
	cli := filepath.Join(d, "1.0.0")
	placeStub(t, cli)
	out := captureInstallOutput(t, installOpts{cliDir: d + "/sub/..", cliVersion: "1.0.0"})
	if want := factsHead(t, cli) + "true}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantEmptyFolder(t, filepath.Join(d, "sub"))
}
