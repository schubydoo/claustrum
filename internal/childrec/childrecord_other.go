//go:build !linux && !darwin

package childrec

// RecordChild is a no-op on windows and other non-linux, non-darwin targets. Linux and
// darwin gather the child's identity (childrecord_linux.go / childrecord_darwin.go) and
// write a <runDir>/children/<pid>.json record. Measured on Windows: the 19f30c46
// reference daemon reports it is not the run-dir lock holder on windows and records no
// children (a child spawned under a run-shaped socket leaves children/ empty). Claustrum
// holds no run-dir lock on windows either (its runlock.LockRunFolder is a no-op
// there), so this stays a no-op. r.RunDir is non-empty on windows too (supervisor.ExecChildRunDir
// gives the folder of the socket on every OS), but its only windows consumers are no-ops, so
// it has no effect.
func (r *Records) RecordChild(pid int, argv0, program string) {}

// HoldRunDir and RemoveChildRecord are no-ops here for the same reason: no record
// exists, so there is no run dir to hold and no record to remove when a child ends.
func (r *Records) HoldRunDir() {}

func (r *Records) RemoveChildRecord(pid int) {}
