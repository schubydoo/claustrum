//go:build windows

package main

// runlockHoldFixture is unreachable on Windows: the run-dir lock is Unix-only, so no
// Windows test drives the "runlock-hold" helper mode. The stub keeps the shared
// runHelper switch compiling on the Windows test build.
func runlockHoldFixture(args []string) int { return 1 }
