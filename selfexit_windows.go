//go:build windows

package main

// selfExitRuns is false on Windows, so exitWhenOrphaned returns at once there. A
// daemon whose socket file was deleted writes no line. In cell K3 of round tier1-01
// (Windows VM) 5fd08069 wrote no line and was still alive after 14 min. The idle exit
// does not run on Windows either. That part is not measured.
const selfExitRuns = false
