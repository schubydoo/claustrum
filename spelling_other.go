//go:build !windows

package main

// windowsPathSpellingHazard is a no-op off Windows: trailing dots and spaces and ":"
// are valid filename characters on POSIX, and the reference accepts such components
// on unix (measured against 7d193f89).
var windowsPathSpellingHazard = func(string) bool { return false }
