//go:build linux

package childrec

// RecordForTest is the record of a child, for a test of package main that reads one.
type RecordForTest = childRecord

// ProcStartTicksForTest returns the start time of pid as the record holds it.
func ProcStartTicksForTest(pid int) int64 { return procStartTicks(pid) }
