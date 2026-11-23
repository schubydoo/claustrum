//go:build unix

package main

// keyedPtyRefused is false on Linux and macOS, where a pty object with a valid key
// runs the command with no terminal (claustrum's own). The tests set it on every
// system.
var keyedPtyRefused = false
