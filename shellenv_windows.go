//go:build windows

package main

// extractLoginPATH is a no-op on Windows: there is no login-shell PATH to extract
// (processes inherit the environment from the registry/parent). This is claustrum's
// own choice. The reference's Windows PATH handling is not probe-measured.
func extractLoginPATH() {}
