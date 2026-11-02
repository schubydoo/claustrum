//go:build windows

package install

import (
	"os"
	"path/filepath"
	"testing"
)

// The -cli-dir text on Windows (measured on Windows). 5fd08069 makes
// nothing for D\sub\.. and answers the "missing" error with the cli path
// D\1.0.0.exe, also with D\sub a file.

// Measured. With sub missing nothing is made.
func TestInstallMakesNoFolderOfTheRawCLIDirOnWindows(t *testing.T) {
	d := rawCLIDirCell(t)
	out := captureInstallOutput(t, installOpts{cliDir: d + `\sub\..`, cliVersion: "1.0.0"})
	if want := factsHead(t, filepath.Join(d, "1.0.0.exe")) + missingTail; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if _, err := os.Lstat(filepath.Join(d, "sub")); !os.IsNotExist(err) {
		t.Errorf("D\\sub: err %v, want it absent", err)
	}
}

// Measured. With D\sub a regular file the answer is the same "missing" error,
// and the file keeps its content.
func TestInstallRawCLIDirThroughAFileAnswersMissingOnWindows(t *testing.T) {
	d := rawCLIDirCell(t)
	sub := filepath.Join(d, "sub")
	if err := os.WriteFile(sub, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: d + `\sub\..`, cliVersion: "1.0.0"})
	if want := factsHead(t, filepath.Join(d, "1.0.0.exe")) + missingTail; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantContent(t, sub, "keep\n")
}
