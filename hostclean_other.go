//go:build !linux && !darwin

package main

// startHostCleaner is a no-op on windows and other non-linux, non-darwin targets. The host
// cleaner runs on linux (/proc) and darwin (sysctl + lsof); the reference ships no cleaner on
// windows, so there is nothing to run there and this stays a no-op (provably 1:1 with the
// reference, whose windows build links no cleaner).
func startHostCleaner(socket string) {}
