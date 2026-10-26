//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// the folder of the calls outside a repository is the root folder on Linux
// and macOS.
func TestOutsideCallFolderIsRoot(t *testing.T) {
	if gitVersionDir != "/" {
		t.Errorf("gitVersionDir = %q, want /", gitVersionDir)
	}
}

// a probe that a signal ended is "unknown": no `git version` run follows and
// nothing is stored. The stub kills itself.
func TestAttrSourceProbeSignalIsUnknown(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nkill -KILL $$\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)
	if got := attrSourceArgs(); got != nil {
		t.Errorf("attrSourceArgs() = %q, want nil", got)
	}
	if attrSourceDone {
		t.Error("a probe that a signal ended stored an answer")
	}
	data, _ := os.ReadFile(log)
	if want := "--attr-source=" + gitEmptyTree + " version\n"; string(data) != want {
		t.Errorf("calls = %q, want only the probe %q", data, want)
	}
}
