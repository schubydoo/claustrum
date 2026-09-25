//go:build windows

package main

// ignoreSigpipeDefault is a no-op on Windows, which has no SIGPIPE.
func ignoreSigpipeDefault() {}
