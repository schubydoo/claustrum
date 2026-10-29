//go:build windows

package main

// cwdUnusable is false on Windows. The second cwd test is not measured there
// (claustrum's own).
func cwdUnusable(string) bool { return false }
