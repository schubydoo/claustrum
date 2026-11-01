package main

import (
	"runtime"
	"slices"
	"testing"
)

// TestHooksPathNullDevice pins the core.hooksPath value of each git call that sets
// it: \\.\NUL on Windows and /dev/null on Linux and macOS (be1c1ca5).
func TestHooksPathNullDevice(t *testing.T) {
	want := "core.hooksPath=/dev/null"
	if runtime.GOOS == "windows" {
		want = `core.hooksPath=\\.\NUL`
	}
	for name, argv := range map[string][]string{
		"light":  gitHardenLight,
		"heavy":  gitHardenHeavy,
		"probe":  probeArgs(),
		"shadow": shadowTestPrefix(),
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("%s: %q holds no %q", name, argv, want)
		}
	}
}
