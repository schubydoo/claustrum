//go:build windows

package main

// hooksNullDevice is the value of core.hooksPath on every git call that sets it.
// be1c1ca5 passes \\.\NUL on each call on Windows (Windows VM).
const hooksNullDevice = `\\.\NUL`
