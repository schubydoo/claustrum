//go:build !linux && !darwin

package main

// reapOrphans is a no-op on windows and other non-linux, non-darwin targets. Linux and
// darwin read live processes (childreap_linux.go / childreap_darwin.go) over the shared
// decision logic in childreap.go. Measured on a windows VM: the 19f30c46 reference daemon
// reports it is not the run-dir lock holder on windows, so it leaves any predecessor's
// recorded children alone and reaps nothing (planted children/<pid>.json records survived a
// daemon startup untouched, for the reference and for claustrum alike). Claustrum holds no
// run-dir lock on windows either (its claimRunDir is a no-op there),
// so this stays a no-op.
func reapOrphans(runDir, ownInstance string) {}
