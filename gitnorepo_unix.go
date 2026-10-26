//go:build unix

package main

import (
	"os"
	"syscall"
)

// otherDevice reports whether folder d is on another device than start. A folder that
// cannot be stat'ed passes the test, so the result is false then (not measured).
func otherDevice(start, d string) bool {
	a, err := os.Stat(start)
	if err != nil {
		return false
	}
	b, err := os.Stat(d)
	if err != nil {
		return false
	}
	sa, okA := a.Sys().(*syscall.Stat_t)
	sb, okB := b.Sys().(*syscall.Stat_t)
	return okA && okB && sa.Dev != sb.Dev
}
