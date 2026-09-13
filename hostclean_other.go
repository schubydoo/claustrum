//go:build !linux

package main

// startHostCleaner is a no-op off linux. The host cleaner reads /proc, SO_PEERCRED, and
// /proc/locks and signals process groups — all linux-specific — so there is nothing to run
// on darwin or windows. Those ports are reconciled in a later slice, matching how the reap
// and the exec-child trampoline landed linux-first.
func startHostCleaner(socket string) {}
