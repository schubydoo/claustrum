//go:build windows

package main

// extractLoginPATH is a no-op on Windows: there is no login-shell PATH to extract
// (processes inherit the environment from the registry/parent), matching the
// upstream Windows build which omits the PATH-extraction step entirely.
func extractLoginPATH() {}
