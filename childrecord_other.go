//go:build !linux

package main

// recordChild is a no-op off linux: the orphan registry's identity (boot id, pid
// namespace, machine id, and /proc start-times) is linux-specific, matching the
// linux-only trampoline that stamps the child marker. Darwin and Windows are
// reconciled in a follow-up slice.
func (m *procManager) recordChild(pid int, argv0 string) {}
