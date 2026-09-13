//go:build !linux && !darwin

package main

// recordChild is a no-op on windows and other non-linux, non-darwin targets. Linux and
// darwin gather the child's identity (childrecord_linux.go / childrecord_darwin.go) and
// write a <runDir>/children/<pid>.json record. Measured on a windows VM: the 19f30c46
// reference daemon reports it is not the run-dir lock holder on windows and records no
// children (a child spawned under a run-shaped socket leaves children/ empty). Claustrum
// holds no run-dir lock on windows either (claimRunDir is a no-op there, reference build
// 4534d86), so this stays a no-op. m.runDir may be non-empty on windows (execChildRunDir
// accepts a run-shaped socket on every OS), but its only windows consumers are no-ops, so
// it has no effect.
func (m *procManager) recordChild(pid int, argv0 string) {}
