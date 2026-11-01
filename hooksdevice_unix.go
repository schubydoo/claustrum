//go:build !windows

package main

// hooksNullDevice is the value of core.hooksPath on every git call that sets it.
// be1c1ca5 passes /dev/null on Linux (Linux VM). macOS is not measured.
const hooksNullDevice = "/dev/null"
