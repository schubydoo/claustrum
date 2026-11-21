//go:build windows

package main

// sessionOffTyped is true on Windows: a process.spawn with a sessionFoldersOff of
// another JSON type than a bool gets Invalid params (sessionOffTypeRefused). The
// tests set it on every system.
var sessionOffTyped = true
