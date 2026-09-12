//go:build windows

package main

// ignoreSigpipeDefault is a no-op on Windows, which has no SIGPIPE — matching the
// reference, whose Windows build has nothing to ignore here.
func ignoreSigpipeDefault() {}
