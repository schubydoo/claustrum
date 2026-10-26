//go:build windows

package main

// otherDevice is false on Windows: the search has no device test (not measured).
func otherDevice(start, d string) bool { return false }
