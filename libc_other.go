//go:build !linux

package main

// detectLibc reports no libc off linux, matching the reference: its
// install.detectLibc exists ONLY in the linux build — GoReSym finds the symbol
// in linux-amd64 and in neither darwin-amd64 nor windows-amd64 — and its
// __INSTALL_RESULT__ carries an empty libc there.
//
// Measured on the Windows guest at 5db5e4a:
//
//	ref  {"os":"windows","arch":"amd64","libc":"",…}
//	cla  {"os":"windows","arch":"amd64","libc":"glibc",…}   (before this)
//
// claustrum ran the linux probe everywhere, so `ldd` was absent, no musl loader
// matched, and it fell through to the "glibc" default — asserting a C library on
// a platform that has no such notion. darwin follows from the same missing
// symbol.
func detectLibc() string {
	return ""
}
