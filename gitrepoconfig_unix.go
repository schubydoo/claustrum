//go:build unix

package main

import "syscall"

// openNoTerminal is added to the open flags of the config read. Without it, an
// open of a terminal makes that terminal the controlling terminal of a daemon
// that has none. With a config that is a link to a pty slave, 89cb6289 and
// 5fd08069 stay up when the pty master closes (cell V17, Linux VM, and cell N3,
// macOS VM, 5 runs).
const openNoTerminal = syscall.O_NOCTTY

// configLinkDetail has no refusal on Linux and macOS. A config that is a symbolic
// link to a regular file is read there (cell C4, Linux VM).
func configLinkDetail(string) string { return "" }

// includeValueDetail and includePlaceDetail have no refusal on Linux and macOS.
// An absolute include, a `~/` include and a relative include are followed there
// (cells C3, Linux VM).
func includeValueDetail(string, string) string { return "" }

func includePlaceDetail(string, string) string { return "" }
