//go:build !linux && !darwin

package main

// startHostCleaner is a no-op on windows and other non-linux, non-darwin targets. The host
// cleaner runs on linux (/proc) and darwin (sysctl + lsof).
func startHostCleaner(socket string) {}
