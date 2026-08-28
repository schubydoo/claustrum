//go:build !windows

package main

// windowsPathSpellingHazard is a no-op off Windows: trailing dots and spaces and ":"
// are valid filename characters on POSIX, and the reference applies no such component
// check on unix (measured — it refuses only a relative path or a ".." component there).
var windowsPathSpellingHazard = func(string) bool { return false }
