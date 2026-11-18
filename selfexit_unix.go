//go:build !windows

package main

// selfExitRuns is true on Linux and macOS: the daemon runs the checks of
// watchSocketOwnership there. That covers the orphan exit and the idle exit.
const selfExitRuns = true
