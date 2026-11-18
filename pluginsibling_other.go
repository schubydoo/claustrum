//go:build !windows

package main

// extraNobodyListening has no additional errors to match off Windows: the POSIX
// syscall.ECONNREFUSED that isRefusedDial checks already covers a refused dial.
func extraNobodyListening(error) bool { return false }
