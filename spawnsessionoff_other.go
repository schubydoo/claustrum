//go:build !windows

package main

// sessionOffTyped is false on Linux and macOS: a process.spawn takes any value of
// sessionFoldersOff, and only true turns the check off (claustrum's own, not
// measured). The tests set it on every system.
var sessionOffTyped = false
