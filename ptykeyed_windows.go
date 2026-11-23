//go:build windows

package main

// keyedPtyRefused is true on Windows. 4c111e86 and 87375cc7 refuse a pty object with a
// valid key, and answer a bad member with Invalid params, on the measured Windows host.
// That every Windows host refuses is claustrum's own. The tests set it on every system.
var keyedPtyRefused = true
