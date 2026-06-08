//go:build unix

package main

import (
	"os"
	"testing"
)

// extractLoginPATH runs the login shell to resolve an interactive PATH and
// installs it into the daemon's environment. It is best-effort, so the contract
// under test is narrow: it must not panic, and it must leave PATH non-empty
// (either the freshly-extracted value or the pre-existing one on any failure).
func TestExtractLoginPATH(t *testing.T) {
	// Snapshot PATH via t.Setenv so the framework restores it after the test,
	// since extractLoginPATH mutates the process environment directly.
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")

	extractLoginPATH()

	if os.Getenv("PATH") == "" {
		t.Error("extractLoginPATH left PATH empty")
	}
}

// With SHELL unset, extractLoginPATH falls back to /bin/sh rather than failing.
func TestExtractLoginPATHDefaultShell(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", "")

	extractLoginPATH() // must not panic; exercises the empty-SHELL default branch

	if os.Getenv("PATH") == "" {
		t.Error("extractLoginPATH (default shell) left PATH empty")
	}
}
