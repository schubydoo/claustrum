//go:build !linux

package main

// detectLibc reports no libc off linux. The reference __INSTALL_RESULT__ carries
// an empty libc on Windows, measured below. The darwin side is not
// probe-measured.
//
// Measured on the Windows guest at 5db5e4a:
//
//	ref  {"os":"windows","arch":"amd64","libc":"",…}
//	cla  {"os":"windows","arch":"amd64","libc":"glibc",…}   (before this)
//
// claustrum ran the linux probe everywhere, so `ldd` was absent, no musl loader
// matched, and it fell through to the "glibc" default — asserting a C library on
// a platform that has no such notion. claustrum treats darwin the same way.
func detectLibc() string {
	return ""
}
