//go:build windows

package main

// raiseInheritedFileLimit is a no-op on Windows, which has no RLIMIT_NOFILE. The
// reference build 19f30c46 raises the inherited open-file limit on Unix only.
func raiseInheritedFileLimit() {}
